package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// outputLimit is the most a reply of a provider's model may hold, where
// known: its catalog entry's (the user's setting, else the vendor's list
// or models.dev).
func outputLimit(p provider.Provider, model string) int {
	cat := provider.Served()
	for i := range cat {
		e := &cat[i] // an Entry is ~1.3KB, the catalog hundreds
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.Output > 0 {
			return e.Output
		}
	}
	return 0
}

// userOutput is the Max output the user set for p's model (provider ›
// model, or the provider's "*"), 0 when they set none. A request asking
// for more is lowered to it on any route, not only in a group: Claude Code
// asks claude-* models for their full 128000 and isn't told magpie's
// figure for them (claudeOutput), so a key whose quota is held per request
// as max_tokens times the price turned every turn away, and the field the
// editor says caps max_tokens did nothing (YaCo on Discord).
func userOutput(p provider.Provider, model string) int {
	cat := provider.Served()
	for i := range cat {
		e := &cat[i] // an Entry is ~1.3KB, the catalog hundreds
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.OutputSet && e.Output > 0 {
			return e.Output
		}
	}
	return 0
}

// askedOutput is the reply limit a request that names none is sent on
// Anthropic's Messages API, which needs one: the model's own as the agents
// are told it (the user's Max output, else the vendor's list or
// models.dev, within the window), or 0 when none is known. A Codex turn
// names none, and was sent a flat 16384, which a long thinking and tool
// turn ran out of, Codex showing "Incomplete response returned, reason:
// max_output_tokens" (#1792, tensornull).
func askedOutput(p provider.Provider, model string) int {
	cat := provider.Served()
	for i := range cat {
		e := &cat[i] // an Entry is ~1.3KB, the catalog hundreds
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) {
			return e.PublishedOutput()
		}
	}
	return 0
}

// defaultMaxTokens is what a request naming no reply limit is sent on
// Anthropic's API for a model whose limit isn't known.
const defaultMaxTokens = 16384

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

// replyLeft reads, from a refusal that counts the reply asked for in the
// window (DeepSeek's and vLLM's "This model's maximum context length is
// 1000000 tokens. However, you requested 1100000 tokens (706784 in the
// messages, 393216 in the completion)"), the tokens the window has left
// for a reply after the prompt; 0 when the error isn't one of those, or
// the prompt alone fills the window.
func replyLeft(msg []byte) int {
	window, prompt := 0, 0
	if m := windowAndPrompt.FindSubmatch(msg); m != nil {
		window, _ = strconv.Atoi(string(m[1]))
		prompt, _ = strconv.Atoi(string(m[2]))
		if m[2] == nil {
			prompt, _ = strconv.Atoi(string(m[3]))
		}
	} else if m := inputPlusReply.FindSubmatch(msg); m != nil {
		prompt, _ = strconv.Atoi(string(m[1]))
		window, _ = strconv.Atoi(string(m[2]))
	}
	if window <= 0 || prompt <= 0 || prompt >= window {
		return 0
	}
	return window - prompt
}

// windowAndPrompt matches the window and the prompt's part of a refusal
// that adds the reply's allowance to the prompt: "(N in the messages, M in
// the completion)", or vLLM's newer "your prompt contains at least N input
// tokens".
var windowAndPrompt = regexp.MustCompile(`(?is)maximum context length is (\d+) tokens.{0,200}?(?:\((\d+) in the messages, \d+ in the completion\)|your prompt contains at least (\d+) input tokens)`)

// inputPlusReply is Anthropic's words for the same, which an endpoint
// speaking its API may answer with: "input length and `max_tokens` exceed
// context limit: 197202 + 21333 > 200000".
var inputPlusReply = regexp.MustCompile(`(?i)input length and .{0,3}max_tokens.{0,3} exceed context limit:\s*(\d+)\s*\+\s*\d+\s*>\s*(\d+)`)

// leastReplyLeft is the least room left for a reply that is worth asking
// for: less, and the conversation needs compacting, which the refusal
// tells the agent as it is (DeepSeek's own default reply is 8K).
const leastReplyLeft = 8192

