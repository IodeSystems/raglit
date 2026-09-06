package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// `raglit audit-tags` — the tag vocabulary of an index, and the drift in it.
//
// The identity pipeline (identity.go) gives every document 3–5 content tags and
// 1–3 role tags. Content tags are an open vocabulary, so over hundreds of
// documents they drift: "lead paint", "LBP" and "paint inspection" arrive from
// three documents meaning one thing. Seeding the prompt with the index's
// established tags stops most of that as it happens; this reports what got
// through, and `--merge` collapses it once a PERSON has decided that two terms
// mean the same thing. Nothing here decides that on its own — see tagmerge.go.
func runAuditTags(args []string) error {
	fs := flag.NewFlagSet("audit-tags", flag.ContinueOnError)
	openStore, homeOf := addStoreFlags(fs)
	minCount := fs.Int("min-count", 1, "only show tags carried by at least this many documents")
	asJSON := fs.Bool("json", false, "machine-readable output")
	var merges stringList
	fs.Var(&merges, "merge", "collapse tags: \"old,other=>new\" (repeatable)")
	docs := fs.Bool("documents", false, "list the documents under each tag")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := openCorpus(fs, openStore, homeOf)
	if err != nil {
		return err
	}
	defer st.Close()

	if len(merges) > 0 {
		return applyTagMerges(st, merges, *asJSON)
	}

	d, err := st.IndexDigestFor("", 0)
	if err != nil {
		return err
	}
	near, err := st.TagNeighbours()
	if err != nil {
		return err
	}
	byTag := map[string][]string{}
	if *docs {
		if byTag, err = documentsByTag(st); err != nil {
			return err
		}
	}

	type tagRow struct {
		Tag       string   `json:"tag"`
		Count     int      `json:"count"`
		Documents []string `json:"documents,omitempty"`
		NearDupes []string `json:"near_dupes,omitempty"`
	}
	rows := func(tags []raglit.TagCount, withNear bool) []tagRow {
		var out []tagRow
		for _, t := range tags {
			if t.Count < *minCount {
				continue
			}
			r := tagRow{Tag: t.Tag, Count: t.Count, Documents: byTag[t.Tag]}
			if withNear {
				r.NearDupes = near[t.Tag]
			}
			out = append(out, r)
		}
		return out
	}

	if *asJSON {
		out := struct {
			Documents int      `json:"documents"`
			Untagged  int      `json:"untagged"`
			Content   []tagRow `json:"content_tags"`
			Roles     []tagRow `json:"role_tags"`
			Invalid   []string `json:"invalid_role_tags,omitempty"`
		}{
			Documents: d.Documents, Untagged: d.Untagged,
			Content: rows(d.Content, true), Roles: rows(d.Roles, false),
			Invalid: invalidRoles(d.Roles),
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("Tag audit: %d document(s)", d.Documents)
	if d.Untagged > 0 {
		fmt.Printf(", %d untagged — `raglit identify --tags-only` fills those in", d.Untagged)
	}
	fmt.Println()

	fmt.Println("\nContent tags:")
	for _, r := range rows(d.Content, true) {
		fmt.Printf("  %4d  %s\n", r.Count, r.Tag)
		if len(r.NearDupes) > 0 {
			fmt.Printf("        ≈ %s\n", strings.Join(r.NearDupes, ", "))
		}
		for _, p := range r.Documents {
			fmt.Printf("          %s\n", p)
		}
	}

	fmt.Println("\nRole tags:")
	for _, r := range rows(d.Roles, false) {
		fmt.Printf("  %4d  %s\n", r.Count, r.Tag)
	}
	if bad := invalidRoles(d.Roles); len(bad) > 0 {
		fmt.Printf("\n⚠ role tags outside the vocabulary: %s\n", strings.Join(bad, ", "))
	}
	if len(near) > 0 {
		fmt.Printf("\n≈ marks tags sharing a word — a PROPOSAL, not a finding.\n" +
			"  Collapse one with: raglit audit-tags --merge \"old,other=>new\"\n")
	}
	return nil
}

// applyTagMerges runs the merges a person named, in the order they named them.
func applyTagMerges(st *raglit.Store, specs []string, asJSON bool) error {
	ctx := context.Background()
	var results []raglit.TagMergeResult
	for _, spec := range specs {
		m, err := raglit.ParseTagMerge(spec)
		if err != nil {
			return err
		}
		res, err := st.MergeTags(ctx, m)
		if err != nil {
			return fmt.Errorf("merge %s: %w", m, err)
		}
		results = append(results, res)
	}
	if asJSON {
		b, err := json.MarshalIndent(struct {
			Merges []raglit.TagMergeResult `json:"merges"`
		}{results}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	for _, r := range results {
		fmt.Printf("%s → %s: %d document(s)\n", strings.Join(r.Collapsed, ", "), r.To, r.Documents)
		if len(r.Missing) > 0 {
			fmt.Printf("  no document carried: %s\n", strings.Join(r.Missing, ", "))
		}
	}
	return nil
}

// documentsByTag inverts the index: every content tag to the paths carrying it.
func documentsByTag(st *raglit.Store) (map[string][]string, error) {
	docs, err := st.Documents()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range docs {
		for _, t := range d.GenContentTags {
			out[t] = append(out[t], d.Path)
		}
	}
	for t := range out {
		sort.Strings(out[t])
	}
	return out, nil
}

// invalidRoles names stored role tags that are not in the closed vocabulary —
// rows written before a term was retired, or by a path that skipped validation.
func invalidRoles(roles []raglit.TagCount) []string {
	var bad []string
	for _, r := range roles {
		if _, ok := raglit.NormalizeRole(r.Tag); !ok {
			bad = append(bad, r.Tag)
		}
	}
	sort.Strings(bad)
	return bad
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }
