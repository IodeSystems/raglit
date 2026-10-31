package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit hint` and `raglit doctype` — what a person tells the models about
// THIS corpus.
//
// Both exist for the same reason: a model reading one document is answering a
// general question about a specific collection, and the collection is the half
// it cannot see. The hint is prose — the conventions, the abbreviations, the
// ambiguities and which way they resolve. A document type is the structured
// form of the same knowledge: these documents are forms, here are their fields,
// here is how to read them.

func runHint(args []string) error {
	fs := flag.NewFlagSet("hint", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	set := fs.String("set", "", "record this hint (- reads stdin)")
	clear := fs.Bool("clear", false, "remove the hint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	switch {
	case *clear:
		if err := t.send(http.MethodPut, "/api/index-hint", map[string]any{"hint": ""}, nil); err != nil {
			return err
		}
		fmt.Println("hint cleared")
		return nil
	case *set != "":
		hint := *set
		if hint == "-" {
			b, err := readAllStdin()
			if err != nil {
				return err
			}
			hint = string(b)
		}
		if err := t.send(http.MethodPut, "/api/index-hint", map[string]any{"hint": hint}, nil); err != nil {
			return err
		}
		// Said plainly because it is not obvious and it costs money: the hint is
		// part of the reading recipe, so documents already pooled under the old
		// one are not re-read by this command.
		fmt.Println("hint recorded. It reaches the page transcription, the segmentation,")
		fmt.Println("the identity and every extraction — and it is part of the reading recipe,")
		fmt.Println("so documents ALREADY read keep the reading they have. Re-ingest to apply it.")
		return nil
	}
	var out struct {
		Hint string `json:"hint"`
	}
	if err := t.get("/api/index-hint", nil, &out); err != nil {
		return err
	}
	if out.Hint == "" {
		fmt.Println("no hint — `raglit hint --set \"...\"` records one")
		return nil
	}
	fmt.Println(out.Hint)
	return nil
}

func runDocType(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("doctype: one of list | show | propose | add | rm")
	}
	switch args[0] {
	case "list":
		return docTypeList(args[1:])
	case "show":
		return docTypeShow(args[1:])
	case "propose":
		return docTypePropose(args[1:])
	case "add":
		return docTypeAdd(args[1:])
	case "rm", "remove", "delete":
		return docTypeRemove(args[1:])
	default:
		return fmt.Errorf("doctype: unknown subcommand %q — one of list | show | propose | add | rm", args[0])
	}
}

// docTypes fetches the index's registered types and their coverage.
func docTypes(t target) ([]raglit.DocType, []raglit.FieldsCoverage, error) {
	var out struct {
		Types    []raglit.DocType        `json:"types"`
		Coverage []raglit.FieldsCoverage `json:"coverage"`
	}
	if err := t.get("/api/doc-types", nil, &out); err != nil {
		return nil, nil, err
	}
	return out.Types, out.Coverage, nil
}

func docTypeList(args []string) error {
	fs := flag.NewFlagSet("doctype list", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tg, err := resolve()
	if err != nil {
		return err
	}
	types, cov, err := docTypes(tg)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(struct {
			Types []raglit.DocType `json:"types"`
		}{types})
	}
	if len(types) == 0 {
		fmt.Println("no document types — `raglit doctype propose <name> <DOC>...` reads examples and proposes one")
		return nil
	}
	byType := map[string]raglit.FieldsCoverage{}
	for _, c := range cov {
		byType[c.Type] = c
	}
	for _, t := range types {
		c := byType[t.Name]
		fmt.Printf("%s  —  %d resolved, %d extracted", t.Name, c.Resolved, c.Extracted)
		if c.Stale > 0 {
			fmt.Printf(", %d STALE (`raglit fields` re-runs them)", c.Stale)
		}
		fmt.Println()
		if t.Description != "" {
			fmt.Printf("    %s\n", t.Description)
		}
		if f := t.FieldNames(); len(f) > 0 {
			fmt.Printf("    fields: %s\n", strings.Join(f, ", "))
		}
	}
	return nil
}

func docTypeShow(args []string) error {
	fs := flag.NewFlagSet("doctype show", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("doctype show: name exactly one type")
	}
	tg, err := resolve()
	if err != nil {
		return err
	}
	types, _, err := docTypes(tg)
	if err != nil {
		return err
	}
	want := raglit.NormalizeTypeName(fs.Arg(0))
	var t *raglit.DocType
	for i := range types {
		if raglit.NormalizeTypeName(types[i].Name) == want {
			t = &types[i]
		}
	}
	if t == nil {
		return fmt.Errorf("doctype show: no document type %q", fs.Arg(0))
	}
	if *asJSON {
		return printJSON(t)
	}
	fmt.Printf("%s\n\n%s\n", t.Name, t.Description)
	if t.Prompt != "" {
		fmt.Printf("\nHOW TO READ ONE:\n%s\n", t.Prompt)
	}
	fmt.Printf("\nSCHEMA:\n%s\n", indentJSON(t.Schema))
	if len(t.Gold) > 0 {
		fmt.Printf("\nproposed from: %s\n", strings.Join(t.Gold, ", "))
	}
	return nil
}

