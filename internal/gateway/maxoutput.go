package gateway

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/provider"
)

// outputLimit is the most a reply of a provider's model may hold, where
// known: its catalog entry's (the user's setting, else the vendor's list
// or models.dev).
func outputLimit(p provider.Provider, model string) int {
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.Output > 0 {
			return e.Output
		}
	}
	return 0
}

// withMaxOutput is body, in proto's words, asking for a reply of at most
// limit tokens: a request that asks for more is lowered to limit, and
// Anthropic's thinking budget, which must be under it, with it. A request
// that asks for limit or less, or names no limit, goes as it is.
func withMaxOutput(proto provider.Protocol, body []byte, limit int) []byte {
	var v struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
		MaxOutputTokens     int `json:"max_output_tokens"`
		Thinking            *struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	if limit <= 0 || json.Unmarshal(body, &v) != nil {
		return body
	}
	fields := map[string]any{}
	switch proto {
	case provider.Chat:
		if v.MaxCompletionTokens > limit {
			fields["max_completion_tokens"] = limit
		}
		if v.MaxTokens > limit {
			fields["max_tokens"] = limit
		}
	case provider.Responses:
		if v.MaxOutputTokens > limit {
			fields["max_output_tokens"] = limit
		}
	case provider.Anthropic:
		if v.MaxTokens <= limit {
			return body
		}
		fields["max_tokens"] = limit
		if t := v.Thinking; t != nil && t.Type == "enabled" && t.Budget >= limit {
			if limit-1 >= 1024 {
				fields["thinking"] = map[string]any{"type": "enabled", "budget_tokens": limit - 1}
			} else {
				// no budget fits under it (Anthropic's least is 1024)
				fields["thinking"] = map[string]any{"type": "disabled"}
			}
		}
	case provider.Gemini:
		if n, ok := v.GenerationConfig["maxOutputTokens"].(float64); ok && int(n) > limit {
			v.GenerationConfig["maxOutputTokens"] = limit
			fields["generationConfig"] = v.GenerationConfig
		}
	}
	if len(fields) == 0 {
		return body
	}
	return withFields(body, fields)
}
