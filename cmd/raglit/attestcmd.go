package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/attest"
)

// runAttest hands a region read to a person to rule on.
//
// `raglit regions` records what the model saw and `raglit region` puts one crop
// back on the screen, but neither of them can record an ANSWER. The tree was
// built so that a human could attest a quotation against the image it actually
// came from, and until now there was nowhere to put the verdict.
//
// The workbench is not raglit's. oidio grew one for audio and it is the same
// job — a machine read something, a person rules on it, and the account of how
// much was ruled on has to stay honest. So the review surface, the verdict
// vocabulary and the append-only log live in `attest`, and raglit supplies the
// two things only raglit knows: what the regions ARE, and how to re-render one.
//
// Nothing here writes to the regions sidecar. A verdict lands in
// <doc>.attest.jsonl, so re-reading a sheet cannot destroy what a person
// confirmed, and confirming cannot freeze the sheet against being re-read.

func runAttest(args []string) error {
	fs := flag.NewFlagSet("attest", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1", "host to bind")
	port := fs.String("port", "", "fixed port; default is one chosen by the OS and printed")
	writeOnly := fs.Bool("write-only", false, "write the reading and exit, without serving a review")
	cert := fs.String("tls-cert", "", "serve over TLS with this certificate (see --tls-key)")
	key := fs.String("tls-key", "", "the certificate's private key")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: raglit attest [--port N] FILE

Publishes a recorded region read as an attestable reading, and serves a review
over it.

Requires `+"`raglit regions --write`"+` to have run: the tree is what says which crop
each passage came from, and without it a reviewer is being shown a whole sheet
for text that was read off a rotated, zoomed corner of it.

Each region becomes a unit, keeping the descent as a parent chain — the sheet,
the drawing interior, the corner of it — so an overview and the leaves refining
it do not read as peers that happen to repeat one another. The digest raglit
already recorded per region is carried through, which is what lets the review
page say THIS IS the image the words came from, or refuse to.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("one FILE, please")
	}
	path := fs.Arg(0)

	// Two producers, chosen by what the asset IS. A sheet is reviewed against
	// its crops and needs a recorded region read first; a text document is
	// reviewed against its own bytes and needs nothing but the file.
	rd, note, err := readingFor(path)
	if err != nil {
		return err
	}
	out, err := attest.WriteReading(path, rd)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s)\n", out, note)
	if *writeOnly {
		return nil
	}

	// The service is rooted at the document's DIRECTORY, and the asset is
	// addressed relative to it. Rooting at the file itself would mean a review
	// of one sheet could never list a second.
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	svc := &attest.Service{Root: abs, Ident: attest.Guest{}, Ev: evidenceFor(path, abs)}

	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("raglit attest", version))
	if err := svc.Register(api, "/api"); err != nil {
		return err
	}
	router.Method(http.MethodGet, "/", svc.UI("/api", "/source"))
	router.Method(http.MethodGet, "/source", svc.AssetBytes())

	ln, url, err := attest.ListenOn(*listen, *port, *cert != "")
	if err != nil {
		return err
	}
	fmt.Printf("raglit attest → %s?asset=%s\n", url, filepath.Base(path))
	fmt.Printf("  verdicts: %s  (appended, never rewritten)\n", attest.LogPath(path))
	fmt.Printf("  authorized guest — every ruling must name the person making it\n")
	return attest.Serve(ln, router, *cert, *key)
}

