package attest

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// The review surface, as operations a host mounts rather than a server attest
// insists on running.
//
// Register is the whole interface. A host with its own router, its own auth and
// its own middleware calls it once and gets the review API inside its API —
// same OpenAPI document, same auth, one deployment. The standalone CLI is a
// thin caller of the same thing, so there is no second implementation to drift.
// House pattern: kgraph/daemon.go.
//
// Nothing here caches. State is resolved from the two files on every request,
// because verdicts are appended by whoever else has the asset open — another
// reviewer, a producer re-reading, a person with an editor — and a cache would
// serve one reviewer a transcript the other has already corrected.

// Service is a review surface over a directory of assets.
type Service struct {
	// Root bounds everything. Assets are addressed relative to it and a request
	// cannot reach outside it.
	Root string

	// Ident is the host's answer to who is calling and what they may do. See
	// identity.go — attest authenticates nobody.
	Ident Identity

	// Ev renders the artifact a claim was read from. Optional: without it the
	// API still serves readings and records verdicts, and the evidence endpoint
	// says plainly that this mount cannot show the artifact rather than showing
	// a substitute.
	Ev Evidence

	// Now is injectable so a test can assert on a timestamp.
	Now func() string
}

func (s *Service) now() string {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().Format(time.RFC3339)
}

// path resolves a root-relative asset reference to a filesystem path, and
// refuses everything that leaves the root.
//
// Two checks, because they catch different things: cleaning the reference kills
// `..`, and resolving the containing directory's symlinks kills a link planted
// inside the root that points outside it. This surface is meant to be publicly
// mounted, so the second one is not paranoia.
func (s *Service) path(asset string) (string, error) {
	if asset == "" {
		return "", huma.Error400BadRequest("asset is required — it is the root-relative path of the thing being reviewed")
	}
	if IsReading(asset) || IsLog(asset) {
		return "", huma.Error400BadRequest("that is attest's own sidecar, not an asset")
	}
	full := filepath.Join(s.Root, filepath.Clean("/"+asset))
	root, err := filepath.EvalSymlinks(s.Root)
	if err != nil {
		return "", huma.Error500InternalServerError("attest root is unreadable")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return "", huma.Error404NotFound("no such asset")
	}
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", huma.Error404NotFound("no such asset")
	}
	return full, nil
}

// authorize resolves the path and the permission together, because every
// operation needs both and splitting them is how one of them gets forgotten.
func (s *Service) authorize(ctx context.Context, asset string, p Permission) (string, error) {
	full, err := s.path(asset)
	if err != nil {
		return "", err
	}
	ok, err := s.Ident.Can(ctx, asset, p)
	if err != nil {
		return "", huma.Error500InternalServerError("permission check failed", err)
	}
	if !ok {
		// 404 rather than 403 for reading: on a public mount, telling an
		// unauthorized caller that an asset exists is itself a disclosure. For
		// the write permissions the caller can already see the asset, so saying
		// "you may not do this" costs nothing and is more useful.
		if p == PermRead {
			return "", huma.Error404NotFound("no such asset")
		}
		return "", huma.Error403Forbidden(fmt.Sprintf("not permitted to %s this asset", p))
	}
	return full, nil
}

// write records entries, stamping the pair of names on each. The author comes
// from the request and the principal from the host; see identity.go for why
// both.
func (s *Service) write(ctx context.Context, full, by string, p Permission, entries ...Entry) error {
	principal, author, err := signature(ctx, s.Ident, strings.TrimSpace(by))
	if err != nil {
		return huma.Error400BadRequest(err.Error())
	}
	at := s.now()
	for i := range entries {
		entries[i].By, entries[i].Auth, entries[i].At = author, principal, at
	}
	if err := Append(full, entries...); err != nil {
		return huma.Error400BadRequest(err.Error())
	}
	return nil
}

func (s *Service) state(full string) (*State, error) {
	st, err := Load(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, huma.Error404NotFound("no such asset")
		}
		return nil, huma.Error404NotFound(err.Error())
	}
	return st, nil
}

// AssetRef is one reviewable asset and how far its review has got.
type AssetRef struct {
	Asset    string `json:"asset" doc:"root-relative path, and the handle every other operation takes"`
	Kind     string `json:"kind,omitempty"`
	Producer string `json:"producer,omitempty"`
	Stats    Stats  `json:"stats"`
}

