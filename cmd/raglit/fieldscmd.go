package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
)

// `raglit fields` — the filled-out schema of documents that ARE forms.
//
// The same shape as `raglit identify`, and for the same reasons: the work is a
// bounded model call per document, a corpus is hundreds of them, so the command
// records the work durably and gets out of the way. It is the same queue and
// the same rows — a fields job is an identity job with a different ask.
func runFields(args []string) error {
	fs := flag.NewFlagSet("fields", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	list := fs.Bool("list", false, "coverage per document type (no model calls)")
	force := fs.Bool("force", false, "re-extract documents that already have fields (a person's is never replaced)")
	dry := fs.Bool("dry-run", false, "name the documents that would be extracted")
	limit := fs.Int("limit", 0, "stop after this many documents (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable")
	set := fs.String("set", "", "record THESE fields for the document, as JSON (a person's ruling; - reads stdin)")
	by := fs.String("by", defaultWithdrawBy(), "who is recording them (with --set)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()

	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	lf.resolve(homeOf())

	if *list {
		return printFieldsCoverage(st, *asJSON)
	}

	// A person's ruling: one document, recorded, done.
	if strings.TrimSpace(*set) != "" {
		if len(targets) != 1 {
			return fmt.Errorf("fields: name exactly one document to record fields for")
		}
		path, err := resolveOneDoc(st, targets[0])
		if err != nil {
			return err
		}
		body := *set
		if body == "-" {
			b, err := readAllStdin()
			if err != nil {
				return err
			}
			body = string(b)
		}
		got, err := st.RecordFields(context.Background(), path,
			raglit.DocFields{Fields: json.RawMessage(body)}, *by)
		if err != nil {
			return err
		}
		return printFields(path, got, *asJSON)
	}

	// Reading one document's fields back.
	if len(targets) == 1 && !*dry {
		path, err := resolveOneDoc(st, targets[0])
		if err == nil {
			if f, ferr := st.DocumentFields(path); ferr == nil && !f.Empty() {
				return printFields(path, f, *asJSON)
			}
		}
	}

	paths, err := fieldsTargets(st, targets, *force)
	if err != nil {
		return err
	}
	if *limit > 0 && len(paths) > *limit {
		paths = paths[:*limit]
	}
	if len(paths) == 0 {
		// "Nothing owed" and "nothing wrong" are different sentences. An
		// extraction whose type was removed is not current and cannot be
		// re-run, and saying everything is current would bury that.
		blocked := 0
		if stale, serr := st.FieldsStaleness(); serr == nil {
			for _, s := range stale {
				if s.Reason == raglit.FieldsTypeGone {
					blocked++
				}
			}
		}
		fmt.Println("nothing to extract — every document that resolved as a type has current fields")
		if blocked > 0 {
			fmt.Printf("\nexcept %d whose type is no longer registered, which cannot be re-run\n", blocked)
			fmt.Println("until it is: `raglit fields --list` names them.")
			return nil
		}
		fmt.Println("(`raglit doctype list` shows the types; a corpus with none has nothing to extract)")
		return nil
	}
	if *dry {
		for _, p := range paths {
			fmt.Println(p)
		}
		fmt.Printf("would extract %d document(s)\n", len(paths))
		return nil
	}

	ns, _ := resolveProject("", homeOf)
	routed := ns != "" && !explicitStoreFlag(fs)
	queued, err := enqueueFieldsWork(st, paths, *force, routed)
	if err != nil {
		return err
	}
	q := identityQueueNow(st, routed)
	fmt.Printf("queued %d document(s) — %d pending, %d running, %d done, %d skipped, %d failed\n",
		queued, q.Pending, q.Running, q.Done, q.Skipped, q.Failed)
	if !routed {
		return drainFieldsLocally(context.Background(), st, lf, homeOf())
	}
	fmt.Println("the daemon is working them; `raglit fields --list` shows coverage")
	return nil
}

func enqueueFieldsWork(st *raglit.Store, paths []string, force, routed bool) (int, error) {
	if !routed {
		return st.EnqueueFieldsFor(paths, force)
	}
	n := 0
	for _, p := range paths {
		queued, err := daemonEnqueueFields(p, force)
		if err != nil {
			return n, err
		}
		n += queued
	}
	return n, nil
}

// fieldsTargets is the work list: the named documents, or every document that
// resolved as a registered type and has no extraction yet.
func fieldsTargets(st *raglit.Store, targets []string, force bool) ([]string, error) {
	if len(targets) > 0 {
		docs, err := st.Documents()
		if err != nil {
			return nil, err
		}
		paths := matchIndexed(docs, targets)
		if len(paths) == 0 {
			return nil, fmt.Errorf("fields: nothing indexed under %s", strings.Join(targets, ", "))
		}
		return paths, nil
	}
	if force {
		ids, err := st.Identities()
		if err != nil {
			return nil, err
		}
		var out []string
		for _, id := range ids {
			if strings.TrimSpace(id.DocType) != "" {
				out = append(out, id.Path)
			}
		}
		return out, nil
	}
	// Owed, not merely absent: an extraction written under a schema that has
	// since been edited answers questions nobody is asking any more.
	return st.ExtractableMissing()
}

// drainFieldsLocally works the queue in-process, for an embedded index with no
// daemon behind it.
func drainFieldsLocally(ctx context.Context, st *raglit.Store, lf *llmFlags, home raglit.Home) error {
	id := lf.identifier(home)
	if id == nil {
		return fmt.Errorf("fields: no identity model configured — run 'raglit init' or set identity_model")
	}
	st.SetIdentifier(id)
	cfg, _, _ := raglit.LoadConfig(home)
	w := &raglit.IdentityWorker{Store: st, Slots: cfg.IdentitySlots}
	done, failed := 0, 0
	w.OnDone = func(job raglit.IdentityJob, _ raglit.DocIdentity, err error) {
		switch {
		case errors.Is(err, raglit.ErrNoDocType):
			// Not a failure: most documents are not forms.
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", job.Path, err)
		default:
			done++
			if f, ferr := st.DocumentFields(job.Path); ferr == nil {
				_ = printFields(job.Path, f, false)
			}
		}
	}
	if _, err := w.Drain(ctx); err != nil {
		return err
	}
	fmt.Printf("extracted %d document(s)", done)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()
	return nil
}

func printFieldsCoverage(st *raglit.Store, asJSON bool) error {
	cov, err := st.FieldsCoverage()
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(struct {
			Coverage []raglit.FieldsCoverage `json:"coverage"`
		}{cov})
	}
	if len(cov) == 0 {
		fmt.Println("no document resolved as a registered type")
		fmt.Println("(`raglit doctype list` shows the types; identify assigns them)")
		return nil
	}
	for _, c := range cov {
		fmt.Printf("  %4d resolved  %4d extracted", c.Resolved, c.Extracted)
		if c.Stale > 0 {
			fmt.Printf("  (%d stale)", c.Stale)
		}
		fmt.Printf("  %s\n", c.Type)
	}
	// Named, not just counted: a stale extraction looks right, so the reason is
	// the only thing that says why it is being re-run.
	stale, err := st.FieldsStaleness()
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	fmt.Printf("\n%d extraction(s) are not current:\n", len(stale))
	byReason := map[raglit.FieldsStale][]string{}
	for _, s := range stale {
		byReason[s.Reason] = append(byReason[s.Reason], s.Path)
	}
	for _, r := range []raglit.FieldsStale{
		raglit.FieldsSchemaMoved, raglit.FieldsTextMoved,
		raglit.FieldsTypeChanged, raglit.FieldsTypeGone,
	} {
		paths := byReason[r]
		if len(paths) == 0 {
			continue
		}
		fmt.Printf("  %d — %s\n", len(paths), r.Reason())
		for i, p := range paths {
			if i == 5 {
				fmt.Printf("      … and %d more\n", len(paths)-5)
				break
			}
			fmt.Printf("      %s\n", p)
		}
	}
	if len(byReason[raglit.FieldsTypeGone]) > 0 {
		fmt.Println("\n(a removed type cannot be re-extracted against — register it again, or")
		fmt.Println(" the records stay as they are, which is what those documents said)")
	}
	fmt.Println("\n`raglit fields` re-runs everything that is owed.")
	return nil
}

func printFields(path string, f raglit.DocFields, asJSON bool) error {
	if asJSON {
		return printJSON(struct {
			Path string `json:"path"`
			raglit.DocFields
		}{path, f})
	}
	who := "generated"
	if f.ByPerson() {
		who = "recorded by " + f.Model
	}
	fmt.Printf("%s\n    %s  ·  %s\n%s\n", path, f.Type, who, indentJSON(f.Fields))
	return nil
}
