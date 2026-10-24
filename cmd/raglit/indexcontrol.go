package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
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

type scopeIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Scopes  *[]string `json:"scopes,omitempty" doc:"replace the scopes; [] makes the index global"`
		Add     []string  `json:"add,omitempty" doc:"scopes to add"`
		Remove  []string  `json:"remove,omitempty" doc:"scopes to remove"`
		Ambient *bool     `json:"ambient,omitempty" doc:"a global index: true = every search reads it, false = only when named"`
		// Claim sets the scopes only when they were never set: an ingest from
		// a project gives a new index that project's directory.
		Claim string `json:"claim,omitempty" doc:"set scopes to [claim] if they were never set"`
	}
}

type scopeOut struct {
	Body struct {
		Scopes  []string `json:"scopes"`
		Ambient bool     `json:"ambient"`
	}
}

func scopeOp(reg *raglit.Registry) func(context.Context, *scopeIn) (*scopeOut, error) {
	return func(ctx context.Context, in *scopeIn) (*scopeOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if err := applyScope(st, in.Body.Scopes, in.Body.Add, in.Body.Remove, in.Body.Ambient, in.Body.Claim); err != nil {
			return nil, huma.Error500InternalServerError("scope", err)
		}
		out := &scopeOut{}
		out.Body.Scopes, _ = st.Scopes()
		out.Body.Ambient = st.Ambient()
		return out, nil
	}
}

func applyScope(st *raglit.Store, replace *[]string, add, remove []string, ambient *bool, claim string) error {
	cur, set := st.Scopes()
	switch {
	case claim != "":
		if set {
			break
		}
		cur = []string{claim}
	case replace != nil:
		cur = *replace
	}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[filepath.Clean(r)], drop[r] = true, true
	}
	next := []string{}
	for _, c := range append(cur, add...) {
		if !drop[c] {
			next = append(next, c)
		}
	}
	if claim == "" || !set {
		if _, err := st.SetScopes(next); err != nil {
			return err
		}
	}
	if ambient != nil {
		return st.SetAmbient(*ambient)
	}
	return nil
}

// runPause is `raglit pause` and `raglit resume`. With no index named it lists
// every index and whether it is paused.
func runPause(verb string, args []string) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := controlBase(*daemon)
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
			state := "active"
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
		fmt.Printf("%sd %s\n", verb, idx)
	}
	return nil
}

type ignoreIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
}
type ignorePutIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Patterns *[]string `json:"patterns,omitempty" doc:"this index's ignore globs, matched against a path at enqueue; **/dir/** for a directory anywhere, a glob with no / for a file name. Omitted: unchanged"`
		Allow    *[]string `json:"allow,omitempty" doc:"globs exempt from the global (builtin) ignores on this index. Omitted: unchanged"`
	}
}
type ignoreOut struct {
	Body struct {
		Patterns []string `json:"patterns"`
		// Builtin is what every index refuses unless Allow exempts it.
		Builtin []string `json:"builtin"`
		Allow   []string `json:"allow"`
	}
}

func ignoreView(st *raglit.Store) *ignoreOut {
	out := &ignoreOut{}
	out.Body.Patterns = st.IgnorePatterns()
	out.Body.Builtin = raglit.EnqueueBuiltinIgnore()
	out.Body.Allow = st.AllowPatterns()
	return out
}

func getIgnoreOp(reg *raglit.Registry) func(context.Context, *ignoreIn) (*ignoreOut, error) {
	return func(ctx context.Context, in *ignoreIn) (*ignoreOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		return ignoreView(st), nil
	}
}

func putIgnoreOp(reg *raglit.Registry) func(context.Context, *ignorePutIn) (*ignoreOut, error) {
	return func(ctx context.Context, in *ignorePutIn) (*ignoreOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if in.Body.Patterns != nil {
			if _, err := st.SetIgnorePatterns(*in.Body.Patterns); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
		}
		if in.Body.Allow != nil {
			if _, err := st.SetAllowPatterns(*in.Body.Allow); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
		}
		return ignoreView(st), nil
	}
}

type purgeIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Pattern string `json:"pattern" doc:"glob matched against each document path, like an ignore pattern"`
		DryRun  bool   `json:"dry_run,omitempty" doc:"count and sample what would go; remove nothing"`
	}
}
type purgeOut struct {
	Body raglit.PurgeResult
}

func purgeOp(reg *raglit.Registry) func(context.Context, *purgeIn) (*purgeOut, error) {
	return func(ctx context.Context, in *purgeIn) (*purgeOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		res, err := st.Purge(in.Body.Pattern, in.Body.DryRun)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if !in.Body.DryRun {
			log.Printf("raglit: %s: purged %d document(s), %d ingest and %d caption job(s) matching %q",
				in.Index, res.Documents, res.IngestJobs, res.CaptionJobs, res.Pattern)
		}
		return &purgeOut{Body: res}, nil
	}
}

type pruneIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		DryRun bool `json:"dry_run,omitempty" doc:"count what would go, by directory; remove nothing"`
		// Separate from dry_run: the copies are reported always and removed only
		// when asked (USER 2026-10-24).
		DeleteOrphanOriginals bool `json:"delete_orphan_originals,omitempty" doc:"also remove the dangling copies under originals/ (never an upload's), dry run or not"`
	}
}
type pruneOut struct {
	Body raglit.PruneResult
}

