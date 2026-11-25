package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// The re-embed's operator surface: how far along the run is, and a way to start
// the index an operator searches most first. The queue both handlers read is
// the daemon's reembedQueue (httpd.go), owned by the backfill goroutine.

// reembedProgressOp reports the re-embed run: the index being re-embedded now,
// how many of its fragments still have no vector, and the indexes waiting in
// order. Empty when the run is done (or no embed model is configured).
func reembedProgressOp(reg *raglit.Registry) func(context.Context, *struct{}) (*raglit.ReembedProgress, error) {
	return func(ctx context.Context, _ *struct{}) (*raglit.ReembedProgress, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		p := reembedQueue.Progress()
		return &p, nil
	}
}

// reembedPriorityIn names the index to start first.
type reembedPriorityIn struct {
	Index string `query:"index"`
	Body  struct {
		Index string `json:"index" doc:"the index to move to the front of the re-embed queue"`
	}
}

// reembedPriorityOp moves a waiting index to the front of the re-embed queue.
// The current index is unchanged: a re-embed in flight finishes before the
// reorder takes effect. Unknown names are ignored.
func reembedPriorityOp(reg *raglit.Registry) func(context.Context, *reembedPriorityIn) (*okOut, error) {
	return func(ctx context.Context, in *reembedPriorityIn) (*okOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		name := in.Body.Index
		if name == "" {
			name = in.Index
		}
		if name == "" {
			return nil, huma.Error400BadRequest("no index named")
		}
		reembedQueue.SetPriority(name)
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

// runReembed is `raglit reembed progress` and `raglit reembed priority INDEX`.
func runReembed(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: raglit reembed progress | priority INDEX")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "progress":
		return reembedProgressCLI(rest)
	case "priority":
		return reembedPriorityCLI(rest)
	default:
		return fmt.Errorf("usage: raglit reembed progress | priority INDEX")
	}
}

func reembedProgressCLI(args []string) error {
	fs := flag.NewFlagSet("reembed progress", flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := controlBase(*daemon)
	b, err := daemonGet(base, "/api/reembed/progress", nil)
	if err != nil {
		return err
	}
	var p raglit.ReembedProgress
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.Current == "" {
		fmt.Println("nothing re-embedding")
		return nil
	}
	fmt.Printf("re-embedding %s: %d fragment(s) left\n", p.Current, p.Remaining)
	if len(p.Waiting) > 0 {
		fmt.Printf("waiting: %s\n", strings.Join(p.Waiting, ", "))
	}
	return nil
}

func reembedPriorityCLI(args []string) error {
	fs := flag.NewFlagSet("reembed priority", flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := controlBase(*daemon)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: raglit reembed priority INDEX")
	}
	for _, idx := range fs.Args() {
		if _, err := daemonPostJSON(base, "/api/reembed/priority?"+urlValues("index", idx).Encode(),
			map[string]any{"index": idx}); err != nil {
			return fmt.Errorf("reembed priority %s: %w", idx, err)
		}
		fmt.Printf("prioritized %s\n", idx)
	}
	return nil
}