// docTypePropose is the authoring step: a person names the type and points at
// documents that ARE one, and the model proposes the schema and the reading
// instructions. Printed rather than stored unless --save, because a schema
// nobody read before it started filling in records is a schema nobody will
// trust afterwards.
func docTypePropose(args []string) error {
	fs := flag.NewFlagSet("doctype propose", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	save := fs.Bool("save", false, "register the proposal as-is (default: print it for review)")
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("doctype propose: name the type, then one or more example documents")
	}
	tg, err := resolve()
	if err != nil {
		return err
	}
	var out struct {
		Type  raglit.DocType `json:"type"`
		Stale int            `json:"stale"`
	}
	if err := tg.post("/api/doc-types/propose", map[string]any{
		"name": fs.Arg(0), "docs": fs.Args()[1:], "save": *save}, &out); err != nil {
		return err
	}
	t := out.Type
	if *asJSON {
		return printJSON(t)
	}
	fmt.Printf("%s\n\n%s\n", t.Name, t.Description)
	if t.Prompt != "" {
		fmt.Printf("\nHOW TO READ ONE:\n%s\n", t.Prompt)
	}
	fmt.Printf("\nSCHEMA:\n%s\n", indentJSON(t.Schema))
	fmt.Printf("\nproposed from %d document(s): %s\n", len(t.Gold), strings.Join(t.Gold, ", "))
	if !*save {
		fmt.Println("\nNOT registered. Review it, then re-run with --save,")
		fmt.Println("or edit the JSON and register it with: raglit doctype add --file <f> <name>")
		return nil
	}
	reportInvalidated(out.Stale)
	return nil
}

// docTypeAdd registers a type from a JSON file — the path for a schema a person
// wrote or edited after reading a proposal.
func docTypeAdd(args []string) error {
	fs := flag.NewFlagSet("doctype add", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	file := fs.String("file", "", "JSON file holding {description, prompt, schema} (- reads stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *file == "" {
		return fmt.Errorf("doctype add: usage is `doctype add --file <F> <NAME>`")
	}
	var b []byte
	var err error
	if *file == "-" {
		b, err = readAllStdin()
	} else {
		b, err = os.ReadFile(*file)
	}
	if err != nil {
		return err
	}
	var t raglit.DocType
	if err := json.Unmarshal(b, &t); err != nil {
		return fmt.Errorf("doctype add: %w", err)
	}
	t.Name = fs.Arg(0)
	tg, err := resolve()
	if err != nil {
		return err
	}
	var out struct {
		Type  raglit.DocType `json:"type"`
		Stale int            `json:"stale"`
	}
	if err := tg.send(http.MethodPut, "/api/doc-types", t, &out); err != nil {
		return err
	}
	fmt.Printf("registered %q with %d field(s)\n", raglit.NormalizeTypeName(t.Name), len(t.FieldNames()))
	reportInvalidated(out.Stale)
	return nil
}

// reportInvalidated says what a registration just made stale.
//
// Said HERE because this is the moment somebody can act on it: an extraction
// written under the old schema still carries the right type name and a
// plausible record, and the field just added is simply absent from it — which
// reads exactly like a document that did not state one. Nothing else in the
// output would ever mention it.
func reportInvalidated(n int) {
	if n == 0 {
		return
	}
	fmt.Printf("\n%d extraction(s) answer the PREVIOUS schema and are now stale.\n", n)
	fmt.Println("They still read as complete records — the fields you just added are")
	fmt.Println("absent from them, which looks like a document that did not state one.")
	fmt.Println("`raglit fields` re-runs them; `raglit fields --list` names them.")
}

func docTypeRemove(args []string) error {
	fs := flag.NewFlagSet("doctype rm", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("doctype rm: name exactly one type")
	}
	tg, err := resolve()
	if err != nil {
		return err
	}
	if _, err := daemonDelete(tg.base, "/api/doc-types", url.Values{"index": {tg.index}, "name": {fs.Arg(0)}}); err != nil {
		return err
	}
	// Said plainly: the extractions are what documents SAID, and removing a type
	// is a statement about the index's vocabulary, not a retraction of readings.
	fmt.Println("removed. The extractions already made under it are kept —")
	fmt.Println("they are what those documents said, and this only stops new ones.")
	return nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// indentJSON re-indents a stored schema for reading, falling back to the raw
// text when it is not valid JSON (which SetDocType refuses, so this is only
// reachable for a row written before that check).
func indentJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

func readAllStdin() ([]byte, error) { return io.ReadAll(os.Stdin) }
