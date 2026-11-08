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
	// tag in gen_content_tags or gen_role_tags — "paint" does not match
	// "lead paint". A tag is compared the way it is stored: lowercased, its
	// whitespace collapsed (normalizeContentTag). An entry that normalizes to
	// nothing is ignored.
	Tags []string
	// Fields keeps documents whose extracted fields (doc_fields.fields) have each
	// key equal to its value. Only a JSON STRING value matches: a number or a
	// boolean in the record does not equal its spelling here. A document with
	// no extraction matches no field constraint.
	Fields map[string]string
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
		// comma dropped, so a row written by hand compares the way splitTagList
		// would read it.
		preds = append(preds, `(instr(','||lower(replace(d.gen_content_tags, ', ', ','))||',', ?) > 0
		  OR instr(','||lower(replace(d.gen_role_tags, ', ', ','))||',', ?) > 0)`)
		needle := "," + t + ","
		args = append(args, needle, needle)
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
