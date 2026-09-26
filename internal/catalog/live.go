package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// A vendor's own /models endpoint is the truth about what it serves today;
// models.dev lags and keeps legacy names around. magpie asks the vendor when
// it has a key, remembers the answer next to the models.dev cache, and lets
// the catalog fill in display names and reasoning levels.

// LivePath is where the fetched model list of one provider is kept.
func LivePath(provider string) string {
	return filepath.Join(filepath.Dir(CachePath()), "models", provider+".json")
}

type liveFile struct {
	Fetched time.Time `json:"fetched"`
	Base    string    `json:"base"`
	Models  []Model   `json:"models"`
}

// Live returns the model list last fetched from the provider, if any.
func Live(provider string) (models []Model, fetched time.Time, ok bool) {
	f, err := filememo.Read("live models", LivePath(provider), func(b []byte) (liveFile, error) {
		var f liveFile
		err := json.Unmarshal(b, &f)
		return f, err
	})
	if err != nil || len(f.Models) == 0 {
		return nil, time.Time{}, false
	}
	return slices.Clone(f.Models), f.Fetched, true
}

// SaveLive stores a fetched list; an empty list forgets it.
func SaveLive(provider, base string, models []Model) error {
	p := LivePath(provider)
	if len(models) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			Touched()
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(liveFile{Fetched: time.Now(), Base: base, Models: models}, "", "  ")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return err
	}
	Touched()
	return nil
}

// Changed, when set, is told that the models magpie offers may be others
// now: a provider added, edited or removed, a vendor's list fetched anew.
// The agent package sets it, to bring the model lists agents keep in files
// of their own up to date.
var Changed func()

// Touched tells Changed, if set.
func Touched() {
	if Changed != nil {
		Changed()
	}
}

// Fetch asks an endpoint for its models. base is an API base URL of any
// flavour (…/v1, …/anthropic, …/api); the usual list paths are tried
// around it. The result keeps the server's order. anthropic sends the
// Anthropic version header, which the official API requires but which a
// dual-protocol relay like OpenRouter reads as a request for its
// Anthropic-flavoured catalog — namespaced, differently named ids.
func Fetch(ctx context.Context, base, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ms, _, err := FetchAt(ctx, base, key, anthropic, headers)
	return ms, err
}

// FetchAt is Fetch, and says which URL answered.
func FetchAt(ctx context.Context, base, key string, anthropic bool, headers map[string]string) ([]Model, string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, "", errors.New("no base URL")
	}
	var urls []string
	add := func(u string) {
		for _, x := range urls {
			if x == u {
				return
			}
		}
		urls = append(urls, u)
	}
	add(base + "/models")
	add(base + "/v1/models")
	root := base
	for _, suffix := range []string{"/anthropic", "/apps/anthropic", "/api/anthropic", "/v1", "/api", "/api/v1"} {
		if strings.HasSuffix(root, suffix) {
			root = strings.TrimSuffix(root, suffix)
		}
	}
	add(root + "/v1/models")
	add(root + "/models")

	var lastErr error
	for _, u := range urls {
		ms, err := fetchOne(ctx, u, key, anthropic, headers)
		if err == nil && len(ms) > 0 {
			return ms, u, nil
		}
		if err != nil {
			lastErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no model list at " + base)
	}
	return nil, "", lastErr
}

// FetchURL asks for the model list at exactly url.
func FetchURL(ctx context.Context, url, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ms, err := fetchOne(ctx, strings.TrimSpace(url), key, anthropic, headers)
	if err == nil && len(ms) == 0 {
		err = errors.New(url + ": no models listed")
	}
	return ms, err
}

func fetchOne(ctx context.Context, url, key string, anthropic bool, headers map[string]string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "magpie")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("x-api-key", key)
	}
	if anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	// The user's own headers, after the defaults so a private auth scheme
	// wins. Written directly so the name keeps the exact case the user typed.
	for k, v := range headers {
		req.Header[k] = []string{v}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	var v struct {
		Data   []liveModel `json:"data"`
		Models []liveModel `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: not a model list", url)
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	var out []Model
	for _, r := range rows {
		id := r.ID
		if id == "" {
			id = r.Name
		}
		if id == "" || !textModel(mdModel{ID: id}) {
			continue
		}
		name := r.DisplayName
		if name == "" {
			name = id
		}
		input := imageInput(r.Modalities.Input)
		m := Model{ID: id, Name: name, ImageInput: input, APIs: EndpointAPIs(r.Endpoints)}
		if input != nil {
			m.Images = *input
		}
		out = append(out, m)
	}
	return out, nil
}

type liveModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Modalities  struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	// the paths the model is served on, where the vendor says: Command
	// Code's Claude models on /messages alone, its open ones on
	// /chat/completions and /responses
	Endpoints []string `json:"supported_endpoints"`
}

// EndpointAPIs names the APIs of a model list's supported_endpoints —
// "chat", "responses", "anthropic" — leaving out any magpie doesn't speak
// (Copilot's websocket one); nil when it names none of them.
func EndpointAPIs(endpoints []string) []string {
	var out []string
	for _, e := range endpoints {
		var api string
		switch strings.TrimPrefix(strings.TrimSuffix(e, "/"), "/v1") {
		case "/chat/completions":
			api = "chat"
		case "/responses":
			api = "responses"
		case "/messages":
			api = "anthropic"
		}
		if api != "" && !slices.Contains(out, api) {
			out = append(out, api)
		}
	}
	return out
}

// Decorate fills in names and reasoning levels for live models from the
// catalog's entry for the same id, keeping the live order.
func Decorate(live []Model, known []Model) []Model {
	byID := make(map[string]Model, len(known))
	for _, m := range known {
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(live))
	for _, m := range live {
		k, ok := byID[m.ID]
		if i := strings.LastIndexByte(m.ID, '/'); !ok && i >= 0 {
			k, ok = byID[m.ID[i+1:]] // a gateway's "deepseek/deepseek-chat"
		}
		if ok {
			if m.Name == "" || m.Name == m.ID {
				m.Name = k.Name
			}
			m.Efforts, m.Released, m.Provider = k.Efforts, k.Released, k.Provider
			if m.ImageInput == nil {
				m.ImageInput = k.ImageInput
				m.Images = m.Images || k.Images
			}
			if m.Context == 0 {
				m.Context = k.Context
			}
			if m.Output == 0 {
				m.Output = k.Output
			}
			if k.Temperature != nil {
				m.Temperature = k.Temperature
			}
		}
		out = append(out, m)
	}
	return out
}
