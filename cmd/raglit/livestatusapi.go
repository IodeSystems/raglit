package main

import (
	"context"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// GET /api/status: the daemon's live status, for a poller (`raglit status
// --json`, dun's ps). Every index the CALLER may read, and only those: the
// filter is the registry's ACL (Registry.Readable), applied here in the
// daemon, so a client cannot widen it. A session key sees its project's
// indexes; the admin sees every index (Carl 2026-11-17: no new key scope).
//
// It takes no lock that ingest or embed work holds (raglit/livestatus.go), so
// it answers while the daemon is busy. Field names are a contract: add
// fields, never rename one.

// daemonStarted is when this process started, for reading the since-start
// totals.
var daemonStarted = time.Now()

type liveStatusIn struct {
	Index string `query:"index" doc:"index selector, as /status takes it; empty: every index the caller may read"`
}

type liveEmbed struct {
	// Backend is "local" (the embed endpoint is on this host's loopback),
	// "remote" (any other host), or "none" (no embed model).
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	// InFlight is the embed requests holding a slot in the embed model's
	// admission channel now, every index and every query together.
	InFlight int `json:"in_flight"`
	// BackedOff is true while a 429 is holding the embed channel's width down.
	BackedOff bool `json:"backed_off"`
}

type liveStatusOut struct {
	Body struct {
		Version   string             `json:"version"`
		StartedAt string             `json:"started_at"` // RFC3339; the embed totals count from here
		Embed     liveEmbed          `json:"embed"`
		Indexes   []raglit.IndexLive `json:"indexes"`
	}
}

func liveStatusOp(reg *raglit.Registry, lf *llmFlags) func(context.Context, *liveStatusIn) (*liveStatusOut, error) {
	return func(ctx context.Context, in *liveStatusIn) (*liveStatusOut, error) {
		out := &liveStatusOut{}
		out.Body.Version = version
		out.Body.StartedAt = daemonStarted.UTC().Format(time.RFC3339)
		out.Body.Indexes = []raglit.IndexLive{}

		// Any model held down after a 429 makes outstanding work "backed-off":
		// a job waits on whichever model it needs, and which one that is is not
		// visible from here.
		var chans []raglit.ChannelStat
		if lf != nil && lf.chans != nil {
			chans = lf.chans.Stats()
		}
		anyChilled := false
		for _, c := range chans {
			anyChilled = anyChilled || c.Chilled
		}
		out.Body.Embed = embedSummary(lf, chans)

		seen := map[string]bool{}
		for _, name := range reg.Readable(ctx, selectIndexes(ctx, reg, in.Index)) {
			if seen[name] {
				continue
			}
			seen[name] = true
			st, err := reg.Existing(name) // a read: never create
			if err != nil {
				continue
			}
			l, err := st.LiveStatus(ctx, anyChilled)
			if err != nil {
				continue
			}
			l.Name = name
			l.Project, l.Index = splitNS(name)
			out.Body.Indexes = append(out.Body.Indexes, l)
		}
		sort.Slice(out.Body.Indexes, func(i, j int) bool { return out.Body.Indexes[i].Name < out.Body.Indexes[j].Name })
		return out, nil
	}
}

// embedSummary is the daemon's embed backend and what is in flight on it.
func embedSummary(lf *llmFlags, chans []raglit.ChannelStat) liveEmbed {
	e := liveEmbed{Backend: "none"}
	if lf == nil || lf.embedModel == nil || *lf.embedModel == "" {
		return e
	}
	e.Model = *lf.embedModel
	e.Backend = embedBackend(lf.or(lf.embed).url)
	for _, c := range chans {
		if c.Model == e.Model {
			e.InFlight, e.BackedOff = c.InFlight, c.Chilled
		}
	}
	return e
}

// embedBackend is "local" for an endpoint on this host's loopback, else
// "remote".
func embedBackend(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "remote"
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return "local"
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return "local"
	}
	return "remote"
}
