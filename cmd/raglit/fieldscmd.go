package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit fields` — the filled-out schema of documents that ARE forms.
//
// The same shape as `raglit identify`, and for the same reasons: the work is a
// bounded model call per document, a corpus is hundreds of them, so the command
// records the work durably and gets out of the way. It is the same queue and
// the same rows — a fields job is an identity job with a different ask.
func runFields(args []string) error {
	fs := flag.NewFlagSet("fields", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
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
	t, err := resolve()
	if err != nil {
		return err
	}
	if *list {
		return printFieldsCoverage(t, *asJSON)
	}

	// A person's ruling: one document, recorded, done.
	if strings.TrimSpace(*set) != "" {
		if len(targets) != 1 {
			return fmt.Errorf("fields: name exactly one document to record fields for")
		}
		body := *set
		if body == "-" {
			b, err := readAllStdin()
			if err != nil {
				return err
			}
			body = string(b)
		}
		if !json.Valid([]byte(body)) {
			return fmt.Errorf("fields: --set is not valid JSON")
		}
		var got struct {
			Path string `json:"path"`
			raglit.DocFields
		}
		if err := t.send(http.MethodPut, "/api/fields", map[string]any{
			"path": targets[0], "fields": json.RawMessage(body), "by": *by}, &got); err != nil {
			return err
		}
		return printFields(got.Path, got.DocFields, *asJSON)
	}

	// Reading one document's fields back.
	if len(targets) == 1 && !*dry {
		var f struct {
			Path string `json:"path"`
			raglit.DocFields
		}
		if err := t.get("/api/fields", url.Values{"path": {targets[0]}}, &f); err == nil && !f.Empty() {
			return printFields(f.Path, f.DocFields, *asJSON)
		}
	}

	paths, err := fieldsTargets(t, targets, *force)
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
		if fst, serr := fieldsStatus(t); serr == nil {
			for _, s := range fst.Stale {
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

	queued := 0
	for _, p := range paths {
		n, err := daemonEnqueueAsk(t, p, *force, raglit.IdentityAskFields)
		if err != nil {
			return err
		}
		queued += n
	}
	q, _ := daemonIdentityQueue(t)
	fmt.Printf("queued %d document(s) — %d pending, %d running, %d done, %d skipped, %d failed\n",
		queued, q.Pending, q.Running, q.Done, q.Skipped, q.Failed)
	// The queue holds one row per document, so a document already in flight for
	// its CAPTION cannot also hold an extraction row. That is the sequencing —
	// a caption can establish the type an extraction reads against, so the
	// extraction follows it — and the worker queues the extraction itself when
	// the caption closes. Said out loud, because otherwise the count above just
	// looks short.
	if deferred := len(paths) - queued; deferred > 0 {
		fmt.Printf("%d deferred behind a caption already in flight — the worker queues those\n", deferred)
		fmt.Println("as each caption closes, because a caption is what establishes the type.")
	}
	fmt.Println("the daemon is working them; `raglit fields --list` shows coverage")
	return nil
}

// fieldsTargets is the work list: the named documents, or every document that
// resolved as a registered type and is owed an extraction.
func fieldsTargets(t target, targets []string, force bool) ([]string, error) {
	if len(targets) > 0 {
		docs, err := t.documents()
		if err != nil {
			return nil, err
		}
		paths := matchIndexed(docs, targets)
		if len(paths) == 0 {
			return nil, fmt.Errorf("fields: nothing indexed under %s", strings.Join(targets, ", "))
		}
		return paths, nil
	}
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	var out struct {
		Paths []string `json:"paths"`
	}
	if err := t.get("/api/fields/targets", q, &out); err != nil {
		return nil, err
	}
	return out.Paths, nil
}

type fieldsStatusBody struct {
	Coverage []raglit.FieldsCoverage `json:"coverage"`
	Stale    []raglit.StaleFields    `json:"stale"`
}

func fieldsStatus(t target) (fieldsStatusBody, error) {
	var out fieldsStatusBody
	err := t.get("/api/fields/status", nil, &out)
	return out, err
}

func printFieldsCoverage(t target, asJSON bool) error {
	fst, err := fieldsStatus(t)
	if err != nil {
		return err
	}
	cov := fst.Coverage
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
	stale := fst.Stale
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
