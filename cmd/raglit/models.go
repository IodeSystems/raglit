package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// modelInfo is one entry from an OpenAI-compatible /v1/models list. Plain OpenAI
// servers populate only ID. corrallm enriches each entry with type/capability/
// modalities/state (its "capabilities matrix"), which lets `raglit init` filter
// the pick lists per role — embeddings for --embed, image-in for OCR — instead
// of dumping every chat/stt/tts model at the user.
type modelInfo struct {
	ID         string
	Type       string // chat|embed|stt|tts|realtime|"" (corrallm)
	Capability string // chat|embeddings|audio.stt|... (corrallm)
	State      string // ready|absent|"" (corrallm)
	Quality    int    // corrallm quality tier; higher = better
	Vision     bool   // modalities.image present → can read images (OCR)
	Audio      bool   // modalities.audio present
}

// enriched reports whether this entry carried any capability metadata beyond its
// id — i.e. the server is corrallm-class, not a bare OpenAI /v1/models list.
func (m modelInfo) enriched() bool {
	return m.Type != "" || m.Capability != "" || m.State != "" || m.Vision || m.Audio
}

// modelsURL normalizes an OpenAI base URL to its /v1/models catalog endpoint,
// tolerating a base with or without a trailing /v1.
func modelsURL(base string) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	return u + "/models"
}

// fetchModels GETs <base>/v1/models and returns the catalog, best-ranked first
// (ready before absent, then higher quality). Enrichment fields are empty for a
// plain OpenAI server; corrallm fills them in.
func fetchModels(ctx context.Context, base, key string) ([]modelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(base), nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID         string                     `json:"id"`
			Type       string                     `json:"type"`
			Capability string                     `json:"capability"`
			State      string                     `json:"state"`
			Quality    int                        `json:"quality"`
			Modalities map[string]json.RawMessage `json:"modalities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]modelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		_, vision := m.Modalities["image"]
		_, audio := m.Modalities["audio"]
		models = append(models, modelInfo{
			ID: m.ID, Type: m.Type, Capability: m.Capability, State: m.State,
			Quality: m.Quality, Vision: vision, Audio: audio,
		})
	}
	sortModels(models)
	return models, nil
}

// sortModels ranks ready models first, then by descending quality, then id, so
// the best default per role floats to the top of a pick list.
func sortModels(ms []modelInfo) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if ar, br := a.State == "ready", b.State == "ready"; ar != br {
			return ar
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
		return a.ID < b.ID
	})
}

// hasCapabilities reports whether the catalog carries capability metadata at all
// (any enriched entry). False → a plain OpenAI server; show the unfiltered list.
func hasCapabilities(ms []modelInfo) bool {
	for _, m := range ms {
		if m.enriched() {
			return true
		}
	}
	return false
}

// roleMatches reports whether a model can serve the given role:
//   - "embed"  → text-embedding model (for --embed / vector search)
//   - "vision" → image-in model (for PDF/scan OCR)
//   - "chat"   → text chat model
func roleMatches(m modelInfo, role string) bool {
	switch role {
	case "embed":
		return m.Capability == "embeddings" || m.Type == "embed"
	case "vision":
		return m.Vision
	case "chat":
		return m.Capability == "chat" || m.Type == "chat"
	}
	return false
}

// filterByRole keeps only the models that can serve role (order preserved).
func filterByRole(ms []modelInfo, role string) []modelInfo {
	var out []modelInfo
	for _, m := range ms {
		if roleMatches(m, role) {
			out = append(out, m)
		}
	}
	return out
}

// modelLabel renders a model for a pick list: its id plus capability tags
// (vision / capability / state) when the server provided them.
func modelLabel(m modelInfo) string {
	var tags []string
	if m.Vision {
		tags = append(tags, "vision")
	}
	if m.Capability != "" {
		tags = append(tags, m.Capability)
	} else if m.Type != "" {
		tags = append(tags, m.Type)
	}
	if m.State != "" {
		tags = append(tags, m.State)
	}
	if len(tags) == 0 {
		return m.ID
	}
	return fmt.Sprintf("%s  (%s)", m.ID, strings.Join(tags, ", "))
}
