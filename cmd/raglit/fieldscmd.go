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
	// Aggregate field query dispatches before the top-level parse: its flags
	// (--eq, --gt, …) are not in the top-level set and would be rejected.
	// "query" appears as a positional arg after the target flags.
	for i, a := range args {
		if a == "query" {
			tfs := flag.NewFlagSet("fields query", flag.ContinueOnError)
			resolve := addTargetFlags(tfs)
			if err := tfs.Parse(args[:i]); err != nil {
				return err
			}
			t, err := resolve()
			if err != nil {
				return err
			}
			asJSON := false
			queryArgs := []string{}
			for _, q := range args[i+1:] {
				if q == "--json" {
					asJSON = true
				} else {
					queryArgs = append(queryArgs, q)
				}
			}
			return runFieldsQuery(t, queryArgs, asJSON)
		}
	}

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

// runFieldsQuery is `raglit fields query`: an aggregate query over extracted
// document fields. Constraints on field values select documents; the result is
// the matching documents with their field values, not fragments.
func runFieldsQuery(t target, args []string, asJSON bool) error {
	fs := flag.NewFlagSet("fields query", flag.ContinueOnError)
	eq := fs.StringArray("eq", nil, "equality constraint: field=value (repeatable)")
	gt := fs.StringArray("gt", nil, "numeric constraint: field=value (repeatable)")
	gte := fs.StringArray("gte", nil, "numeric constraint: field=value (repeatable)")
	lt := fs.StringArray("lt", nil, "numeric constraint: field=value (repeatable)")
	lte := fs.StringArray("lte", nil, "numeric constraint: field=value (repeatable)")
	sortField := fs.String("sort", "", "field to order the result by")
	dir := fs.String("dir", "asc", "asc | desc")
	limit := fs.Int("limit", 0, "at most this many documents (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	type fconstraint struct {
		Field string `json:"field"`
		Op    string `json:"op"`
		Value string `json:"value"`
	}
	var constraints []fconstraint
	add := func(arr []string, op string) error {
		for _, s := range arr {
			f, v, ok := strings.Cut(s, "=")
			if !ok || f == "" {
				return fmt.Errorf("fields query: %s: expected field=value, got %q", op, s)
			}
			constraints = append(constraints, fconstraint{f, op, v})
		}
		return nil
	}
	if err := add(*eq, "eq"); err != nil {
		return err
	}
	if err := add(*gt, "gt"); err != nil {
		return err
	}
	if err := add(*gte, "gte"); err != nil {
		return err
	}
	if err := add(*lt, "lt"); err != nil {
		return err
	}
	if err := add(*lte, "lte"); err != nil {
		return err
	}

	body := struct {
		Constraints []fconstraint `json:"constraints"`
		Sort        *string       `json:"sort,omitempty"`
		Dir         *string       `json:"dir,omitempty"`
		Limit       *int          `json:"limit,omitempty"`
	}{}
	body.Constraints = constraints
	if *sortField != "" {
		body.Sort = sortField
	}
	if *dir != "" {
		body.Dir = dir
	}
	if *limit > 0 {
		body.Limit = limit
	}

	var out struct {
		Documents []struct {
			Path   string `json:"path"`
			Type   string `json:"type"`
			Fields string `json:"fields"`
		} `json:"documents"`
	}
	if err := t.send(http.MethodPost, "/api/fields/query", body, &out); err != nil {
		return err
	}

	if asJSON {
		return printJSON(out)
	}
	if len(out.Documents) == 0 {
		fmt.Println("no documents matched")
		return nil
	}
	for _, d := range out.Documents {
		fmt.Printf("%s\n    %s\n%s\n", d.Path, d.Type, indentJSON(json.RawMessage(d.Fields)))
	}
	fmt.Printf("%d document(s)\n", len(out.Documents))
	return nil
}