// withReplyCap is body asking for a reply of at most limit tokens, in
// proto's words, whether or not it named a limit before: a request that
// named none was given the vendor's own, which was too large. false when
// it asked for no more already, or proto keeps no such field magpie sets.
func withReplyCap(proto provider.Protocol, body []byte, limit int) ([]byte, bool) {
	if limit <= 0 {
		return nil, false
	}
	out := withMaxOutput(proto, body, limit)
	if !bytes.Equal(out, body) {
		return out, true
	}
	var key string
	switch proto {
	case provider.Chat:
		if gjson.GetBytes(body, "max_tokens").Exists() || gjson.GetBytes(body, "max_completion_tokens").Exists() {
			return nil, false
		}
		key = "max_tokens"
	case provider.Responses:
		if gjson.GetBytes(body, "max_output_tokens").Exists() {
			return nil, false
		}
		key = "max_output_tokens"
	case provider.Gemini:
		gc := gjson.GetBytes(body, "generationConfig")
		if gc.Get("maxOutputTokens").Exists() {
			return nil, false
		}
		m := map[string]any{}
		if gc.IsObject() {
			dec := json.NewDecoder(strings.NewReader(gc.Raw))
			dec.UseNumber()
			if dec.Decode(&m) != nil {
				return nil, false
			}
		}
		m["maxOutputTokens"] = limit
		return withFields(body, map[string]any{"generationConfig": m}), true
	default:
		// Anthropic's always names one
		return nil, false
	}
	return withFields(body, map[string]any{key: limit}), true
}

// namesOutput is whether body, in proto's words, names a reply limit.
func namesOutput(proto provider.Protocol, body []byte) bool {
	switch proto {
	case provider.Chat:
		return gjson.GetBytes(body, "max_tokens").Exists() || gjson.GetBytes(body, "max_completion_tokens").Exists()
	case provider.Responses:
		return gjson.GetBytes(body, "max_output_tokens").Exists()
	case provider.Anthropic:
		return gjson.GetBytes(body, "max_tokens").Exists()
	case provider.Gemini:
		return gjson.GetBytes(body, "generationConfig.maxOutputTokens").Exists()
	}
	return true
}

// shortByDefault is whether p's API cuts a reply that names no limit far
// short of what its model can give: DeepSeek's gives 8K without thinking
// and 64K with it (api-docs.deepseek.com, create-chat-completion:
// "When not set, the default is 8K in non-thinking mode, 64K in thinking
// mode"), on /v1/responses too, where its models go to 384K. Codex names
// no max_output_tokens, so its turns there stopped at 8192 tokens, "Incomplete
// response returned, reason: max_output_tokens" (xuanwo on X). Checked
// against api.deepseek.com on 2026-10-27: deepseek-flash, effort none, no
// limit: incomplete at 8192; max_output_tokens 393216: completed at 17602.
func shortByDefault(p provider.Provider) bool {
	return p.Host() == "api.deepseek.com"
}

// withOwnOutput is body, in proto's words, asking p's model for the reply
// its own limit allows (askedOutput) when it names none and p's API would
// otherwise cut it short (shortByDefault). A limit the client named goes
// as it is.
func withOwnOutput(p provider.Provider, model string, proto provider.Protocol, body []byte) []byte {
	if !shortByDefault(p) || namesOutput(proto, body) {
		return body
	}
	n := askedOutput(p, model)
	if n <= 0 {
		return body
	}
	if b, ok := withReplyCap(proto, body, n); ok {
		return b
	}
	return body
}

// fillsOutput is whether a request translated for p's API on to that
// names no reply limit is sent its model's own (askedOutput): the client's
// API reads none as the model's whole, which another vendor's needn't
// (DeepSeek's reads it as 8K, shortByDefault). Anthropic's always is, as
// it needs one. An account's own backend (the ChatGPT one turns
// max_output_tokens away) isn't. Another magpie is: this fills only a
// translated turn (forwardTranslated), and one translated here goes to
// the other magpie with its tools converted, a body it passes whole —
// its own filling is translation work it never does (#5566), so its
// vendor's short default was all a cascade got.
func fillsOutput(p provider.Provider, to provider.Protocol) bool {
	if to == provider.Anthropic {
		return true
	}
	return (to == provider.Chat || to == provider.Responses) &&
		(p.Account == nil || p.IsPlugin())
}

// outputFieldRefused is a request error about the reply limit itself
// ("max_tokens must be at most 32768"), not the window it shares with the
// prompt, which replyLeft reads and the request is asked again for.
func outputFieldRefused(status int, body []byte) bool {
	if !badRequest(status) {
		return false
	}
	w := wordsOf(body)
	if replyLeft(w) > 0 {
		return false
	}
	m := strings.ToLower(string(w))
	return strings.Contains(m, "max_tokens") || strings.Contains(m, "max_output_tokens") ||
		strings.Contains(m, "max_completion_tokens") || strings.Contains(m, "maxoutputtokens")
}

// outputFillRefused names, for markUnfit, a model whose vendor turned
// away the reply limit magpie filled in: a request naming none is sent
// none there from then on, as before.
func outputFillRefused(model string) string { return "filled-output:" + model }
