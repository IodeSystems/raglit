package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// stubChatter records what the OCR path sent and returns a canned transcription.
type stubChatter struct {
	sawImage bool
	sawText  bool
	dataURI  string
	reply    string
}

func (s *stubChatter) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolDef) (string, []llm.ToolCall, error) {
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Type {
			case "image_url":
				if p.ImageURL != nil {
					s.sawImage = true
					s.dataURI = p.ImageURL.URL
				}
			case "text":
				s.sawText = true
			}
		}
	}
	return s.reply, nil, nil
}

func TestOCR_Page_SendsMultimodalAndTrims(t *testing.T) {
	sc := &stubChatter{reply: "  Refresh token rotates.  \n"}
	got, err := NewOCR(sc).Page(context.Background(), PageImage{
		Page: 2, Mime: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.sawImage {
		t.Error("OCR did not attach an image part")
	}
	if !sc.sawText {
		t.Error("OCR did not include a text instruction")
	}
	if !strings.HasPrefix(sc.dataURI, "data:image/png;base64,") {
		t.Errorf("image not sent as a png data URI: %q", sc.dataURI)
	}
	if got != "Refresh token rotates." {
		t.Errorf("result not trimmed: %q", got)
	}
}
