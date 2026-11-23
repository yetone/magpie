package catalog

// models.dev's api.json (v1) leaves out the models it marks v2_only, and
// every embedding model is one: OpenRouter's qwen/qwen3-embedding-8b and
// its 39 others aren't in v1 at all, Vercel's 28 are there with no price.
// Its experimental v2 catalog lists them, each with what its host charges
// a million tokens, so an embedding call there was counted unpriced
// (Winnie7572 on X, #7321: 415 of 420). Sync keeps v2's embedding models
// beside the provider catalog, and the load adds each one v1 lacks, or
// lists without a price, to v1's provider: v1's word stands wherever it
// gives one.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// EmbeddingsPath is where Sync keeps models.dev's v2 embedding models,
// beside its provider catalog.
func EmbeddingsPath() string {
	return filepath.Join(filepath.Dir(CachePath()), "embeddings.json")
}

// embeddingsTried is set once this process asked models.dev for its v2
// embedding models, answered or not (labsTried).
var embeddingsTried atomic.Bool

// embeddingsURL is v2's embedding models beside the provider catalog
// modelsDevURL names, or "" when it names none (a test's own server root).
func embeddingsURL() string {
	if base, ok := strings.CutSuffix(modelsDevURL, "/api.json"); ok {
		return base + "/experimental/v2.0/api.json?type=embedding"
	}
	return ""
}

// v2Provider and v2Model are what magpie reads of a v2 provider and its
// models: v2 names a model's kind (type), words its inputs as media types,
// and prices each part as {"price": USD a million tokens}, or a unit's
// ("character") when it says one.
type v2Provider struct {
	Models map[string]v2Model `json:"models"`
}

type v2Model struct {
	ID          string `json:"id"`
	Canonical   string `json:"canonical_id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Family      string `json:"family"`
	ReleaseDate string `json:"release_date"`
	Modalities  struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost *struct {
		Text *struct {
			Input  *v2Part `json:"input"`
			Output *v2Part `json:"output"`
		} `json:"text"`
	} `json:"cost"`
}

type v2Part struct {
	Price *float64 `json:"price"`
	Unit  string   `json:"unit"`
}

// price is the model's text price a million tokens: its input's, and its
// output's or none. A model priced by another unit (Vertex's per
// character), or with no input price, has none here.
func (m v2Model) price() (*Price, bool) {
	if m.Cost == nil || m.Cost.Text == nil {
		return nil, false
	}
	in, out := m.Cost.Text.Input, m.Cost.Text.Output
	if in == nil || in.Price == nil || in.Unit != "" || *in.Price < 0 {
		return nil, false
	}
	p := &Price{Input: *in.Price}
	if out != nil {
		if out.Unit != "" || out.Price == nil || *out.Price < 0 {
			return nil, false
		}
		p.Output = *out.Price
	}
	return p, true
}

// readEmbeddings is the v2 embedding models Sync kept, by provider, or
// none when it has none or can't be read: v1 answers alone, as before.
func readEmbeddings() map[string]v2Provider {
	b, err := os.ReadFile(EmbeddingsPath())
	if err != nil {
		return nil
	}
	var m map[string]v2Provider
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// addEmbeddings gives each provider of the v1 catalog all the v2
// embedding models it lacks, and a price to one it lists without any.
// One added outputs embeddings whatever v2 says (it words some "text"),
// so it is never taken for a chat model (textModel). A provider v1 doesn't
// list gets none: it has no entry of its own here.
func addEmbeddings(all map[string]mdProvider, v2 map[string]v2Provider) {
	for pid, p := range v2 {
		have, ok := all[pid]
		if !ok || have.Models == nil {
			continue
		}
		for id, x := range p.Models {
			if x.Type != "embedding" {
				continue
			}
			pr, priced := x.price()
			if was, ok := have.Models[id]; ok {
				if was.Cost == nil && priced {
					was.Cost = pr
					have.Models[id] = was
				}
				continue
			}
			if x.ID == "" {
				x.ID = id
			}
			m := mdModel{ID: x.ID, Name: x.Name, ReleaseDate: x.ReleaseDate, Family: x.Family, Canonical: x.Canonical}
			m.Modalities.Input, m.Modalities.Output = Kinds(x.Modalities.Input), []string{"embedding"}
			m.Limit.Context, m.Limit.Output = x.Limit.Context, x.Limit.Output
			if priced {
				m.Cost = pr
			}
			have.Models[id] = m
		}
	}
}

// syncEmbeddings fetches v2's embedding models into EmbeddingsPath,
// written whole through a rename. A failed fetch, or an answer that isn't
// a catalog, keeps the one kept before.
func syncEmbeddings(ctx context.Context) (bool, error) {
	u := embeddingsURL()
	if u == "" {
		return false, nil
	}
	embeddingsTried.Store(true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("models.dev v2: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return false, err
	}
	var probe map[string]v2Provider
	if err := json.Unmarshal(b, &probe); err != nil || len(probe) == 0 {
		return false, fmt.Errorf("models.dev v2: unexpected payload")
	}
	return keepFetched(EmbeddingsPath(), b)
}

// embeddingsMissing is labsMissing for the v2 embedding models: a catalog
// kept before magpie read them has them fetched at once, not a day later.
func embeddingsMissing() bool {
	if embeddingsTried.Load() || embeddingsURL() == "" || Source() != CachePath() {
		return false
	}
	_, err := os.Stat(EmbeddingsPath())
	return err != nil
}