// Assets lists every asset under the root with a machine reading, skipping the
// ones this caller may not see.
//
// Resolved rather than merely listed: the completeness numbers are the reason
// anyone opens this list, and a list that showed names only would send every
// reviewer into every asset to find the unfinished one.
func (s *Service) Assets(ctx context.Context) ([]AssetRef, error) {
	var out []AssetRef
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !IsReading(p) {
			return nil
		}
		asset := strings.TrimSuffix(p, readingSuffix)
		rel, rerr := filepath.Rel(s.Root, asset)
		if rerr != nil {
			return nil
		}
		if ok, cerr := s.Ident.Can(ctx, rel, PermRead); cerr != nil || !ok {
			return nil
		}
		st, lerr := Load(asset)
		if lerr != nil {
			// A corpus with one unreadable sidecar is still a usable corpus, and
			// failing the whole listing would make it unusable. The asset is
			// omitted; opening it directly reports the real error.
			return nil
		}
		out = append(out, AssetRef{Asset: rel, Kind: st.Asset.Kind, Producer: st.Producer, Stats: st.Stats})
		return nil
	})
	return out, err
}

type assetQuery struct {
	Asset string `query:"asset" required:"true" doc:"root-relative path of the asset"`
}

// StateView is the wire shape of a resolved review.
//
// Spelled out rather than embedding State, because the provenance sentence is
// part of the contract: a consumer that reads the units and not the sentence is
// the consumer that publishes a half-reviewed transcript as a verified one.
type StateView struct {
	Asset    Asset    `json:"asset"`
	Producer string   `json:"producer,omitempty"`
	Units    []Status `json:"units"`
	Stats    Stats    `json:"stats"`
	Orphaned []Entry  `json:"orphaned,omitempty"`

	// Provenance is generated from the counts and must accompany anything
	// derived from this asset.
	Provenance string `json:"provenance"`
}

func view(st *State) StateView {
	return StateView{
		Asset: st.Asset, Producer: st.Producer, Units: st.Units,
		Stats: st.Stats, Orphaned: st.Orphaned, Provenance: st.Provenance(),
	}
}

// UnitDraft is a unit a PERSON is proposing: the payload of a resegment.
//
// Deliberately not a Unit, and the two missing fields are the whole reason this
// type exists.
//
//   - No ID. The id is a digest OF the claim, so accepting one from a caller
//     lets a unit be filed under an address that describes something else —
//     and every verdict keyed to that address then rules on the wrong thing.
//   - No Evidence. An evidence digest asserts "this is the artifact the text
//     was read from", and nothing read a unit a person just cut with a
//     keystroke. Accepting one would let a caller mint the "this is the
//     artifact it was read from" badge for words no machine ever produced,
//     which is precisely the false attestation the digest exists to prevent.
//
// A re-cut unit therefore has no artifact, and the reviewer is told so rather
// than shown a substitute.
type UnitDraft struct {
	Locator Locator `json:"locator"`
	Text    string  `json:"text,omitempty"`
	Label   string  `json:"label,omitempty"`
	Parent  string  `json:"parent,omitempty" doc:"id of the unit this nests under, for a hierarchical read"`
}

func (d UnitDraft) unit() Unit {
	return Unit{Locator: d.Locator, Text: d.Text, Label: d.Label, Parent: d.Parent}
}

