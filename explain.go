package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ExplainReport is the pre-flight report for an ingest target: every decision
// the real ingest would make, computed without writing anything.
type ExplainReport struct {
	// Kind is the document kind (pdf, text, image, …).
	Kind string `json:"kind"`
	// KindDecider says what determined the kind: "extension" or "content-type".
	KindDecider string `json:"kind_decider"`
	// Lane is the lane the document would take: "light" or "heavy".
	Lane string `json:"lane"`
	// Fragmenter is the fragmentation mode: "text-overlap" or "llm-seg".
	// For PDFs and images the answer is conditional (llm-seg if a page
	// escalates to the VLM); for text/code kinds it is always text-overlap.
	Fragmenter string `json:"fragmenter"`
	// FragmenterNote explains the fragmenter choice, including the
	// conditional for vision kinds.
	FragmenterNote string `json:"fragmenter_note"`
	// FragWindow, FragStride, FragFloor are the resolved fragmentation
	// parameters (after defaults and embed-limit cap).
	FragWindow  int `json:"frag_window"`
	FragStride  int `json:"frag_stride"`
	FragFloor   int `json:"frag_floor"`
	// EmbedderConfigured reports whether an embedder is in the registry.
	EmbedderConfigured bool `json:"embedder_configured"`
	// IdentityQueued reports whether identity extraction (caption/summary)
	// would be queued: true when an identifier model is configured.
	IdentityQueued bool `json:"identity_queued"`
	// Index is the index name the document would land in.
	Index string `json:"index"`
	// DocIdentity is the URL/path under which the document would be stored.
	DocIdentity string `json:"doc_identity"`
	// DocExists reports whether that identity already has a row in the index.
	DocExists bool `json:"doc_exists"`
	// HashShortCircuits reports whether the stored content hash matches the
	// source file's hash, meaning the real ingest would skip (no re-ingest).
	// Only meaningful when DocExists is true.
	HashShortCircuits bool `json:"hash_short_circuits"`
}

// ExplainInput carries what Explain needs: the target URL, the index name,
// and the fragmentation parameters from the worker's config.
type ExplainInput struct {
	URL      string
	Index    string
	Window   int
	Stride   int
	Floor    int
	EmbedLimit int // 0 = uncapped
}

// Explain computes the pre-flight report for an ingest target. It reads from
// the store (document existence, content hash) and from the registry
// (embedder, identifier) but writes nothing: no index entry, no fragment, no
// job stage, no identity queue item.
func Explain(ctx context.Context, reg *Registry, st *Store, in ExplainInput) (*ExplainReport, error) {
	rep := &ExplainReport{
		Index:       in.Index,
		DocIdentity: in.URL,
	}

	// 1. Kind + decider.
	// ClassifyDoc tries extension first, then content-type.
	// For a local file we can read the first bytes to get a content-type
	// hint; for http(s) we would need to fetch (we don't — that's the
	// worker's job). We use the extension as the primary signal and note
	// the content-type path as the fallback.
	ext := strings.ToLower(extOf(in.URL))
	ct := contentTypeFromExt(ext)
	kind := ClassifyDoc(in.URL, ct)
	decider := "extension"
	if ext == "" {
		decider = "content-type"
	}
	rep.Kind = kind.String()
	rep.KindDecider = decider

	// 2. Lane.
	lane := LaneForKind(kind)
	if lane == LaneHeavy {
		rep.Lane = "heavy"
	} else {
		rep.Lane = "light"
	}

	// 3. Fragmenter.
	// pipeline.go:476 — fragModeLLM only when sawVision && sg != nil.
	// sawVision is true for PDF and Image kinds (pages that escalate to VLM).
	// For text/code/office/email/spreadsheet/audio the answer is deterministic:
	// text-overlap.
	isVisionKind := kind == KindPDF || kind == KindImage
	if isVisionKind {
		rep.Fragmenter = "conditional"
		rep.FragmenterNote = "llm-seg if any page escalates to the VLM; otherwise text-overlap"
	} else {
		rep.Fragmenter = "text-overlap"
		rep.FragmenterNote = "deterministic for this kind"
	}
	// Resolved frag params apply to the text-overlap path.
	fw, fs, ff := ResolveFragParams(in.Window, in.Stride, in.Floor, in.EmbedLimit)
	rep.FragWindow = fw
	rep.FragStride = fs
	rep.FragFloor = ff

	// 4. Embedder + identity.
	rep.EmbedderConfigured = reg.HasEmbedder()
	rep.IdentityQueued = reg.HasIdentifier()

	// 5. Document identity existence + hash short-circuit.
	// st is nil when the index does not exist yet (explain into a new index).
	if st != nil {
		storedHash, _ := st.DocumentHash(in.URL)
		if storedHash != "" {
			rep.DocExists = true
			// Compute the source file's hash to check the short-circuit.
			srcHash, err := sourceFileHash(in.URL)
			if err == nil && srcHash == storedHash {
				rep.HashShortCircuits = true
			}
		}
	}

	return rep, nil
}

// extOf returns the file extension of a URL or path, without the dot.
// For file:// URLs it extracts the path first.
func extOf(u string) string {
	if strings.HasPrefix(u, "file://") {
		parsed, err := url.Parse(u)
		if err == nil {
			return extFromPath(parsed.Path)
		}
		return ""
	}
	// Bare path or http URL: use the last path segment.
	idx := strings.LastIndex(u, "/")
	if idx >= 0 {
		u = u[idx+1:]
	}
	return extFromPath(u)
}

func extFromPath(p string) string {
	dot := strings.LastIndex(p, ".")
	if dot < 0 || dot == len(p)-1 {
		return ""
	}
	return p[dot:]
}

// contentTypeFromExt returns a plausible content-type for a file extension,
// matching what an HTTP server would send. Used only for the pre-flight
// classification; the worker gets the real content-type from the fetch.
func contentTypeFromExt(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tiff", ".bmp", ".heic":
		return "image/" + strings.TrimPrefix(ext, ".")
	case ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".mp4", ".webm":
		return "audio/" + strings.TrimPrefix(ext, ".")
	case ".txt", ".md", ".rst", ".adoc":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".json", ".yaml", ".yml", ".toml", ".xml":
		return "text/plain"
	case ".eml", ".mbox":
		return "message/rfc822"
	}
	return ""
}

// sourceFileHash computes the sha256 hex of a local file's bytes.
// Returns "" and an error for non-local URLs (http/https).
func sourceFileHash(u string) (string, error) {
	var path string
	if strings.HasPrefix(u, "file://") {
		parsed, err := url.Parse(u)
		if err != nil {
			return "", fmt.Errorf("parse file URL: %w", err)
		}
		path = parsed.Path
	} else if !strings.Contains(u, "://") {
		// Bare path.
		path = u
	} else {
		// http://, https://, etc. — cannot read without a network fetch.
		return "", fmt.Errorf("remote URL: %s", u)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// HasEmbedder reports whether an embedder is configured in the registry.
func (r *Registry) HasEmbedder() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.embedder != nil
}
