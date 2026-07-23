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

// resolveDoc turns the tool input into a real file path (external extractors —
// poppler, pandoc — read files). Exactly one of path / data must be set: path is
// file://… or a local path (used as-is); data is base64, written to a temp file
// whose extension is inferred from mime (then sniffed) so the router can classify
// it. cleanup removes any temp file.
func resolveDoc(path, data, mime string) (filePath string, cleanup func(), err error) {
	path, data = strings.TrimSpace(path), strings.TrimSpace(data)
	switch {
	case path != "" && data != "":
		return "", func() {}, fmt.Errorf("give `path` OR `data`, not both")
	case path != "":
		return strings.TrimPrefix(path, "file://"), func() {}, nil
	case data != "":
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return "", func() {}, fmt.Errorf("data is not valid base64: %w", err)
		}
		ext := raglit.ExtForContentType(mime)
		if ext == "" {
			ext = raglit.ExtForContentType(http.DetectContentType(raw))
		}
		if ext == "" {
			ext = ".bin"
		}
		f, err := os.CreateTemp("", "raglit-doc-*"+ext)
		if err != nil {
			return "", func() {}, err
		}
		if _, err := f.Write(raw); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", func() {}, err
		}
		f.Close()
		return f.Name(), func() { os.Remove(f.Name()) }, nil
	default:
		return "", func() {}, fmt.Errorf("provide `path` or `data`")
	}
}

// ocrDocument extracts a document to paged text via the format router
// (raglit.ExtractPaged): PDF text-layer/OCR hybrid, office via pandoc, images via
// the OCR cascade, text read directly.
func ocrDocument(ctx context.Context, ocr *raglit.OCR, filePath string) (ocrOut, error) {
	out := ocrOut{Pages: []ocrPage{}, Engines: map[string]int{}}
	pages, err := raglit.ExtractPaged(ctx, filePath, ocr)
	if err != nil {
		return out, err
	}
	for _, p := range pages {
		out.Pages = append(out.Pages, ocrPage{Page: p.Page, Text: p.Text, Engine: p.Engine})
		out.Engines[p.Engine]++
	}
	return out, nil
}

// buildToolOCR assembles the OCR the ocr tool uses: the vision client when a
// vision model is configured, plus the configured cheap engine. Never nil — the
// tool is useful even with no OCR at all (born-digital PDF text layer, office via
// pandoc, plain text); only a scanned image page then errors for lack of a VLM.
func buildToolOCR(lf *llmFlags, home raglit.Home) *raglit.OCR {
	ocr := &raglit.OCR{}
	if *lf.visionModel != "" {
		ocr.Client = lf.visionClient()
	}
	attachCheapOCR(ocr, home)
	return ocr
}

// ocrHandler is the MCP tool: image/PDF bytes → paged text.
func ocrHandler(ocr *raglit.OCR) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, cleanup, err := resolveDoc(req.GetString("path", ""), req.GetString("data", ""), req.GetString("mime", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer cleanup()
		res, err := ocrDocument(ctx, ocr, filePath)
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
