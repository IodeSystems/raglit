package raglit

import (
	"strings"
)

// CorpusCoverage counts how many documents in an index (under an optional path
// prefix) have been captioned, tagged, typed, extracted, and attested.
// Branch-aware: a branch overlays its own rows over the parent's, the same way
// IndexDigestFor does.
type CorpusCoverage struct {
	Total     int `json:"total"`
	Captioned int `json:"captioned"`
	Tagged    int `json:"tagged"`
	Typed     int `json:"typed"`
	Extracted int `json:"extracted"`
	Attested  int `json:"attested"`
}

// coverageRow is the one row per document the coverage query reads. It carries
// only the columns needed for the five counts — no fragment counts, no engine
// breakdown, nothing the dashboard band does not render.
type coverageRow struct {
	path      string
	captioned bool
	tagged    bool
	typed     bool
	extracted bool
	attested  bool
}

func (s *Store) coverageRowsLocal(pathPrefix string) ([]coverageRow, error) {
	q := `SELECT d.path,
	        d.gen_name, d.gen_summary,
	        d.gen_content_tags, d.gen_role_tags,
	        d.gen_doc_type,
	        (SELECT EXISTS(SELECT 1 FROM doc_fields f WHERE f.doc_id = d.id)) AS extracted,
	        (SELECT EXISTS(SELECT 1 FROM attestations a WHERE a.doc = d.path)) AS attested
	   FROM documents d`
	var args []any
	if pathPrefix != "" {
		q += ` WHERE d.path LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(pathPrefix))
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []coverageRow
	for rows.Next() {
		var r coverageRow
		var name, summary, ct, rt, dt string
		var extracted, attested int64
		if err := rows.Scan(&r.path, &name, &summary, &ct, &rt, &dt, &extracted, &attested); err != nil {
			return nil, err
		}
		r.captioned = name != "" || summary != ""
		r.tagged = ct != "" || rt != ""
		r.typed = strings.TrimSpace(dt) != ""
		r.extracted = extracted != 0
		r.attested = attested != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// coverageRows overlays branch rows over the parent's, the same way digestRows
// does — a branch that reports only its own documents describes an index the
// reader is not searching.
func (s *Store) coverageRows(pathPrefix string) ([]coverageRow, error) {
	rows, err := s.coverageRowsLocal(pathPrefix)
	if err != nil || s.parent == nil {
		return rows, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, pp := range s.parentPrefixes(pathPrefix) {
		prows, err := s.parent.coverageRows(pp)
		if err != nil {
			return nil, err
		}
		for _, pr := range prows {
			bp, ok := s.fromParent(pr.path)
			if !ok || shadow[bp] || !s.admitParent(bp, pathPrefix) {
				continue
			}
			if s.worktree() {
				if seen[bp] {
					continue
				}
				seen[bp] = true
			}
			pr.path = bp
			rows = append(rows, pr)
		}
	}
	return rows, nil
}

// CoverageFor counts the five segments under a path prefix (empty for the
// whole index). Branch-aware via the same overlay as IndexDigestFor.
func (s *Store) CoverageFor(pathPrefix string) (CorpusCoverage, error) {
	rows, err := s.coverageRows(pathPrefix)
	if err != nil {
		return CorpusCoverage{}, err
	}
	c := CorpusCoverage{Total: len(rows)}
	for _, r := range rows {
		if r.captioned {
			c.Captioned++
		}
		if r.tagged {
			c.Tagged++
		}
		if r.typed {
			c.Typed++
		}
		if r.extracted {
			c.Extracted++
		}
		if r.attested {
			c.Attested++
		}
	}
	return c, nil
}
