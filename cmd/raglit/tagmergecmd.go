package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit tag-merge` — the model pass over the tag corpus and the review of its
// proposals.
//
// The lexical path (raglit audit-tags) pairs tags sharing a word; this runs the
// model pass over the whole vocabulary for pairs that name the same thing and
// share no word, records them as proposals, and lets a person accept (applies
// the merge through the existing mechanism) or reject (touches no tags) each.
func runTagMerge(args []string) error {
	fs := flag.NewFlagSet("tag-merge", flag.ContinueOnError)
	resolve := addTargetFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := resolve()
	if err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return tagMergeUsage()
	}
	switch rest[0] {
	case "propose":
		return tagMergePropose(t, *asJSON)
	case "list":
		status := ""
		if len(rest) > 1 {
			status = rest[1]
		}
		return tagMergeList(t, status, *asJSON)
	case "accept":
		if len(rest) < 2 {
			return fmt.Errorf("tag-merge accept needs a proposal id")
		}
		return tagMergeRule(t, rest[1], true, *asJSON)
	case "reject":
		if len(rest) < 2 {
			return fmt.Errorf("tag-merge reject needs a proposal id")
		}
		return tagMergeRule(t, rest[1], false, *asJSON)
	default:
		return tagMergeUsage()
	}
}

func tagMergeUsage() error {
	fmt.Fprintln(flag.CommandLine.Output(), strings.TrimSpace(`
raglit tag-merge SUBCOMMAND
  propose            run the model pass and record its proposals for review
  list [status]      list proposals (status: pending, accepted, rejected; default all)
  accept ID          accept a proposal: runs the tag merge, marks it accepted
  reject ID          reject a proposal: marks it rejected, touches no tags

Options: --json --home --index --daemon --project`))
	return nil
}

func tagMergePropose(t target, asJSON bool) error {
	var out struct {
		Proposals []raglit.TagMergeProposalRecord `json:"proposals"`
	}
	if err := t.post("/api/tags/propose", struct{}{}, &out); err != nil {
		return err
	}
	return printProposals(out.Proposals, asJSON)
}

func tagMergeList(t target, status string, asJSON bool) error {
	var out struct {
		Proposals []raglit.TagMergeProposalRecord `json:"proposals"`
	}
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if err := t.get("/api/tags/proposals", q, &out); err != nil {
		return err
	}
	return printProposals(out.Proposals, asJSON)
}

func tagMergeRule(t target, id string, accept bool, asJSON bool) error {
	var r int64
	if _, err := fmt.Sscanf(id, "%d", &r); err != nil {
		return fmt.Errorf("proposal id %q is not a number", id)
	}
	path := "/api/tags/proposals/accept"
	if !accept {
		path = "/api/tags/proposals/reject"
	}
	var out struct {
		Proposal raglit.TagMergeProposalRecord `json:"proposal"`
	}
	if err := t.post(path, map[string]any{"id": r}, &out); err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(out.Proposal, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	verb := "accepted"
	if !accept {
		verb = "rejected"
	}
	fmt.Printf("Proposal %d (%s → %s): %s\n", out.Proposal.ID, out.Proposal.A, out.Proposal.B, verb)
	return nil
}

func printProposals(ps []raglit.TagMergeProposalRecord, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(struct {
			Proposals []raglit.TagMergeProposalRecord `json:"proposals"`
		}{ps}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if len(ps) == 0 {
		fmt.Println("No tag-merge proposals.")
		return nil
	}
	for _, p := range ps {
		fmt.Printf("#%d  %s → %s  [%s]\n", p.ID, p.A, p.B, p.Status)
		if p.Rationale != "" {
			fmt.Printf("      %s\n", p.Rationale)
		}
	}
	return nil
}
