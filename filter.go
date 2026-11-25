package raglit

import (
	"fmt"
	"sort"
	"strings"
)

// SearchFilter scopes a search to the documents that qualify, BEFORE anything is
// ranked. The constraints go into each layer's WHERE, next to the path scope, so
// a limit counts only qualifying fragments: a filtered query with limit 1 returns
// the best QUALIFYING fragment, not the best fragment of the corpus that the
// filter then throws away. Filtering after ranking would return fewer answers
// than were asked for, or none, and that reads as a thinner corpus.
//
// Every constraint must hold (AND). The zero value constrains nothing.
type SearchFilter struct {
	// PathPrefix keeps documents whose path starts with it (see pathPredicate).
	PathPrefix string
	// Tags keeps documents that carry EVERY one of these tags, each as a whole
	// tag in the caller's tags (Store.SetTags), gen_content_tags or
	// gen_role_tags — "paint" does not match "lead paint". A tag is compared the way writeIdentity stores it:
	// lowercased, its whitespace collapsed (normalizeContentTag). An entry that
	// normalizes to nothing is ignored.
	Tags []string
	// Fields keeps documents whose extracted fields (doc_fields.fields) have each
	// key equal to its value. Only a JSON STRING value matches: a number or a
	// boolean in the record does not equal its spelling here. A document with
	// no extraction matches no field constraint.
	Fields map[string]string
	// Kind keeps documents whose gen_kind equals it exactly. Empty matches all.
	Kind string
	// DocType keeps documents whose gen_doc_type equals it exactly. Empty matches all.
	DocType string
	// Origin keeps documents that have at least one fragment whose origin equals
	// it exactly. Empty matches all. Origin is a fragment property (which model
	// wrote the text), not a document column, so the predicate is an EXISTS.
	Origin string
	// RoleTag keeps documents that carry it as a whole tag in gen_role_tags.
	// Compared the way Tags is: lowercased, whitespace collapsed, comma-joined.
	// Empty matches all.
	RoleTag string
	// ContentTag keeps documents that carry it as a whole tag in gen_content_tags.
	// Compared the way Tags is. Empty matches all.
	ContentTag string
}

// filterPredicate returns a bare SQL predicate over the documents alias d and
// its args, or ("", nil) when the filter constrains nothing. The predicate is a
// conjunction, so the caller can AND it into a WHERE as one term.
func filterPredicate(f SearchFilter) (string, []any, error) {
	var preds []string
	var args []any
	if p, a := pathPredicate(f.PathPrefix); p != "" {
		preds = append(preds, p)
		args = append(args, a...)
	}
	for _, t := range f.Tags {
		t = normalizeContentTag(t)
		if t == "" {
			continue
		}
		// A comma-joined column wrapped in commas holds ",tag," exactly when tag is
		// one of its whole entries. The column is lowercased and a space after a
		// comma dropped, so a row written before writeIdentity normalized, or by
		// hand, compares the way splitTagList would read it.
		preds = append(preds, `(instr(','||lower(replace(d.tags, ', ', ','))||',', ?) > 0
		  OR instr(','||lower(replace(d.gen_content_tags, ', ', ','))||',', ?) > 0
		  OR instr(','||lower(replace(d.gen_role_tags, ', ', ','))||',', ?) > 0)`)
		needle := "," + t + ","
		args = append(args, needle, needle, needle)
	}
	// Single-valued identity columns: exact comparison. The values come from the
	// facet counts themselves (the same columns the count reads), so a chip for a
	// value the corpus holds re-queries the exact set that value was counted over.
	if f.Kind != "" {
		preds = append(preds, "d.gen_kind = ?")
		args = append(args, f.Kind)
	}
	if f.DocType != "" {
		preds = append(preds, "d.gen_doc_type = ?")
		args = append(args, f.DocType)
	}
	// Origin is a fragment property, not a document column: keep documents that
	// have at least one fragment of that origin. The fragment need not match the
	// query — the document qualifies, and the FTS match still scopes the hits.
	if f.Origin != "" {
		preds = append(preds, `EXISTS (SELECT 1 FROM fragments fr WHERE fr.doc_id = d.id AND fr.origin = ?)`)
		args = append(args, f.Origin)
	}
	// Role and content tags: whole-tag in the column that carries them only, the
	// way the facet CTE reads them (lowercased, space after a comma dropped).
	// Unlike Tags, which spans all three columns, a role chip must not match a
	// content tag of the same spelling and vice versa.
	if f.RoleTag != "" {
		needle := "," + normalizeContentTag(f.RoleTag) + ","
		preds = append(preds, `instr(','||lower(replace(d.gen_role_tags, ', ', ','))||',', ?) > 0`)
		args = append(args, needle)
	}
	if f.ContentTag != "" {
		needle := "," + normalizeContentTag(f.ContentTag) + ","
		preds = append(preds, `instr(','||lower(replace(d.gen_content_tags, ', ', ','))||',', ?) > 0`)
		args = append(args, needle)
	}
	// Sorted, so the same filter always produces the same SQL text.
	keys := make([]string, 0, len(f.Fields))
	for k := range f.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// The key is quoted in the JSON path so a dot or a space in it names one
		// key rather than a nested one. SQLite's path syntax has no escape for a
		// double quote inside a quoted key.
		if k == "" || strings.Contains(k, `"`) {
			return "", nil, fmt.Errorf("raglit: field filter key %q: empty or contains a double quote", k)
		}
		// json_valid guards json_extract, which fails the whole query on one
		// malformed record rather than not matching it. json_type keeps an object
		// or array, which json_extract returns as JSON text, from equalling its
		// own serialization.
		path := `$."` + k + `"`
		preds = append(preds, `EXISTS (SELECT 1 FROM doc_fields df WHERE df.doc_id = d.id
		  AND CASE WHEN json_valid(df.fields) THEN
		        CASE WHEN json_type(df.fields, ?) = 'text' THEN json_extract(df.fields, ?) END
		      END = ?)`)
		args = append(args, path, path, f.Fields[k])
	}
	if len(preds) == 0 {
		return "", nil, nil
	}
	return strings.Join(preds, " AND "), args, nil
}
