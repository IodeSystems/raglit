package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestPNG writes a small non-blank PNG to path and returns it.
func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			im.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0xff, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureStderr runs fn with os.Stderr replaced by a pipe and returns whatever
// was written to it. The return value of fn is discarded: the command will
// fail at the model call (no live endpoint), and the gate message is printed
// before that, so the failure is expected.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = orig
	var buf bytes.Buffer
	b := make([]byte, 4096)
	for {
		n, _ := r.Read(b)
		buf.Write(b[:n])
		if n == 0 {
			break
		}
	}
	return buf.String()
}

// A PNG has no native raster, so NativeDPI returns 0 and the gate refuses
// descent: the page is read whole at the DPI the decision picked, and the
// refusal is printed to stderr pointing at the gate.
func TestRegionsGateRefusesDescentWhenSourceHoldsNothing(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "page.png")
	writeTestPNG(t, doc)

	got := captureStderr(t, func() {
		_ = runRegions([]string{"--llm-model", "test-model", "--depth", "3", doc})
	})
	t.Logf("stderr: %q", got)

	if !strings.Contains(got, "descent refused") {
		t.Errorf("expected gate message 'descent refused' in stderr, got:\n%s", got)
	}
	if !strings.Contains(got, "native 0 dpi") {
		t.Errorf("expected gate to name the native dpi (0) in stderr, got:\n%s", got)
	}
}

// With --depth 0 the gate is not consulted at all — the page is read whole
// by design, not because the gate refused. No gate message should appear.
func TestRegionsGateNotConsultedWhenDepthZero(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "page.png")
	writeTestPNG(t, doc)

	got := captureStderr(t, func() {
		_ = runRegions([]string{"--llm-model", "test-model", "--depth", "0", doc})
	})
	t.Logf("stderr: %q", got)

	if strings.Contains(got, "descent refused") {
		t.Errorf("gate message should not appear with --depth 0, got:\n%s", got)
	}
}
