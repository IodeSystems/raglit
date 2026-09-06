package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Nomic-vision image embedder — the shipped ImageEmbedder.
//
// nomic-embed-vision-v1.5 embeds an image into the SAME latent space as
// nomic-embed-text-v1.5. Since raglit's default text embedder IS nomic-embed-text,
// a figure embedded here is directly cosine-comparable to a text query embedded by
// the text embedder — no separate query tower. That is why Aligned() is true: it
// holds ONLY when the configured text embed model is the nomic-text pair. Point
// URL at Nomic's Atlas API or a self-hosted endpoint of the same shape.

// defaultNomicVisionURL is Nomic's Atlas image-embedding endpoint.
const defaultNomicVisionURL = "https://api-atlas.nomic.ai/v1/embedding/image"

// defaultNomicVisionModel is the model whose space aligns with nomic-embed-text.
const defaultNomicVisionModel = "nomic-embed-vision-v1.5"

// NomicVisionEmbedder embeds figure images via nomic-embed-vision over HTTP. Its
// vectors are L2-normalized (like the text embedder's), so cosine is a dot product.
type NomicVisionEmbedder struct {
	URL    string // full endpoint; "" → defaultNomicVisionURL
	APIKey string
	Model  string // "" → defaultNomicVisionModel
	HTTP   *http.Client
}

// NewNomicVisionEmbedder builds the embedder. url/model empty → the nomic defaults.
func NewNomicVisionEmbedder(url, apiKey, model string) *NomicVisionEmbedder {
	if url == "" {
		url = defaultNomicVisionURL
	}
	if model == "" {
		model = defaultNomicVisionModel
	}
	return &NomicVisionEmbedder{
		URL: url, APIKey: apiKey, Model: model,
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}
}

// Aligned reports that nomic-vision shares nomic-text's space — true by
// construction (this embedder is the nomic pair). The caller is responsible for
// keeping the text embed model on nomic-embed-text; a mismatch silently degrades
// figure ranking rather than erroring.
func (e *NomicVisionEmbedder) Aligned() bool { return true }

// EmbedImage posts the image as multipart form-data (model + images file) and
// returns the L2-normalized embedding.
func (e *NomicVisionEmbedder) EmbedImage(ctx context.Context, mime string, data []byte) ([]float32, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("model", e.Model); err != nil {
		return nil, err
	}
	fw, err := w.CreateFormFile("images", "figure"+extForImageMime(mime))
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raglit: nomic-vision: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("raglit: nomic-vision: %s: %s", resp.Status, bytes.TrimSpace(rb))
	}
	v, err := parseImageEmbedding(rb)
	if err != nil {
		return nil, err
	}
	normalize(v)
	return v, nil
}

// parseImageEmbedding pulls the first vector out of the response, tolerating both
// nomic's {"embeddings":[[...]]} and an OpenAI-style {"data":[{"embedding":[...]}]}.
func parseImageEmbedding(b []byte) ([]float32, error) {
	var r struct {
		Embeddings [][]float32 `json:"embeddings"`
		Data       []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("raglit: nomic-vision: decode: %w", err)
	}
	if len(r.Embeddings) > 0 && len(r.Embeddings[0]) > 0 {
		return r.Embeddings[0], nil
	}
	if len(r.Data) > 0 && len(r.Data[0].Embedding) > 0 {
		return r.Data[0].Embedding, nil
	}
	return nil, fmt.Errorf("raglit: nomic-vision: no embedding in response")
}

// extForImageMime maps an image mime to a filename extension (the endpoint sniffs
// format from the upload); defaults to .png.
func extForImageMime(mime string) string {
	switch {
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return ".jpg"
	case strings.Contains(mime, "webp"):
		return ".webp"
	case strings.Contains(mime, "tiff"):
		return ".tif"
	case strings.Contains(mime, "gif"):
		return ".gif"
	default:
		return ".png"
	}
}