// readingFromRegions converts a recorded region tree into attest's shape.
//
// The mapping is nearly free, and that is the point: raglit already records
// everything a verdict needs. The bbox, rotation and dpi ARE the locator; the
// digest it computed as a cycle detector IS the evidence; the flags are already
// conditions rather than scores.
//
// Producer handles are the raglit ids ("p1", "p1.0.2"), and attest rewrites
// them to content addresses on Seal. Parents are emitted before children
// because Flatten walks the tree that way, which is exactly what Seal requires.
func readingFromRegions(path string, doc *raglit.RegionDoc) (*attest.Reading, error) {
	rd := &attest.Reading{
		Asset: attest.Asset{
			ID:   filepath.Base(path),
			Name: doc.Doc,
			Kind: assetKindFor(path),
		},
		Producer: "raglit/regions",
		Read:     time.Now().Format(time.RFC3339),
	}
	for _, p := range doc.Pages {
		if p.Root == nil {
			continue
		}
		for _, n := range p.Root.Flatten() {
			dpi := n.DPI
			if dpi == 0 {
				dpi = p.DPI
			}
			u := attest.Unit{
				ID:     n.ID,
				Parent: parentOf(n.ID),
				Locator: attest.Locator{Area: &attest.Area{
					Page:     p.Page,
					BBox:     attest.Rect{X: n.BBox.X, Y: n.BBox.Y, W: n.BBox.W, H: n.BBox.H},
					Rotation: n.Rotation,
					DPI:      dpi,
				}},
				Text:     n.Text,
				Label:    n.Kind,
				Flags:    n.Flags,
				Evidence: n.SHA256,
			}
			// Extra carries what only raglit can say and attest must not
			// interpret. Deliberately outside the content address: adding a
			// diagnostic must never orphan a verdict.
			//
			// tokens_per_sq_in is the number that condemns a transcription on its
			// own — the survey came in at four against a letter page's thirty-nine
			// — and a reviewer looking at a suspiciously fluent paragraph deserves
			// to see it.
			u.Extra = extraJSON(map[string]any{
				"tokens_per_sq_in": n.TokensPerSqIn,
				"downscales":       n.Downscales,
				"depth":            n.Depth,
				"raglit_id":        n.ID,
			})
			rd.Units = append(rd.Units, u)
		}
	}
	if len(rd.Units) == 0 {
		return nil, fmt.Errorf("%s records no regions", raglit.RegionsPath(path))
	}
	return rd, nil
}

// parentOf derives the producer's parent handle from a raglit region id: the
// path minus its last component, and empty for a page root. The ids are
// path-shaped precisely so ancestry needs no lookup.
func parentOf(id string) string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '.' {
			return id[:i]
		}
	}
	return ""
}

func assetKindFor(path string) string {
	if isPDF(path) {
		return attest.KindPDF
	}
	return attest.KindImage
}

// regionEvidence re-renders the crop a region's text was read from.
//
// This is the whole reason raglit is in this loop at all: attest cannot
// rasterize a page and deliberately does not link a renderer. raglit can, and
// already does it for `raglit region`.
// root is the service root the asset id is relative to. Resolved here rather
// than trusting the process's cwd: the reading records a base name, and a
// review served from one directory must not resolve it against another.
type regionEvidence struct{ root string }

func (e regionEvidence) Render(_ context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	page, reg, err := regionFor(e.root, a, u)
	if err != nil {
		return attest.Artifact{}, err
	}
	b, err := raglit.RerenderRegion(page, reg)
	if err != nil {
		return attest.Artifact{}, err
	}
	// raglit's digest is sha256 of exactly these PNG bytes, so this either
	// matches what the read recorded or the rasterizer has changed underneath
	// it. Reported, never smoothed over.
	return attest.Artifact{MIME: "image/png", Body: b, Digest: attest.SHA256Hex(b)}, nil
}

// AsSeen replays the context downscales: what the model was actually given,
// where an oversized crop had to be shrunk mid-call to fit.
//
// A diagnostic, and attest labels it as one. It answers "could this have been
// read at all", which is a different question from "does the document say
// this", and the crop is the one a verdict rests on.
func (e regionEvidence) AsSeen(_ context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	page, reg, err := regionFor(e.root, a, u)
	if err != nil {
		return attest.Artifact{}, err
	}
	b, err := raglit.RerenderRegionAsSeen(page, reg)
	if err != nil {
		return attest.Artifact{}, err
	}
	return attest.Artifact{MIME: "image/png", Body: b, Digest: attest.SHA256Hex(b)}, nil
}

