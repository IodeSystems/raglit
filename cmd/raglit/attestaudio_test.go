package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"math"
	"testing"
)

func tone(samples int, amp float64) []byte {
	var b bytes.Buffer
	for i := range samples {
		v := int16(amp * 32767 * math.Sin(float64(i)/20))
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

// The waveform is the fallback a reviewer gets when they cannot listen, so it
// has to actually distinguish silence from sound. A picture that looks the same
// either way is worse than no picture: it invites `unclear` to be answered from
// an image that never had the answer in it.
func TestWaveformDistinguishesSilenceFromSound(t *testing.T) {
	loud := waveform(tone(16000, 0.9))
	quiet := waveform(make([]byte, 32000))

	ink := func(img interface {
		At(x, y int) (c interface{ RGBA() (r, g, b, a uint32) })
	}) int {
		return 0
	}
	_ = ink

	countInk := func(w, h int, at func(x, y int) (uint32, uint32, uint32)) int {
		n := 0
		for y := range h {
			for x := range w {
				r, g, b := at(x, y)
				if r > 0x3000 || g > 0x3000 || b > 0x3000 {
					n++
				}
			}
		}
		return n
	}
	lb, qb := loud.Bounds(), quiet.Bounds()
	loudInk := countInk(lb.Dx(), lb.Dy(), func(x, y int) (uint32, uint32, uint32) {
		r, g, b, _ := loud.At(x, y).RGBA()
		return r, g, b
	})
	quietInk := countInk(qb.Dx(), qb.Dy(), func(x, y int) (uint32, uint32, uint32) {
		r, g, b, _ := quiet.At(x, y).RGBA()
		return r, g, b
	})
	if loudInk <= quietInk*4 {
		t.Fatalf("a loud window drew %d lit pixels and silence drew %d — the picture does not carry the answer", loudInk, quietInk)
	}
}

func TestWaveformEncodesAndHandlesEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, waveform(nil)); err != nil {
		t.Fatalf("empty pcm did not encode: %v", err)
	}
	if b.Len() == 0 {
		t.Fatal("encoded to nothing")
	}
}

// The container is framing; the digest is over samples. Asserting the header
// length keeps a future edit from quietly digesting the wrapper.
func TestWavContainerWrapsWithoutTouchingSamples(t *testing.T) {
	pcm := tone(100, 0.5)
	w := wavContainer(pcm)
	if len(w) != len(pcm)+44 {
		t.Fatalf("container is %d bytes for %d of pcm, want a 44-byte header", len(w), len(pcm))
	}
	if !bytes.Equal(w[44:], pcm) {
		t.Fatal("samples were altered by packaging")
	}
	if string(w[:4]) != "RIFF" || string(w[8:12]) != "WAVE" {
		t.Fatal("not a RIFF/WAVE header")
	}
}

func TestIsAudioAsset(t *testing.T) {
	for _, p := range []string{"a.wav", "b.MP3", "c.m4a", "d.opus"} {
		if !isAudioAsset(p) {
			t.Errorf("isAudioAsset(%q) = false", p)
		}
	}
	for _, p := range []string{"a.pdf", "b.md", "c.png"} {
		if isAudioAsset(p) {
			t.Errorf("isAudioAsset(%q) = true", p)
		}
	}
}
