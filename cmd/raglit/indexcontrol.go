package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/raglit"
)

// Index controls over HTTP and the CLI: pause and resume. See
// raglit/indexcontrol.go.

type pauseIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Paused bool `json:"paused" doc:"true pauses the index, false resumes it"`
	}
}

func pauseOp(reg *raglit.Registry) func(context.Context, *pauseIn) (*okOut, error) {
	return func(ctx context.Context, in *pauseIn) (*okOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if err := st.SetPaused(in.Body.Paused); err != nil {
			return nil, huma.Error500InternalServerError("pause", err)
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

// runPause is `raglit pause` and `raglit resume`. With no index named it lists
// every index and whether it is paused.
func runPause(verb string, args []string) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := *daemon
	if base == "" {
		addr := ""
		if st, ok := readDaemonState(raglit.DefaultRoot()); ok {
			addr = st.Addr
		}
		base = localBase(addr)
	}
	if fs.NArg() == 0 {
		b, err := daemonGet(base, "/indexes", nil)
		if err != nil {
			return err
		}
		var out struct {
			Indexes []idxRow `json:"indexes"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return err
		}
		for _, r := range out.Indexes {
			state := "running"
			if r.Paused {
				state = "paused"
			}
			fmt.Printf("%-8s %s\n", state, r.Name)
		}
		return nil
	}
	for _, idx := range fs.Args() {
		body := map[string]any{"paused": verb == "pause"}
		if _, err := daemonPostJSON(base, "/api/index/pause?"+urlValues("index", idx).Encode(), body); err != nil {
			return fmt.Errorf("%s %s: %w", verb, idx, err)
		}
		fmt.Printf("%sd %s\n", strings.TrimSuffix(verb, "e"), idx)
	}
	return nil
}

type ignoreIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
}
type ignorePutIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Patterns []string `json:"patterns" doc:"globs matched against a path at enqueue; **/dir/** for a directory anywhere, a glob with no / for a file name"`
	}
}
type ignoreOut struct {
	Body struct {
		Patterns []string `json:"patterns"`
		// Builtin is what every index refuses whatever its own list.
		Builtin []string `json:"builtin"`
	}
}

func ignoreView(patterns []string) *ignoreOut {
	out := &ignoreOut{}
	out.Body.Patterns = patterns
	out.Body.Builtin = raglit.EnqueueBuiltinIgnore()
	return out
}

func getIgnoreOp(reg *raglit.Registry) func(context.Context, *ignoreIn) (*ignoreOut, error) {
	return func(ctx context.Context, in *ignoreIn) (*ignoreOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		return ignoreView(st.IgnorePatterns()), nil
	}
}

func putIgnoreOp(reg *raglit.Registry) func(context.Context, *ignorePutIn) (*ignoreOut, error) {
	return func(ctx context.Context, in *ignorePutIn) (*ignoreOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		got, err := st.SetIgnorePatterns(in.Body.Patterns)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		return ignoreView(got), nil
	}
}
