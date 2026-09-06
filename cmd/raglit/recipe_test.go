package main

import (
	"strings"
	"testing"
)

// The pool caches indexing work by (recipe, file). Anything that changes what a
// document's indexed output IS must be in the recipe, or a change is a cache
// HIT: the pool replays the old work and the job reports done, with nothing
// anywhere saying the result is not what the current settings would produce.
func TestIngestRecipe_ChangesWithEveryTermThatShapesTheOutput(t *testing.T) {
	base := recipeInputs{
		VisionModel: "vision-1", EmbedModel: "embed-1", CheapEngine: "tesseract",
		FragWindow: 9000, FragStride: 6000, FragFloor: 3000,
		IndexHint: "RO means repair order.",
	}
	want := ingestRecipe(base)

	for _, tc := range []struct {
		name string
		mut  func(*recipeInputs)
	}{
		{"vision model", func(i *recipeInputs) { i.VisionModel = "vision-2" }},
		{"embed model", func(i *recipeInputs) { i.EmbedModel = "embed-2" }},
		{"cheap engine", func(i *recipeInputs) { i.CheapEngine = "paddleocr" }},
		{"frag window", func(i *recipeInputs) { i.FragWindow = 8000 }},
		{"frag stride", func(i *recipeInputs) { i.FragStride = 5000 }},
		{"frag floor", func(i *recipeInputs) { i.FragFloor = 2000 }},
		// The one this test exists for. The hint reaches the transcription and
		// segmentation prompts, so a page read under one is not the same page
		// read under another.
		{"index hint", func(i *recipeInputs) { i.IndexHint = "RO means received." }},
		{"index hint cleared", func(i *recipeInputs) { i.IndexHint = "" }},
	} {
		got := base
		tc.mut(&got)
		if ingestRecipe(got) == want {
			t.Errorf("changing the %s did not change the recipe — a change to it is a cache HIT", tc.name)
		}
	}

	// And it is stable: the same inputs must not reprocess a corpus for nothing.
	if ingestRecipe(base) != want {
		t.Error("the recipe is not stable across calls")
	}
	// The hint is hashed rather than inlined, so a paragraph of prose does not
	// become the pool key.
	if strings.Contains(want, "repair order") {
		t.Errorf("the hint text is inlined in the recipe key: %q", want)
	}
}
