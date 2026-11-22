package raglit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Persisted tag-merge proposals: the model pass (ProposeTagMerges) produces
// candidates, a person reviews and rules. Accepting runs MergeTags — the same
// mechanism a person-named --merge uses — and marks the row accepted. Rejecting
// marks the row rejected and touches no tags.
//
// The table is additive (store.go); proposals survive a restart so a person can
// rule on them later.

// TagMergeProposalRecord is one proposal as stored, with its ruling.
type TagMergeProposalRecord struct {
	ID        int64  `json:"id"`
	A         string `json:"a"`
	B         string `json:"b"`
	Rationale string `json:"rationale"`
	Source    string `json:"source"`
	Status    string `json:"status"` // "pending", "accepted", "rejected"
	At        int64  `json:"at"`
	Result    string `json:"result,omitempty"` // TagMergeResult JSON, on accept
}

// RecordTagMergeProposals upserts proposals: a pending row is left as-is; an
// already-ruled row is not re-opened. Returns the ids in order.
func (s *Store) RecordTagMergeProposals(ctx context.Context, ps []TagMergeProposal) ([]int64, error) {
	if len(ps) == 0 {
		return nil, nil
	}
	now := time.Now().Unix()
	ids := make([]int64, 0, len(ps))
	for _, p := range ps {
		a, b := normalizeContentTag(p.A), normalizeContentTag(p.B)
		if a == "" || b == "" || a == b {
			continue
		}
		// Order-insensitive: store the smaller tag first.
		if a > b {
			a, b = b, a
		}
		var id int64
		err := s.db.QueryRowContext(ctx,
			`INSERT INTO tag_merge_proposals (a, b, rationale, source, status, at)
			 VALUES (?, ?, ?, ?, 'pending', ?)
			 ON CONFLICT(a, b) DO NOTHING
			 RETURNING id`, a, b, p.Rationale, p.Source, now).Scan(&id)
		if err == sql.ErrNoRows {
			// Already exists: keep the existing row, return its id.
			err = s.db.QueryRowContext(ctx,
				`SELECT id FROM tag_merge_proposals WHERE a = ? AND b = ?`, a, b).Scan(&id)
			if err != nil {
				return nil, fmt.Errorf("record proposal %q/%q: %w", a, b, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("record proposal %q/%q: %w", a, b, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ListTagMergeProposals returns proposals, optionally filtered by status
// (empty = all), oldest first.
func (s *Store) ListTagMergeProposals(ctx context.Context, status string) ([]TagMergeProposalRecord, error) {
	q := `SELECT id, a, b, rationale, source, status, at, result FROM tag_merge_proposals`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()
	var out []TagMergeProposalRecord
	for rows.Next() {
		var r TagMergeProposalRecord
		if err := rows.Scan(&r.ID, &r.A, &r.B, &r.Rationale, &r.Source, &r.Status, &r.At, &r.Result); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AcceptTagMergeProposal applies the merge through MergeTags and marks the
// row accepted. The pair (a, b) merges a into b: a is the tag being collapsed,
// b is the one it collapses into.
func (s *Store) AcceptTagMergeProposal(ctx context.Context, id int64) (TagMergeProposalRecord, error) {
	var r TagMergeProposalRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT id, a, b, rationale, source, status, at, result FROM tag_merge_proposals WHERE id = ?`, id).
		Scan(&r.ID, &r.A, &r.B, &r.Rationale, &r.Source, &r.Status, &r.At, &r.Result)
	if err == sql.ErrNoRows {
		return r, fmt.Errorf("proposal %d not found", id)
	}
	if err != nil {
		return r, err
	}
	if r.Status != "pending" {
		return r, fmt.Errorf("proposal %d is %s, not pending", id, r.Status)
	}
	res, err := s.MergeTags(ctx, TagMerge{From: []string{r.A}, To: r.B})
	if err != nil {
		return r, fmt.Errorf("accept proposal %d: %w", id, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return r, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tag_merge_proposals SET status = 'accepted', result = ? WHERE id = ?`, string(b), id); err != nil {
		return r, err
	}
	r.Status = "accepted"
	r.Result = string(b)
	return r, nil
}

// RejectTagMergeProposal marks the row rejected. It touches no tags: both
// remain as they are.
func (s *Store) RejectTagMergeProposal(ctx context.Context, id int64) (TagMergeProposalRecord, error) {
	var r TagMergeProposalRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT id, a, b, rationale, source, status, at, result FROM tag_merge_proposals WHERE id = ?`, id).
		Scan(&r.ID, &r.A, &r.B, &r.Rationale, &r.Source, &r.Status, &r.At, &r.Result)
	if err == sql.ErrNoRows {
		return r, fmt.Errorf("proposal %d not found", id)
	}
	if err != nil {
		return r, err
	}
	if r.Status != "pending" {
		return r, fmt.Errorf("proposal %d is %s, not pending", id, r.Status)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tag_merge_proposals SET status = 'rejected' WHERE id = ?`, id); err != nil {
		return r, err
	}
	r.Status = "rejected"
	return r, nil
}
