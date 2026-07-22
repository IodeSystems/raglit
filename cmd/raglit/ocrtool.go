package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ocrPage is one page's transcription plus which engine produced it (the cheap
// engine's name when its result passed the gibberish gate, else "vision").
type ocrPage struct {
	Page   int    `json:"page"`
	Text   string `json:"text"`
	Engine string `json:"engine"`
}

// ocrOut is the ocr tool's paged-text result: pages in order, plus a per-engine
// tally so a caller sees how many pages needed the (expensive) vision fallback.
type ocrOut struct {
	Pages   []ocrPage      `json:"pages"`
	Engines map[string]int `json:"engines"`
}

// loadDoc resolves the tool input to raw bytes + a mime hint. Exactly one of
// path / data must be set: path is file://… or a local path; data is base64.
func loadDoc(path, data, mime string) (raw []byte, resolvedMime string, err error) {
	path, data = strings.TrimSpace(path), strings.TrimSpace(data)
	switch {
	case path != "" && data != "":
		return nil, "", fmt.Errorf("give `path` OR `data`, not both")
	case path != "":
		p := strings.TrimPrefix(path, "file://")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, "", err
		}
		if mime == "" {
			mime = mimeForImage(p) // extension-based; PDFs are sniffed below
		}
		return b, mime, nil
	case data != "":
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, "", fmt.Errorf("data is not valid base64: %w", err)
		}
		return b, mime, nil
	default:
		return nil, "", fmt.Errorf("provide `path` or `data`")
	}
}

// docIsPDF decides whether the bytes are a PDF (→ rasterize to pages) rather than
// a single image, from the mime, the path extension, or the %PDF magic.
func docIsPDF(raw []byte, mime, path string) bool {
	if strings.Contains(strings.ToLower(mime), "pdf") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".pdf") {
		return true
	}
	return len(raw) >= 5 && string(raw[:5]) == "%PDF-"
}

// ocrDocument runs the cascade over a document: a PDF is rasterized to its
// embedded page images (PagifyBytes); anything else is treated as a single page.
// Pure/testable — the MCP handler is a thin wrapper over it.
func ocrDocument(ctx context.Context, ocr *raglit.OCR, raw []byte, mime, path string) (ocrOut, error) {
	out := ocrOut{Pages: []ocrPage{}, Engines: map[string]int{}}
	var pages []raglit.PageImage
	if docIsPDF(raw, mime, path) {
		p, err := raglit.PagifyBytes(raw)
		if err != nil {
			return out, err // incl. ErrNoPageImages for a born-digital PDF
		}
		pages = p
	} else {
		if mime == "" || !strings.HasPrefix(mime, "image/") {
			mime = http.DetectContentType(raw) // sniff png/jpeg/… when unknown
		}
		pages = []raglit.PageImage{{Page: 1, Mime: mime, Data: raw}}
	}
	for _, pg := range pages {
		text, engine, err := ocr.PageWithEngine(ctx, pg)
		if err != nil {
			return out, fmt.Errorf("page %d: %w", pg.Page, err)
		}
		out.Pages = append(out.Pages, ocrPage{Page: pg.Page, Text: text, Engine: engine})
		out.Engines[engine]++
	}
	return out, nil
}

// buildToolOCR assembles the OCR the ocr tool uses: the vision client when a
// vision model is configured, plus the configured cheap engine. Returns nil when
// NEITHER is available — there is nothing to OCR with, so the tool is not offered.
func buildToolOCR(lf *llmFlags, home raglit.Home) *raglit.OCR {
	ocr := &raglit.OCR{}
	if *lf.visionModel != "" {
		ocr.Client = lf.visionClient()
	}
	attachCheapOCR(ocr, home)
	if ocr.Client == nil && ocr.Cheap == nil {
		return nil
	}
	return ocr
}

// ocrHandler is the MCP tool: image/PDF bytes → paged text.
func ocrHandler(ocr *raglit.OCR) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, mime, err := loadDoc(req.GetString("path", ""), req.GetString("data", ""), req.GetString("mime", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := ocrDocument(ctx, ocr, raw, mime, req.GetString("path", ""))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("ocr", err), nil
		}
		b, err := json.Marshal(res)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("encode", err), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}
