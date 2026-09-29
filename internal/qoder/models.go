package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
// model an agent can pick.
func ParseModels(body []byte) ([]catalog.Model, error) {
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
		out = append(out, catalog.Model{ID: m.Key, Name: name, Provider: ProviderKey,
			Context: m.MaxInputTokens, Images: m.IsVL, Efforts: m.Efforts})
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
