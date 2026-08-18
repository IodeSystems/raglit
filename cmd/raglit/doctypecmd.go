package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
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
	openStore, homeOf := addStoreFlags(fs)
	set := fs.String("set", "", "record this hint (- reads stdin)")
	clear := fs.Bool("clear", false, "remove the hint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	switch {
	case *clear:
		if err := st.SetIndexHint("", time.Now().UnixNano()); err != nil {
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
		if err := st.SetIndexHint(hint, time.Now().UnixNano()); err != nil {
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

	h := st.IndexHint()
	if h == "" {
		fmt.Println("no hint — `raglit hint --set \"...\"` records one")
		return nil
	}
	fmt.Println(h)
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

func docTypeList(args []string) error {
	fs := flag.NewFlagSet("doctype list", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	types, err := st.DocTypes()
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
	cov, err := st.FieldsCoverage()
	if err != nil {
		return err
	}
	byType := map[string]raglit.FieldsCoverage{}
	for _, c := range cov {
		byType[c.Type] = c
	}
	for _, t := range types {
		c := byType[t.Name]
		fmt.Printf("%s  —  %d resolved, %d extracted\n", t.Name, c.Resolved, c.Extracted)
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
	openStore, homeOf := addStoreFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("doctype show: name exactly one type")
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	t, err := st.DocTypeByName(fs.Arg(0))
	if err != nil {
		return err
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
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	save := fs.Bool("save", false, "register the proposal as-is (default: print it for review)")
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("doctype propose: name the type, then one or more example documents")
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	lf.resolve(homeOf())
	id := lf.identifier(homeOf())
	if id == nil {
		return fmt.Errorf("doctype propose: no identity model configured — run 'raglit init' or set identity_model")
	}
	st.SetIdentifier(id)

	name := fs.Arg(0)
	var gold []string
	for _, ref := range fs.Args()[1:] {
		p, err := resolveOneDoc(st, ref)
		if err != nil {
			return err
		}
		gold = append(gold, p)
	}
	t, err := st.ProposeDocType(context.Background(), name, gold)
	if err != nil {
		return err
	}
	if *save {
		if err := st.SetDocType(t); err != nil {
			return err
		}
	}
	if *asJSON {
		return printJSON(t)
	}
	fmt.Printf("%s\n\n%s\n", t.Name, t.Description)
	if t.Prompt != "" {
		fmt.Printf("\nHOW TO READ ONE:\n%s\n", t.Prompt)
	}
	fmt.Printf("\nSCHEMA:\n%s\n", indentJSON(t.Schema))
	fmt.Printf("\nproposed from %d document(s): %s\n", len(gold), strings.Join(gold, ", "))
	if !*save {
		fmt.Println("\nNOT registered. Review it, then re-run with --save,")
		fmt.Println("or edit the JSON and register it with: raglit doctype add <name> --file <f>")
	}
	return nil
}

// docTypeAdd registers a type from a JSON file — the path for a schema a person
// wrote or edited after reading a proposal.
func docTypeAdd(args []string) error {
	fs := flag.NewFlagSet("doctype add", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	file := fs.String("file", "", "JSON file holding {description, prompt, schema} (- reads stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *file == "" {
		return fmt.Errorf("doctype add: name the type and pass --file")
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
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.SetDocType(t); err != nil {
		return err
	}
	fmt.Printf("registered %q with %d field(s)\n", raglit.NormalizeTypeName(t.Name), len(t.FieldNames()))
	return nil
}

func docTypeRemove(args []string) error {
	fs := flag.NewFlagSet("doctype rm", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("doctype rm: name exactly one type")
	}
	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.DeleteDocType(fs.Arg(0)); err != nil {
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
