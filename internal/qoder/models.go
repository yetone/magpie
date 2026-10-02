package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// ModelInfo is one entry of the model-list response's "chat" array. Only the
// fields magpie uses are kept.
type ModelInfo struct {
	Key            string          `json:"key"`
	Source         string          `json:"source,omitempty"`
	Enable         bool            `json:"enable"`
	DisplayName    string          `json:"display_name,omitempty"`
	IsVL           bool            `json:"is_vl,omitempty"`
	IsReasoning    bool            `json:"is_reasoning,omitempty"`
	MaxInputTokens int             `json:"max_input_tokens,omitempty"`
	Efforts        []string        `json:"reasoning_efforts,omitempty"`
	Config         json.RawMessage `json:"-"`
	// From thinking_config: whether the model can think, whether it can't
	// stop, and the effort it thinks at unless asked for another.
	Thinks        bool   `json:"-"`
	AlwaysThinks  bool   `json:"-"`
	DefaultEffort string `json:"-"`
	// Free is set on a model that costs the plan no credits, as Qoder's
	// client reads it: is_free, or a price_factor of 0.
	Free bool `json:"-"`
}

// free reads whether the listing marks a model free: is_free true, or a
// price_factor (the credits a request costs, as a multiple) of 0. Either
// may come in snake or camel case.
func (m *ModelInfo) free(raw json.RawMessage) {
	var v struct {
		IsFree      *bool    `json:"is_free"`
		IsFreeC     *bool    `json:"isFree"`
		PriceFactor *float64 `json:"price_factor"`
		PriceC      *float64 `json:"priceFactor"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	if v.IsFree == nil {
		v.IsFree = v.IsFreeC
	}
	if v.PriceFactor == nil {
		v.PriceFactor = v.PriceC
	}
	m.Free = v.IsFree != nil && *v.IsFree || v.PriceFactor != nil && *v.PriceFactor == 0
}

// effortOrder ranks Qoder's effort names, lowest first.
var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// thinking reads the listing's thinking_config, where Qoder keeps the
// efforts a model offers and its default; is_reasoning alone stands for a
// model listed without one.
func (m *ModelInfo) thinking(raw json.RawMessage) {
	var v struct {
		Config *struct {
			Disabled json.RawMessage `json:"disabled"`
			Enabled  *struct {
				Efforts map[string]struct {
					IsDefault bool `json:"is_default"`
				} `json:"efforts"`
			} `json:"enabled"`
		} `json:"thinking_config"`
	}
	m.Thinks = m.IsReasoning
	if json.Unmarshal(raw, &v) != nil || v.Config == nil {
		return
	}
	m.Thinks = v.Config.Enabled != nil
	m.AlwaysThinks = m.Thinks && len(v.Config.Disabled) == 0
	m.Efforts = nil
	if !m.Thinks {
		return
	}
	for name, e := range v.Config.Enabled.Efforts {
		m.Efforts = append(m.Efforts, name)
		if e.IsDefault {
			m.DefaultEffort = name
		}
	}
	rank := func(s string) int {
		for i, e := range effortOrder {
			if e == s {
				return i
			}
		}
		return len(effortOrder)
	}
	sort.Slice(m.Efforts, func(i, j int) bool {
		if rank(m.Efforts[i]) != rank(m.Efforts[j]) {
			return rank(m.Efforts[i]) < rank(m.Efforts[j])
		}
		return m.Efforts[i] < m.Efforts[j]
	})
}

// FetchModels retrieves the raw model listing for a signed-in user. The
// request carries the same COSY envelope the client uses for every /algo
// call; an empty body is signed for a GET.
func FetchModels(ctx context.Context, client *http.Client, baseURL string, user *User) ([]byte, error) {
	if client == nil {
		client = &http.Client{}
	}
	if user == nil || strings.TrimSpace(user.Token) == "" || strings.TrimSpace(user.UID) == "" {
		return nil, fmt.Errorf("qoder: uid and token are required to fetch models")
	}
	url := strings.TrimRight(baseURL, "/") + ListModelsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder: build models request: %w", err)
	}
	cosy, err := BuildCosyHeaders(url, user, "", 0)
	if err != nil {
		return nil, fmt.Errorf("qoder: build cosy headers: %w", err)
	}
	for k, v := range cosy {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder models request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("qoder models: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qoder models: HTTP %d: %s", resp.StatusCode, sanitize(body))
	}
	return body, nil
}

func sanitize(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 512 {
		s = s[:512] + "..."
	}
	return s
}

// ParseModels decodes the listing and returns the enabled, routable chat
// models as magpie's catalog entries. Placeholder ids ("auto", "default")
// are dropped: they route between models inside Qoder and aren't a single
// model an agent can pick. provider is the magpie provider id the entries are
// listed under (ProviderKey or CNProviderKey).
func ParseModels(body []byte, provider string) ([]catalog.Model, error) {
	models, err := ModelConfigs(body)
	if err != nil {
		return nil, err
	}
	var out []catalog.Model
	for _, m := range models {
		name := m.DisplayName
		if name == "" {
			name = m.Key
		}
		out = append(out, catalog.Model{ID: m.Key, Name: name, Provider: provider,
			Context: m.MaxInputTokens, Images: m.IsVL, Efforts: m.Efforts, Free: m.Free})
	}
	return out, nil
}

// ModelConfigs preserves each enabled model's complete upstream configuration.
func ModelConfigs(body []byte) ([]ModelInfo, error) {
	var resp struct {
		Chat []json.RawMessage `json:"chat"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("qoder: invalid models json: %w", err)
	}
	var out []ModelInfo
	for _, raw := range resp.Chat {
		var m ModelInfo
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		if !m.Enable || !IsRoutableModel(m.Key) {
			continue
		}
		m.Config = raw
		m.thinking(raw)
		m.free(raw)
		out = append(out, m)
	}
	return out, nil
}

// IsRoutableModel reports whether a model key names one model an agent can
// pick. Aggregate entries ("auto", "default") route inside Qoder and aren't.
func IsRoutableModel(key string) bool {
	switch strings.TrimSpace(key) {
	case "", "auto", "default":
		return false
	}
	return true
}
