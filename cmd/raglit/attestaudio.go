package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/iodesystems/raglit/attest"
)

// Reviewing audio.
//
// Ported from oidio, which is where this was written and where it stops being.
// The division: oidio stays in the machine best-effort game — it transcribes and
// diarizes and emits its reading — and raglit does the verifying and correcting,
// for all three modalities, in one place with one store of rulings.
//
// The whole thing is OPTIONAL. attest is explicit that an Evidence is optional
// and that a mount without one "says plainly that this mount cannot show the
// artifact rather than showing a substitute" — so if ffmpeg is not on the box,
// audioEvidenceFor returns nil, the endpoint answers 501, and the UI prints
// "this mount cannot render the artifact". Readings still list and verdicts are
// still recorded; a reviewer simply cannot listen, and is told so rather than
// being handed silence.

const (
	pcmRate = 16000
	pcmBits = 16
)

// haveFFmpeg is probed once. A review page hits the evidence endpoint per unit,
// and shelling out to answer "does this binary exist" on every one of them turns
// a missing dependency into a per-request fork.
var haveFFmpeg = sync.OnceValue(func() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
})

// audioEvidenceFor returns the renderer, or nil when this box cannot decode.
//
// Nil rather than a renderer that errors per request: the difference is what a
// reviewer is told. A nil Evidence is a property of the MOUNT and attest reports
// it once, plainly; a renderer that fails on every call reads like a broken file
// and invites somebody to go looking for a corrupt recording that is fine.
func audioEvidenceFor(root string) attest.Evidence {
	if !haveFFmpeg() {
		return nil
	}
	return audioEvidence{root: root}
}

// audioEvidence renders the audio a claim was read from.
//
// root is the directory the reading's asset id resolves against, so a review
// served from one directory cannot be made to read a file in another.
type audioEvidence struct{ root string }

// Render returns the exact window, as a WAV around the canonical samples.
//
// The digest covers the SAMPLES, not this file. A WAV header is 44 bytes of
// framing that says nothing about what was said, and putting it in the digest
// would make the evidence depend on how the window happened to be packaged for
// a browser. So the payload and the digest describe different byte strings on
// purpose — which is exactly why attest takes the digest from the producer
// instead of computing it over what it was handed.
func (e audioEvidence) Render(_ context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	t, path, err := e.locate(a, u)
	if err != nil {
		return attest.Artifact{}, err
	}
	pcm, err := audioWindow(path, t.Start, t.End)
	if err != nil {
		return attest.Artifact{}, err
	}
	return attest.Artifact{MIME: "audio/wav", Body: wavContainer(pcm), Digest: attest.SHA256Hex(pcm)}, nil
}

// Humane returns the same window levelled for listening, and falls back to a
// PICTURE of it when levelling is not available.
//
// Both are legitimate and neither is the artifact; attest labels them so. The
// fallback earns its place because the alternative is nothing: a reviewer who
// cannot hear a passage still needs to answer "is there anything here", and a
// waveform answers that. Silence and speech look different even when a codec
// will not play in the browser, and `unclear` — looked, cannot tell — is a real
// verdict that wants evidence behind it.
func (e audioEvidence) Humane(_ context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	t, path, err := e.locate(a, u)
	if err != nil {
		return attest.Artifact{}, err
	}
	if out, err := levelled(path, t.Start, t.End); err == nil {
		// Digest of what was actually produced, which deliberately will NOT
		// match the recorded evidence. A humane rendering can never satisfy a
		// Matches check, by construction.
		return attest.Artifact{MIME: "audio/wav", Body: wavContainer(out), Digest: attest.SHA256Hex(out)}, nil
	}
	pcm, err := audioWindow(path, t.Start, t.End)
	if err != nil {
		return attest.Artifact{}, err
	}
	img := waveform(pcm)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return attest.Artifact{}, err
	}
	return attest.Artifact{MIME: "image/png", Body: b.Bytes(), Digest: attest.SHA256Hex(b.Bytes())}, nil
}

func (e audioEvidence) locate(a attest.Asset, u attest.Unit) (*attest.TimeSpan, string, error) {
	t := u.Locator.Time
	if t == nil {
		return nil, "", fmt.Errorf("raglit: unit %s is not a span of a recording", u.ID)
	}
	return t, filepath.Join(e.root, a.ID), nil
}

// audioWindow decodes one span of a recording to canonical PCM.
//
// -ss and -to come AFTER -i, which makes ffmpeg decode from the start and
// discard rather than seeking by keyframe. Slower, and worth it: keyframe
// seeking is approximate on a lossy source, so the fast form hands back a window
// a few milliseconds off the one that was digested, and the re-render reports a
// mismatch that nothing was actually wrong with.
func audioWindow(path string, start, end float64) ([]byte, error) {
	if end <= start {
		return nil, fmt.Errorf("raglit: window %.3f–%.3f is empty", start, end)
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-ss", ftoa(start), "-to", ftoa(end),
		"-ac", "1", "-ar", strconv.Itoa(pcmRate), "-f", "s16le", "-")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("raglit: ffmpeg %.3f–%.3f: %w (%s)", start, end, err, errb.String())
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("raglit: %.3f–%.3f decoded to nothing", start, end)
	}
	return out.Bytes(), nil
}

func levelled(path string, start, end float64) ([]byte, error) {
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-ss", ftoa(start), "-to", ftoa(end),
		"-af", "speechnorm", "-ac", "1", "-ar", strconv.Itoa(pcmRate), "-f", "s16le", "-")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("raglit: ffmpeg speechnorm: %w (%s)", err, errb.String())
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("raglit: speechnorm produced nothing")
	}
	return out.Bytes(), nil
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// waveform draws peak amplitude over the window.
//
// Peak per column rather than RMS: the question a reviewer brings to a picture
// of audio is "is there anything here", and RMS averages a short loud consonant
// away into a quiet column. Peak keeps it.
const (
	waveW = 900
	waveH = 160
)

func waveform(pcm []byte) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, waveW, waveH))
	bg := color.RGBA{0x11, 0x11, 0x11, 0xff}
	fg := color.RGBA{0x66, 0xcc, 0xff, 0xff}
	for y := range waveH {
		for x := range waveW {
			img.Set(x, y, bg)
		}
	}
	n := len(pcm) / 2
	if n == 0 {
		return img
	}
	mid := waveH / 2
	per := max(1, n/waveW)
	for x := range waveW {
		lo, hi := x*per, min(n, (x+1)*per)
		peak := 0
		for i := lo; i < hi; i++ {
			v := int(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		h := peak * mid / 32768
		for y := mid - h; y <= mid+h; y++ {
			if y >= 0 && y < waveH {
				img.Set(x, y, fg)
			}
		}
	}
	return img
}

// wavContainer wraps canonical PCM in the smallest thing a browser will play.
func wavContainer(pcm []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&b, binary.LittleEndian, uint32(pcmRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(pcmRate*pcmBits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(pcmBits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(pcmBits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// isAudioAsset reports whether a path is reviewed as a recording.
func isAudioAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".m4a", ".flac", ".ogg", ".opus", ".aac":
		return true
	}
	return false
}
