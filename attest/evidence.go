package attest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Putting the right artifact in front of the reviewer.
//
// attest cannot decode audio or rasterize a page and must not learn how: raglit
// deliberately does not link a native PDF renderer, and oidio shells out to
// ffmpeg. Both already know how to produce the exact bytes their claims were
// read from. So the producer supplies the rendering and attest supplies the
// discipline about which rendering is which.
//
// Three of them, because there are three genuinely different questions and
// collapsing them is how a review becomes decorative:
//
//   - The CROP — what the claim was read from. The attestation image, the one
//     the verdict is about. A person handed the whole page instead is checking a
//     different image than the one that produced the text, and on a survey sheet
//     that is exactly where the legal description vanished.
//   - AS SEEN — what the model actually got, where that differs. raglit
//     re-renders an oversized page smaller mid-call, so the crop is SHARPER than
//     what was read. A diagnostic: it answers "could this have been read at
//     all", which is not the same question as "does the document say this".
//   - HUMANE — a rendering optimised for a person rather than fidelity. oidio
//     plays level-corrected audio because a passage nobody can hear is one they
//     will guess at, and a guess is worse for ground truth than a gap. It is
//     legitimate and it is NOT the artifact; the UI has to say so, or the whole
//     exact-bytes discipline is decoration.

// Artifact is one rendering of one unit.
type Artifact struct {
	MIME string
	Body []byte

	// Digest is the producer's digest of what it just rendered, computed the
	// same way it computed Unit.Evidence when it read the unit.
	//
	// Computed by the producer rather than by attest over Body, because the
	// canonical form is not always the served form: an audio window is digested
	// as decoded samples, since re-encoding is not byte-reproducible, but what
	// gets served to a browser is a container with a header. The producer is the
	// only party that knows the difference.
	//
	// This does not defend against a lying producer, and is not meant to. It
	// catches the thing that actually happens: a renderer upgrade that silently
	// produces different pixels at the same nominal dpi, months after the
	// verdicts were recorded.
	Digest string
}

// Matches reports whether this rendering is the artifact the unit's claim was
// read from.
//
// Byte equality via the digest, not "same box, same rotation". A weaker check
// would pass an image cropped from a page rasterized by a different tool at the
// same nominal dpi, which is precisely the substitution a person attesting a
// quotation is being asked to rule out.
func (a Artifact) Matches(u Unit) bool {
	return u.Evidence != "" && a.Digest != "" && a.Digest == u.Evidence
}

// Evidence renders the crop: the artifact a claim was read from.
type Evidence interface {
	Render(ctx context.Context, asset Asset, u Unit) (Artifact, error)
}

// AsSeenEvidence is the optional diagnostic rendering — what the model got,
// where a producer degraded the crop before reading it.
type AsSeenEvidence interface {
	AsSeen(ctx context.Context, asset Asset, u Unit) (Artifact, error)
}

// HumaneEvidence is the optional reviewer-facing rendering, when fidelity and
// legibility genuinely conflict.
//
// A producer that implements this is asserting that the plain crop is hard for
// a person to use, not that it is wrong. Everything served through it is
// labelled, and it can never satisfy a Matches check — by construction, since
// it is not the artifact.
type HumaneEvidence interface {
	Humane(ctx context.Context, asset Asset, u Unit) (Artifact, error)
}

// Rendering names which of the three a caller wants.
type Rendering string

const (
	// AsCrop is the attestation image and the default. Anything recording a
	// verdict should be showing this.
	AsCrop Rendering = "crop"
	// AsModelSaw is the diagnostic.
	AsModelSaw Rendering = "seen"
	// AsHumane is the legibility-first rendering. Never the basis of a verdict
	// on its own.
	AsHumane Rendering = "humane"
)

// Attestable reports whether a verdict recorded against this rendering means
// what a verdict is supposed to mean.
//
// Only the crop is. This is a predicate rather than a prohibition: oidio's
// reviewers work almost entirely from level-corrected audio and the pass is
// still worth having. What must not happen is a record that cannot tell the
// difference afterwards.
func (r Rendering) Attestable() bool { return r == AsCrop }

// Render dispatches to whichever rendering the provider supports, and says
// plainly when it does not support one rather than quietly substituting the
// crop.
//
// Substitution is the failure mode worth spending an error on: a UI that asks
// for the humane rendering, silently receives the crop, and labels it "levelled"
// has told the reviewer something untrue about what they are looking at.
func Render(ctx context.Context, ev Evidence, asset Asset, u Unit, as Rendering) (Artifact, error) {
	switch as {
	case AsCrop, "":
		return ev.Render(ctx, asset, u)
	case AsModelSaw:
		p, ok := ev.(AsSeenEvidence)
		if !ok {
			return Artifact{}, fmt.Errorf("attest: this producer records nothing between the crop and the read, "+
				"so there is no separate as-seen image for unit %s", u.ID)
		}
		return p.AsSeen(ctx, asset, u)
	case AsHumane:
		p, ok := ev.(HumaneEvidence)
		if !ok {
			return Artifact{}, fmt.Errorf("attest: this producer serves no separate reviewer rendering; "+
				"the crop for unit %s is what there is", u.ID)
		}
		return p.Humane(ctx, asset, u)
	}
	return Artifact{}, fmt.Errorf("attest: unknown rendering %q", as)
}

// SHA256Hex is the digest scheme for producers whose canonical form IS the
// bytes they serve — raglit's cropped PNG. Producers whose canonical form
// differs, like a decoded audio window, digest that instead and are responsible
// for using the same function at read time and at render time.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// VerifyEvidence reports whether a rendering is the artifact a unit's text was
// read from, with an error that says what went wrong rather than just "false".
func VerifyEvidence(u Unit, a Artifact) error {
	if u.Evidence == "" {
		return fmt.Errorf("attest: unit %s recorded no evidence digest; "+
			"nothing can say whether this is the artifact it was read from", u.ID)
	}
	if a.Digest == "" {
		return fmt.Errorf("attest: this rendering of unit %s carries no digest", u.ID)
	}
	if a.Digest != u.Evidence {
		return fmt.Errorf("attest: unit %s re-rendered to %s, but its text was read from %s — "+
			"this is not the image that produced it", u.ID, short(a.Digest), short(u.Evidence))
	}
	return nil
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
