package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// KiloVersion is the Kilo CLI release magpie says it is to the Kilo
// Gateway.
const KiloVersion = "7.8.3"

// KiloClient makes a request to the Kilo Gateway carry what the Kilo CLI
// sends it (Kilo-Org/kilocode packages/opencode/src/session/llm/request.ts
// and kilocode/const.ts, packages/kilo-gateway/src/headers.ts): Kilo
// Code's attribution and User-Agent, the editor it runs in, its mode, and
// the session as the task, which the gateway keys its prompt cache on.
// session names the conversation, made into one of the CLI's ids (ses_…)
// as for OpenCode; none leaves the session headers out. A provider with no
// key sends no Authorization at all: the gateway answers a free model to a
// caller with no credential as anonymous (by IP, 200 requests an hour), and
// refuses a bearer it can't verify with 401 (Kilo-Org/cloud
// web-shared/src/lib/tokens.ts, ai-gateway/handlers/llm-proxy.ts). Kilo's
// clients still send "Bearer anonymous", which the gateway reads as none
// only until no released client sends it, so it is not copied here.
func KiloClient(h http.Header, key, session string) {
	h.Set("HTTP-Referer", "https://kilocode.ai")
	h.Set("X-Title", "Kilo Code")
	h.Set("User-Agent", "Kilo-Code/"+KiloVersion)
	h.Set("X-KILOCODE-EDITORNAME", "Kilo CLI "+KiloVersion)
	if session != "" {
		id := openCodeID("ses", session)
		h.Set("x-kilocode-mode", "code")
		h.Set("X-KILOCODE-TASKID", id)
		h.Set("x-session-affinity", id)
		h.Set("X-Session-Id", id)
	}
	if key == "" {
		h.Del("Authorization")
	}
}

// IsKilo reports whether the provider is the Kilo Gateway: made from its
// preset, or at Kilo's API host.
func (p Provider) IsKilo() bool {
	if p.Preset == "kilo" {
		return true
	}
	h := p.Host()
	return h == "api.kilo.ai" || h == "api.kilocode.ai"
}

// kiloModels is the gateway's list as Kilo's clients ask it (GET
// {base}/models, kilo-gateway src/api/models.ts): OpenRouter's shape, with
// isFree on the models it serves at no cost. Without a key only those are
// listed, the rest wanting a Kilo account; with one, all of them, the free
// ones marked. A model that says it takes no tools is left out, as Kilo
// leaves it out.
func (p Provider) kiloModels(ctx context.Context) ([]catalog.Model, string, error) {
	base := strings.TrimRight(strings.TrimSpace(p.Chat), "/")
	u := base + "/models"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ms []catalog.Model
	err := catalog.Paged(u, func(at string) (bool, string, error) {
		page, more, last, err := p.kiloModelsPage(ctx, at)
		if err != nil {
			return false, "", err
		}
		ms = append(ms, page...)
		return more, last, nil
	})
	if err != nil {
		return nil, base, err
	}
	if len(ms) == 0 {
		return nil, base, errors.New(u + ": no models listed")
	}
	return ms, base, nil
}

// kiloModelsPage asks the gateway's list at at, one page of it, and says
// whether another page follows, and the id it starts after. The reply's
// shape is OpenRouter's, which pages with has_more and last_id, so the
// pages are followed to the end of the list (catalog.Paged) rather than
// refused: a page read as the whole list drops every model of the pages
// after from the user's picks (#904). As far as was measured on 2026-10-06
// the gateway answers with the whole list either way — 400 models and no
// has_more at api.kilo.ai/api/openrouter/models, 466 and links.next: null
// at OpenRouter's own /api/v1/models — so this is the shape's rule kept in
// one place, for the day either of them does answer with a page.
func (p Provider) kiloModelsPage(ctx context.Context, at string) ([]catalog.Model, bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, at, nil)
	if err != nil {
		return nil, false, "", err
	}
	req.Header.Set("Accept", "application/json")
	KiloClient(req.Header, p.Key, "")
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	for k, v := range p.Headers {
		req.Header[k] = []string{v}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, false, "", errorf("%s: HTTP %d", at, res.StatusCode)
	}
	var list struct {
		Data []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			ContextLength any      `json:"context_length"`
			MaxOutput     any      `json:"max_completion_tokens"`
			Params        []string `json:"supported_parameters"`
			IsFree        bool     `json:"isFree"`
			TopProvider   struct {
				MaxOutput any `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Architecture struct {
				Input  []string `json:"input_modalities"`
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
		HasMore bool   `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, false, "", errorf("%s: not a model list", at)
	}
	num := func(v any) int {
		if n, ok := v.(float64); ok && n > 0 {
			return int(n)
		}
		return 0
	}
	var ms []catalog.Model
	for _, r := range list.Data {
		if r.ID == "" || len(r.Params) > 0 && !slices.Contains(r.Params, "tools") {
			continue
		}
		if out := r.Architecture.Output; len(out) > 0 && !slices.Contains(out, "text") {
			continue // one that only draws or makes sound
		}
		free := r.IsFree || isKiloFree(r.ID)
		if p.Key == "" && !free {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		m := catalog.Model{ID: r.ID, Name: name, Free: free, Context: num(r.ContextLength),
			Reasoning: slices.Contains(r.Params, "reasoning")}
		if m.Output = num(r.TopProvider.MaxOutput); m.Output == 0 {
			m.Output = num(r.MaxOutput)
		}
		if in := r.Architecture.Input; in != nil {
			yes := slices.Contains(in, "image")
			m.ImageInput, m.Images = &yes, yes
		}
		if free {
			m.Price = &catalog.Price{}
		}
		ms = append(ms, m)
	}
	return ms, list.HasMore, list.LastID, nil
}

// isKiloFree reports whether id is one of the gateway's free models by its
// id alone: a ":free" one, which its docs say anyone may ask, or Kilo's
// own free router.
func isKiloFree(id string) bool {
	return strings.HasSuffix(id, ":free") || id == "kilo-auto/free"
}

// kiloFreeModel reports whether model is one of the Kilo Gateway's free
// models on p: one the fetched list marks free, else one free by its id.
func (p Provider) kiloFreeModel(model string) bool {
	if !p.IsKilo() {
		return false
	}
	if ms, _, ok := p.live(); ok {
		for _, m := range ms {
			if m.ID == model {
				return m.Free
			}
		}
	}
	return isKiloFree(model)
}