func pruneOp(reg *raglit.Registry) func(context.Context, *pruneIn) (*pruneOut, error) {
	return func(ctx context.Context, in *pruneIn) (*pruneOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		res, err := st.Prune(in.Body.DryRun, in.Body.DeleteOrphanOriginals)
		if err != nil {
			return nil, huma.Error500InternalServerError("prune", err)
		}
		if res.Removed {
			log.Printf("raglit: %s: pruned %d document(s) whose file is gone, %d orphan page image(s)",
				in.Index, res.Documents, res.OrphanPages)
		}
		if res.OriginalsRemoved && res.DanglingOriginals > 0 {
			log.Printf("raglit: %s: removed %d dangling original(s)", in.Index, res.DanglingOriginals)
		}
		return &pruneOut{Body: res}, nil
	}
}

// runPurge is `raglit purge INDEX PATTERN`: a dry run unless --yes.
func runPurge(args []string) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	yes := fs.Bool("yes", false, "remove the documents; without it, only report what would go")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("purge: usage: raglit purge [--yes] INDEX PATTERN")
	}
	idx, pattern := fs.Arg(0), fs.Arg(1)
	b, err := daemonPostJSON(controlBase(*daemon), "/api/index/purge?"+urlValues("index", idx).Encode(),
		map[string]any{"pattern": pattern, "dry_run": !*yes})
	if err != nil {
		return err
	}
	var res raglit.PurgeResult
	if err := json.Unmarshal(b, &res); err != nil {
		return err
	}
	for _, p := range res.Sample {
		fmt.Println(p)
	}
	if res.Documents > len(res.Sample) {
		fmt.Printf("… and %d more\n", res.Documents-len(res.Sample))
	}
	verb := "would purge"
	if *yes {
		verb = "purged"
	}
	fmt.Printf("%s %d document(s), %d pending import and %d pending caption job(s) from %s; files on your disk are untouched, uploaded files are deleted\n",
		verb, res.Documents, res.IngestJobs, res.CaptionJobs, idx)
	if !*yes && res.Documents+res.IngestJobs+res.CaptionJobs > 0 {
		fmt.Println("run again with --yes to remove them")
	}
	return nil
}

// runPrune is `raglit prune [--index INDEX] [--yes] [--delete-orphan-originals]`:
// list the documents whose local file is gone, per index and per deleted
// directory, and remove them with --yes; report the page images and originals/
// copies no document needs, and remove the copies with --delete-orphan-originals.
// No --index: every index on the daemon.
func runPrune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	daemon := fs.String("daemon", "", "daemon URL (default: this machine's daemon)")
	index := fs.String("index", "", "prune this index only (the daemon's name)")
	yes := fs.Bool("yes", false, "remove the documents and orphan page images; without it, only report what would go")
	originals := fs.Bool("delete-orphan-originals", false, "remove the dangling copies under originals/ (never an upload's)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("prune: usage: raglit prune [--index INDEX] [--yes] [--delete-orphan-originals]")
	}
	base := controlBase(*daemon)
	names := []string{*index}
	if *index == "" {
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
		names = names[:0]
		for _, r := range out.Indexes {
			names = append(names, r.Name)
		}
	}
	var total, pages, origs int
	var pageBytes, origBytes int64
	for _, idx := range names {
		b, err := daemonPostJSON(base, "/api/index/prune?"+urlValues("index", idx).Encode(),
			map[string]any{"dry_run": !*yes, "delete_orphan_originals": *originals})
		if err != nil {
			if *index == "" {
				// One index the caller cannot write must not stop the rest.
				fmt.Fprintf(os.Stderr, "%s: %v\n", idx, err)
				continue
			}
			return err
		}
		var res raglit.PruneResult
		if err := json.Unmarshal(b, &res); err != nil {
			return err
		}
		pages, pageBytes = pages+res.OrphanPages, pageBytes+res.OrphanPageBytes
		origs, origBytes = origs+res.DanglingOriginals, origBytes+res.DanglingOriginalBytes
		if res.Documents == 0 {
			continue
		}
		total += res.Documents
		fmt.Printf("%s: %d local file(s) not found\n", idx, res.Documents)
		for _, d := range res.Dirs {
			fmt.Printf("  %6d  %s\n", d.Documents, d.Dir)
		}
	}
	switch {
	case total == 0:
		fmt.Println("no local file missing: every indexed file is still on disk")
	case *yes:
		fmt.Printf("pruned %d document(s) whose local file was not found\n", total)
	default:
		fmt.Printf("%d document(s) whose local file was not found (already out of search); --yes removes them\n", total)
	}
	switch {
	case pages == 0:
	case *yes:
		fmt.Printf("removed %d orphan page image(s), %s\n", pages, humanBytes(pageBytes))
	default:
		fmt.Printf("%d orphan page image(s), %s, belong to no document; --yes removes them\n", pages, humanBytes(pageBytes))
	}
	switch {
	case origs == 0:
	case *originals:
		fmt.Printf("removed %d dangling original(s), %s\n", origs, humanBytes(origBytes))
	default:
		fmt.Printf("%d dangling original(s), %s, under originals/; --delete-orphan-originals removes them\n", origs, humanBytes(origBytes))
	}
	return nil
}

// controlBase is the daemon pause, resume and purge talk to: --daemon, else this machine's.
func controlBase(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	addr := ""
	if st, ok := readDaemonState(raglit.DefaultRoot()); ok {
		addr = st.Addr
	}
	return localBase(addr)
}

// humanBytes is n as KB, MB or GB, one decimal.
func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	}
}
