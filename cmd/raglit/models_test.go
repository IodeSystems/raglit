package main

import "testing"

// corrallm-shaped catalog (subset of the live /v1/models enrichment).
func corrallmCatalog() []modelInfo {
	ms := []modelInfo{
		{ID: "Qwen3-6-27B-MPT", Type: "chat", Capability: "chat", State: "ready", Quality: 2, Vision: true},
		{ID: "ternary-bonsai-27b", Type: "chat", Capability: "chat", State: "absent", Quality: 1, Vision: true},
		{ID: "groq-llama-70b", Type: "chat", Capability: "chat", State: "absent", Quality: 3},
		{ID: "nomic-embed-text", Type: "embed", Capability: "embeddings", State: "ready", Quality: 0},
		{ID: "stt", Type: "stt", Capability: "audio.stt", State: "ready", Audio: true},
		{ID: "tts", Type: "tts", Capability: "audio.tts", State: "absent", Audio: true},
	}
	sortModels(ms)
	return ms
}

func TestHasCapabilities(t *testing.T) {
	if !hasCapabilities(corrallmCatalog()) {
		t.Fatal("enriched catalog should report capabilities")
	}
	plain := []modelInfo{{ID: "gpt-4o"}, {ID: "text-embedding-3-small"}}
	if hasCapabilities(plain) {
		t.Fatal("bare OpenAI catalog should report no capabilities")
	}
}

func TestFilterByRole(t *testing.T) {
	all := corrallmCatalog()

	embed := filterByRole(all, "embed")
	if len(embed) != 1 || embed[0].ID != "nomic-embed-text" {
		t.Fatalf("embed filter = %v, want [nomic-embed-text]", ids(embed))
	}

	vision := filterByRole(all, "vision")
	// Both image-modality chat models, ready first.
	if len(vision) != 2 || vision[0].ID != "Qwen3-6-27B-MPT" {
		t.Fatalf("vision filter = %v, want ready Qwen first then bonsai", ids(vision))
	}
	for _, m := range vision {
		if !m.Vision {
			t.Fatalf("vision filter returned non-vision model %s", m.ID)
		}
	}

	chat := filterByRole(all, "chat")
	if len(chat) != 3 {
		t.Fatalf("chat filter = %v, want the 3 chat models", ids(chat))
	}
}

func TestSortModels_ReadyThenQuality(t *testing.T) {
	ms := []modelInfo{
		{ID: "absent-hi", State: "absent", Quality: 5},
		{ID: "ready-lo", State: "ready", Quality: 1},
		{ID: "ready-hi", State: "ready", Quality: 3},
	}
	sortModels(ms)
	want := []string{"ready-hi", "ready-lo", "absent-hi"}
	for i, w := range want {
		if ms[i].ID != w {
			t.Fatalf("sort = %v, want %v", ids(ms), want)
		}
	}
}

func TestModelLabel(t *testing.T) {
	got := modelLabel(modelInfo{ID: "Qwen3-6-27B-MPT", Capability: "chat", State: "ready", Vision: true})
	want := "Qwen3-6-27B-MPT  (vision, chat, ready)"
	if got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if bare := modelLabel(modelInfo{ID: "gpt-4o"}); bare != "gpt-4o" {
		t.Fatalf("bare label = %q, want %q", bare, "gpt-4o")
	}
}

func ids(ms []modelInfo) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
