package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/agentkit/llm"
)

// The model pass over the tag corpus.
//
// TagNeighbours (tagmerge.go) pairs tags that SHARE a significant word, which
// is a LEXICAL signal and nothing more. It cannot see that "escrow closing" and
// "settlement" name the same legal step, because they share no word at all — and
// that is exactly the drift a corpus owner cares about, since it is the kind
// spelling-seeding can never catch.
//
// So this asks the identity model — the one already configured to read this
// corpus's documents — for the pairs it reads as one thing. The model is the
// only thing with the concept, not the string. The proposal it returns is a
// PROPOSAL: it carries the model's rationale, and a person accepts or rejects
// it. Nothing here writes; a merge is applied through MergeTags, the same
// mechanism a person-named collapse uses.
//
// The pass is OPTIONAL. With no identity model configured it is skipped and the
// lexical path remains the sole mechanism for proposing merges — a corpus with
// no model still gets TagNeighbours, and a model that is down never changes
// what the lexical path reports.

// TagMergeProposal is one candidate a person is being asked to rule on: two
// content tags the pass reads as the same thing, and why.
type TagMergeProposal struct {
	// A and B are the two tags, in the order the pass reported them.
	A  string `json:"a"`
	B  string `json:"b"`
	// Rationale is the model's reason the two mean the same thing — what the
	// person is being asked to trust.
	Rationale string `json:"rationale"`
	// Source names what proposed it: "model" for this pass.
	Source string `json:"source"`
}

// tagPairKey is the order-insensitive identity of an unordered tag pair: the
// smaller tag, then the larger. It is what lets the model pass say "the lexical
// path already surfaced this pair, so I will not propose it a second time".
func tagPairKey(a, b string) string {
	x, y := a, b
	if x > y {
		x, y = y, x
	}
	return x + "\x00" + y
}

// ProposeTagMerges is the model pass over an index's content tags: it asks the
// identity model for pairs it reads as one thing beyond what lexical overlap
// already surfaces.
//
// It runs the lexical path first — TagNeighbours — so it knows which pairs a
// person is ALREADY being shown. A model proposal for a pair the lexical path
// has surfaced is dropped, not doubled: two rows for one pair is a proposal
// list nobody trusts, and the model's rationale adds nothing to a pair the
// spelling already gave away.
//
// With no identity model configured it returns no proposals and no error: the
// pass is skipped, and the lexical path (TagNeighbours) remains the sole
// mechanism. An empty corpus is the same — there are no pairs to propose.
func (s *Store) ProposeTagMerges(ctx context.Context) ([]TagMergeProposal, error) {
	id := s.identity()
	if id == nil {
		return nil, nil
	}
	d, err := s.IndexDigestFor("", 0)
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(d.Content))
	for _, tc := range d.Content {
		tags = append(tags, tc.Tag)
	}
	if len(tags) < 2 {
		return nil, nil
	}
	// The pairs the lexical path already surfaced — this pass must not re-offer
	// them. TagNeighbours is the lexical half; reuse it rather than re-deriving
	// the word-sharing, so the two halves cannot drift apart.
	taken := map[string]bool{}
	if near, err := s.TagNeighbours(); err == nil {
		for a, bset := range near {
			for _, b := range bset {
				taken[tagPairKey(a, b)] = true
			}
		}
	}
	cands, err := id.ProposeTagMerges(ctx, tags, s.IndexHint())
	if err != nil {
		return nil, err
	}
	var out []TagMergeProposal
	for _, p := range cands {
		if taken[tagPairKey(p.A, p.B)] {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// proposeTagMergesToolDef is the schema the model's answer is validated against.
func proposeTagMergesToolDef() llm.ToolDef {
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = "emit_tag_merges"
	td.Function.Description = "Emit the content-tag pairs that mean the same thing."
	td.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pairs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"a":       map[string]any{"type": "string"},
						"b":       map[string]any{"type": "string"},
						"rationale": map[string]any{"type": "string"},
					},
					"required": []string{"a", "b", "rationale"},
				},
			},
		},
		"required": []string{"pairs"},
	}
	return td
}

const proposeTagMergesJSONShape = `{"pairs":[{"a":"...","b":"...","rationale":"..."}]}`

// ProposeTagMerges asks the identity model, over an index's content tags, for
// the pairs it reads as naming the same thing.
//
// It is handed the WHOLE tag vocabulary — the model sees what the index has
// already settled on, which is what lets it say "escrow closing and settlement
// are the same step" rather than inventing a pair from nothing. tags is the
// content vocabulary (IndexDigest.Content), not roles: roles are a closed
// vocabulary and cannot drift.
//
// The answer is a LIST OF PROPOSALS, each carrying the rationale a person will
// rule on. An empty answer is not an error: a corpus whose tags are already
// clean is the model saying "nothing to merge", which is a real answer.
func (id *Identifier) ProposeTagMerges(ctx context.Context, tags []string, hint string) ([]TagMergeProposal, error) {
	validator := newValidator(proposeTagMergesToolDef())

	var b strings.Builder
	b.WriteString(proposeTagMergesPrompt)
	if hb := HintBlock(hint); hb != "" {
		b.WriteString(hb)
	}
	b.WriteString("\n\nCONTENT TAGS IN THIS INDEX:\n")
	for _, t := range tags {
		b.WriteString("- " + t + "\n")
	}

	sub := &Identifier{
		Client: id.Client, Model: id.Model, MaxRetries: id.MaxRetries,
		validator: validator,
	}
	var pairs []TagMergeProposal
	err := sub.ask(ctx, b.String(), "emit_tag_merges", proposeTagMergesJSONShape, func(js string) error {
		var d struct {
			Pairs []struct {
				A         string `json:"a"`
				B         string `json:"b"`
				Rationale string `json:"rationale"`
			} `json:"pairs"`
		}
		if err := json.Unmarshal([]byte(js), &d); err != nil {
			return fmt.Errorf("unparseable: %v", err)
		}
		for _, p := range d.Pairs {
			a, b := normalizeContentTag(p.A), normalizeContentTag(p.B)
			// A pair is two DIFFERENT tags; a model that echoes one tag twice
			// proposes a merge of a tag into itself, which is nothing.
			if a == "" || b == "" || a == b {
				continue
			}
			pairs = append(pairs, TagMergeProposal{A: a, B: b, Rationale: strings.TrimSpace(p.Rationale), Source: "model"})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("propose tag merges: %w", err)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		return pairs[i].B < pairs[j].B
	})
	return pairs, nil
}

const proposeTagMergesPrompt = `You are cleaning up the content tags of a document index. The tags were
written independently, document by document, so the same idea arrives under more
than one name: "escrow closing" and "settlement" may be the same legal step,
"LBP" and "lead paint" the same subject.

Below is the index's full list of content tags. Find the pairs that name the SAME
thing — the same subject, step or entity — even when they share no word. Do NOT
propose a pair that already shares a word; spelling overlap is handled elsewhere.
Only propose what the meaning of the terms makes clear. When two terms are close
but genuinely different ("lead paint" and "lead paint disclosure" are different
facts), do not pair them.

For each pair, say in one line WHY they mean the same thing — that is what a
person will read to decide. If there are no such pairs, return an empty list.
Output ONLY the JSON object ` + proposeTagMergesJSONShape + `.`
