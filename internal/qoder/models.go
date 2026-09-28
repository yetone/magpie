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
	Key            string  `json:"key"`
	Format         string  `json:"format,omitempty"`
	Source         string  `json:"source,omitempty"`
	Enable         bool    `json:"enable"`
	DisplayName    string  `json:"display_name,omitempty"`
	IsVL           bool    `json:"is_vl,omitempty"`
	IsReasoning    bool    `json:"is_reasoning,omitempty"`
	IsDefault      bool    `json:"is_default,omitempty"`
	PriceFactor    float64 `json:"price_factor,omitempty"`
	MaxInputTokens int     `json:"max_input_tokens,omitempty"`
}

// FetchModels retrieves the raw model listing for a signed-in user. The
// request carries the same COSY envelope the client uses for every /algo
// call; an empty body is signed for a GET.
func FetchModels(ctx context.Context, client *http.Client, baseURL, uid, token string) ([]byte, error) {
	if client == nil {
		client = &http.Client{}
	}
	if strings.TrimSpace(token) == "" || strings.TrimSpace(uid) == "" {
		return nil, fmt.Errorf("qoder: uid and token are required to fetch models")
	}
	url := strings.TrimRight(baseURL, "/") + ListModelsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder: build models request: %w", err)
	}
	cosy, err := BuildCosyHeaders(url, &User{UID: uid, Token: token}, "", 0)
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
	var resp struct {
		Chat []ModelInfo `json:"chat"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("qoder: invalid models json: %w", err)
	}
	var out []catalog.Model
	for _, m := range resp.Chat {
		if !m.Enable || !IsRoutableModel(m.Key) {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Key
		}
		cm := catalog.Model{ID: m.Key, Name: name, Provider: ProviderKey, Context: m.MaxInputTokens,
			Images: m.IsVL}
		out = append(out, cm)
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