// Register mounts the review operations on a host's huma API under prefix.
//
// Operation ids are namespaced, because they land in the host's OpenAPI
// document alongside its own.
func (s *Service) Register(api huma.API, prefix string) error {
	if s.Root == "" {
		return fmt.Errorf("attest: Service needs a Root")
	}
	if s.Ident == nil {
		return fmt.Errorf("attest: Service needs an Identity — attest authenticates nobody, " +
			"and defaulting to permit-all is not a decision it may make on a host's behalf")
	}
	prefix = strings.TrimSuffix(prefix, "/")

	huma.Register(api, huma.Operation{
		OperationID: "attestListAssets", Method: http.MethodGet, Path: prefix + "/assets",
		Summary: "Assets with a machine reading, and how far each review has got.",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Assets []AssetRef `json:"assets"`
		}
	}, error) {
		refs, err := s.Assets(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("listing assets", err)
		}
		out := &struct {
			Body struct {
				Assets []AssetRef `json:"assets"`
			}
		}{}
		out.Body.Assets = refs
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "attestGetState", Method: http.MethodGet, Path: prefix + "/state",
		Summary: "One asset's units, the verdicts in force, and the provenance sentence.",
		Description: "Provenance is generated from the data and is what any consumer must print beside " +
			"anything derived from this asset. A partly-reviewed transcript presented as a verified one " +
			"launders an unchecked machine guess into the record.",
	}, func(ctx context.Context, in *assetQuery) (*struct{ Body StateView }, error) {
		full, err := s.authorize(ctx, in.Asset, PermRead)
		if err != nil {
			return nil, err
		}
		st, err := s.state(full)
		if err != nil {
			return nil, err
		}
		return &struct{ Body StateView }{Body: view(st)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "attestVerdict", Method: http.MethodPost, Path: prefix + "/verdict",
		Summary: "Rule on one claim.",
		Description: "`by` is the person doing the review and is required. It is deliberately not taken " +
			"from the session: whoever holds the link may not be the account holder — an attorney hands " +
			"a paralegal the link and the paralegal does the work — and the record has to be able to say " +
			"so. The authenticated account is recorded separately and cannot be set by the caller.",
	}, func(ctx context.Context, in *struct {
		Body struct {
			Asset string `json:"asset" required:"true"`
			Unit  string `json:"unit" required:"true"`
			Kind  Kind   `json:"kind" required:"true" enum:"confirmed,corrected,affirmed,unclear,unsupported,retract"`
			Text  string `json:"text,omitempty" doc:"corrected text; omit if the words were not disputed"`
			Label string `json:"label,omitempty" doc:"corrected speaker or region kind"`
			Note  string `json:"note,omitempty"`
			By    string `json:"by" required:"true" doc:"the person making this ruling"`
		}
	}) (*struct {
		Body struct {
			Stats Stats `json:"stats"`
		}
	}, error) {
		b := in.Body
		if b.Kind == Resegment {
			return nil, huma.Error400BadRequest("re-cutting the units is a separate operation")
		}
		full, err := s.authorize(ctx, b.Asset, PermAttest)
		if err != nil {
			return nil, err
		}
		if err := s.write(ctx, full, b.By, PermAttest, Entry{
			Kind: b.Kind, Unit: b.Unit, Text: b.Text, Label: b.Label, Note: b.Note,
		}); err != nil {
			return nil, err
		}
		return s.statsOut(full)
	})

	huma.Register(api, huma.Operation{
		OperationID: "attestSweep", Method: http.MethodPost, Path: prefix + "/sweep",
		Summary: "\"I went through this, and here are the terms I accept the rest under.\"",
		Description: "Marks every so-far-unruled unit AFFIRMED, never confirmed. This is the ORDINARY " +
			"end of a pass: a reviewer goes through the asset, edits what needs it, and accepts what " +
			"does not. `statement` is their own words and is quoted verbatim in the provenance, never " +
			"paraphrased — a qualified attestation with a materiality standard in it is not the same " +
			"claim as \"the rest is right\". Units already ruled on individually are left alone.",
	}, func(ctx context.Context, in *struct {
		Body struct {
			Asset     string `json:"asset" required:"true"`
			By        string `json:"by" required:"true"`
			Statement string `json:"statement,omitempty" doc:"the terms you accept the remainder under, in your own words; quoted verbatim in the provenance"`
			Undo      bool   `json:"undo,omitempty" doc:"withdraw the sweep, leaving individual rulings alone"`
		}
	}) (*struct {
		Body struct {
			Stats Stats `json:"stats"`
		}
	}, error) {
		full, err := s.authorize(ctx, in.Body.Asset, PermAttest)
		if err != nil {
			return nil, err
		}
		kind := Affirmed
		if in.Body.Undo {
			kind = Retract
		}
		if err := s.write(ctx, full, in.Body.By, PermAttest,
			Entry{Kind: kind, Blanket: true, Statement: in.Body.Statement}); err != nil {
			return nil, err
		}
		return s.statsOut(full)
	})

	huma.Register(api, huma.Operation{
		OperationID: "attestResegment", Method: http.MethodPost, Path: prefix + "/resegment",
		Summary: "The machine cut in the wrong place; replace the units.",
		Description: "The common repair in both media, and why this is an editor rather than a survey: a " +
			"diarization boundary falling mid-sentence leaves one speaker's last word attached to the " +
			"next speaker's turn, and no amount of relabelling expresses the fix. Replacements are added " +
			"UNRULED — cutting in the right place is not the same as deciding who spoke.",
	}, func(ctx context.Context, in *struct {
		Body struct {
			Asset      string      `json:"asset" required:"true"`
			Supersedes []string    `json:"supersedes,omitempty" doc:"unit ids these replace"`
			Units      []UnitDraft `json:"units,omitempty" doc:"the replacements; ids are assigned from content, never accepted"`
			By         string      `json:"by" required:"true"`
		}
	}) (*struct {
		Body struct {
			Stats Stats `json:"stats"`
		}
	}, error) {
		full, err := s.authorize(ctx, in.Body.Asset, PermResegment)
		if err != nil {
			return nil, err
		}
		units := make([]Unit, 0, len(in.Body.Units))
		for _, d := range in.Body.Units {
			units = append(units, d.unit())
		}
		if err := s.write(ctx, full, in.Body.By, PermResegment, Entry{
			Kind: Resegment, Supersedes: in.Body.Supersedes, Units: units,
		}); err != nil {
			return nil, err
		}
		return s.statsOut(full)
	})

	huma.Register(api, huma.Operation{
		OperationID: "attestEvidence", Method: http.MethodGet, Path: prefix + "/evidence",
		Summary: "The artifact a claim was read from.",
		Description: "`crop` is the attestation image — the bytes the claim came from, and the only " +
			"rendering a verdict properly rests on. `seen` is what the model actually got where a " +
			"producer degraded the crop before reading it. `humane` is optimised for a person rather " +
			"than fidelity. The response says which it gave you and whether it matches the recorded " +
			"digest; a UI that does not surface that is showing a reviewer an unlabelled substitute.",
	}, func(ctx context.Context, in *struct {
		Asset string    `query:"asset" required:"true" doc:"root-relative path of the asset"`
		Unit  string    `query:"unit" required:"true"`
		As    Rendering `query:"as" enum:"crop,seen,humane" default:"crop"`
	}) (*struct {
		ContentType string `header:"Content-Type"`
		Digest      string `header:"X-Attest-Digest"`
		Rendering   string `header:"X-Attest-Rendering"`
		// Verified says whether these bytes are the artifact the claim was read
		// from. A header rather than an error, because an unverifiable rendering
		// is still worth looking at — it just must not be mistaken for the
		// artifact.
		Verified string `header:"X-Attest-Verified"`
		Body     []byte
	}, error) {
		full, err := s.authorize(ctx, in.Asset, PermRead)
		if err != nil {
			return nil, err
		}
		if s.Ev == nil {
			return nil, huma.Error501NotImplemented(
				"this mount serves readings and verdicts but cannot render the artifact they refer to")
		}
		st, err := s.state(full)
		if err != nil {
			return nil, err
		}
		var u *Unit
		for i := range st.Units {
			if st.Units[i].Unit.ID == in.Unit {
				u = &st.Units[i].Unit
				break
			}
		}
		if u == nil {
			return nil, huma.Error404NotFound("no such unit in this reading")
		}
		as := in.As
		if as == "" {
			as = AsCrop
		}
		art, err := Render(ctx, s.Ev, st.Asset, *u, as)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		verified := "no"
		if as.Attestable() && art.Matches(*u) {
			verified = "yes"
		}
		mime := art.MIME
		if mime == "" {
			mime = "application/octet-stream"
		}
		return &struct {
			ContentType string `header:"Content-Type"`
			Digest      string `header:"X-Attest-Digest"`
			Rendering   string `header:"X-Attest-Rendering"`
			Verified    string `header:"X-Attest-Verified"`
			Body        []byte
		}{ContentType: mime, Digest: art.Digest, Rendering: string(as), Verified: verified, Body: art.Body}, nil
	})

	return nil
}

func (s *Service) statsOut(full string) (*struct {
	Body struct {
		Stats Stats `json:"stats"`
	}
}, error) {
	st, err := s.state(full)
	if err != nil {
		return nil, err
	}
	out := &struct {
		Body struct {
			Stats Stats `json:"stats"`
		}
	}{}
	out.Body.Stats = st.Stats
	return out, nil
}