// regionFor resolves an attest unit back to the raglit region it came from, and
// rasterizes its page.
//
// The join is by RECORDED GEOMETRY, not by id: attest content-addresses its
// units, so the raglit path id does not survive into the unit's own id. The
// bbox, rotation, dpi and page identify the region unambiguously within a
// recorded read, which is the same tuple the digest is taken over.
func regionFor(root string, a attest.Asset, u attest.Unit) (image.Image, *raglit.Region, error) {
	ar := u.Locator.Area
	if ar == nil {
		return nil, nil, fmt.Errorf("raglit: unit %s is not a region of a page", u.ID)
	}
	path := filepath.Join(root, a.ID)
	doc, ok, err := raglit.ReadRegionDoc(path)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("raglit: no region read recorded for %s", path)
	}
	rp, ok := doc.PageRead(ar.Page)
	if !ok || rp.Root == nil {
		return nil, nil, fmt.Errorf("raglit: %s has no recorded read of page %d", path, ar.Page)
	}
	var found *raglit.Region
	for _, n := range rp.Root.Flatten() {
		if n.Rotation == ar.Rotation && sameRect(n.BBox, ar.BBox) {
			found = n
			break
		}
	}
	if found == nil {
		return nil, nil, fmt.Errorf("raglit: no recorded region at that geometry on page %d — "+
			"the sheet has been re-read since this unit was written", ar.Page)
	}
	dpi := ar.DPI
	if dpi == 0 {
		dpi = rp.DPI
	}
	page, err := renderPage(path, ar.Page, dpi)
	if err != nil {
		return nil, nil, err
	}
	// Checked before the crop is taken. A page that comes out a different size
	// at the same nominal dpi means a different renderer or a re-exported
	// document, and saying THAT is far more useful than an unexplained digest
	// mismatch two steps downstream.
	if b := page.Bounds(); rp.PxW > 0 && (b.Dx() != rp.PxW || b.Dy() != rp.PxH) {
		return nil, nil, fmt.Errorf("raglit: page %d rasterized to %dx%d, but the read recorded %dx%d "+
			"at the same dpi — this renderer does not reproduce that one",
			ar.Page, b.Dx(), b.Dy(), rp.PxW, rp.PxH)
	}
	return page, found, nil
}

// sameRect compares normalized coordinates with a tolerance, because the
// numbers make a round trip through JSON on the way here.
func sameRect(a raglit.Rect, b attest.Rect) bool {
	const eps = 1e-9
	return near(a.X, b.X, eps) && near(a.Y, b.Y, eps) && near(a.W, b.W, eps) && near(a.H, b.H, eps)
}

func near(a, b, eps float64) bool {
	d := a - b
	return d < eps && d > -eps
}

// extraJSON marshals producer diagnostics, dropping them rather than failing
// the reading: a unit with no diagnostics is worse than none only slightly, and
// a review that will not start because a number would not serialize is worse
// than both.
func extraJSON(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// readingFor picks the producer for an asset and builds its reading.
//
// The split is on the asset, not on what happens to be recorded beside it. A
// PDF with no region read is an error telling you to run `raglit regions`, NOT a
// fallback to reading its transcription sidecar as text: for a scanned page the
// crop is the evidence, and quietly reviewing the sidecar instead would put a
// person in front of characters when the open question is whether the pixels say
// them.
func readingFor(path string) (*attest.Reading, string, error) {
	if isTextAsset(path) {
		rd, err := readingFromText(path)
		if err != nil {
			return nil, "", err
		}
		return rd, fmt.Sprintf("%d paragraphs", len(rd.Units)), nil
	}
	doc, ok, err := raglit.ReadRegionDoc(path)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("no region read recorded for %s — run `raglit regions --write --page N %s` first",
			path, path)
	}
	rd, err := readingFromRegions(path, doc)
	if err != nil {
		return nil, "", err
	}
	return rd, fmt.Sprintf("%d regions across %d page(s)", len(rd.Units), len(doc.Pages)), nil
}

// evidenceFor supplies the rendering for whichever producer read the asset.
func evidenceFor(path, root string) attest.Evidence {
	switch {
	case isTextAsset(path):
		return textEvidence{root: root}
	case isAudioAsset(path):
		// May be nil, and that is a legitimate mount: without ffmpeg the API
		// still lists the reading and records verdicts, and the evidence
		// endpoint says it cannot render rather than serving a substitute.
		return audioEvidenceFor(root)
	}
	return &regionEvidence{root: root}
}
