package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// PublicCatalogs are vendors whose /models, asked with a key, leaves out
// the image models the key can use: AIHubMix lists only its Gemini image
// models there, while gpt-image-2 and the rest draw with the same key. Its
// public catalog, by the host of the vendor's base URL, has them.
var PublicCatalogs = map[string]string{
	"aihubmix.com": "https://aihubmix.com/api/v1/models",
}

// PublicDrawers are the image models the public catalog for base's host
// lists, marked as drawing; nil when there is none or it can't be read.
func PublicDrawers(ctx context.Context, base string) []Model {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return nil
	}
	at, ok := PublicCatalogs[strings.TrimPrefix(u.Hostname(), "www.")]
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, at, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "magpie")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	var out []Model
	for _, raw := range list.Data {
		var m struct {
			ID      string `json:"model_id"`
			Name    string `json:"model_name"`
			Types   string `json:"types"`
			Out     string `json:"output_modalities"`
			In      string `json:"input_modalities"`
			Release string `json:"release_date"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			continue
		}
		// what makes images and is asked the way magpie draws: Ideogram's
		// V_2, DESCRIBE and UPSCALE are the vendor's own tasks
		if !strings.Contains(m.Types, "image_generation") && !strings.Contains(m.Out, "image") || !DrawsID(m.ID) {
			continue
		}
		d := Model{ID: m.ID, Name: m.Name, Draws: true, Released: m.Release}
		if d.Name == "" {
			d.Name = m.ID
		}
		if slices.Contains(strings.Split(m.In, ","), "image") {
			d.Images, d.ImageInput = true, imageInputOf(true)
		}
		out = append(out, d)
	}
	return out
}

// WithDrawers adds the drawers not already in ms.
func WithDrawers(ms, drawers []Model) []Model {
	for _, d := range drawers {
		if !slices.ContainsFunc(ms, func(m Model) bool { return m.ID == d.ID }) {
			ms = append(ms, d)
		}
	}
	return ms
}

func imageInputOf(b bool) *bool { return &b }
