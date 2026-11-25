package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
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
	} else if m := routerWindow.FindSubmatch(msg); m != nil {
		window, _ = strconv.Atoi(string(m[1]))
		for _, part := range routerInput.FindAllSubmatch(m[2], -1) {
			n, _ := strconv.Atoi(string(part[1]))
			prompt += n
		}
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
var windowAndPrompt = lazyre.New(`(?is)maximum context length is (\d+) tokens.{0,200}?(?:\((\d+) in the messages, \d+ in the completion\)|your prompt contains at least (\d+) input tokens)`)

// routerWindow matches OpenRouter's refusal, which splits the prompt by
// kind and counts the reply in the window: "This endpoint's maximum
// context length is 1048576 tokens. However, you requested about 1050086
// tokens (101187 of text input, 5181 of tool input, 943718 in the output)".
// Group 2 is the parenthesis; routerInput reads each "N of <kind> input".
var routerWindow = lazyre.New(`(?is)maximum context length is (\d+) tokens\..{0,100}?requested about \d+ tokens \(([^)]{0,200}?\d+ in the output)\)`)
var routerInput = lazyre.New(`(?i)(\d+) of [a-z ]{1,20}? input`)

// inputPlusReply is Anthropic's words for the same, which an endpoint
// speaking its API may answer with: "input length and `max_tokens` exceed
// context limit: 197202 + 21333 > 200000".
var inputPlusReply = lazyre.New(`(?i)input length and .{0,3}max_tokens.{0,3} exceed context limit:\s*(\d+)\s*\+\s*\d+\s*>\s*(\d+)`)

// roomedOutput is the reply limit n a request is filled in with, lowered
// to what a window holds with a prompt of about promptTokens: the model's
// own limit may be nearly the whole window (OpenRouter lists 943718 for a
// DeepSeek model of 1048576), and a vendor that counts the reply in the
// window refuses the pair (H1_B1_K1 on X). The prompt is sized from its
// bytes, rounded up; room under leastReplyLeft leaves n as it was, and the
// refusal tells the agent to compact. An unknown window leaves n alone.
func roomedOutput(n, window, promptTokens int) int {
	if n <= 0 || window <= 0 {
		return n
	}
	room := window - promptTokens*5/4 - 1024
	if room >= leastReplyLeft && room < n {
		return room
	}
	return n
}

// contextOf is the context window of p's model, 0 when unknown.
func contextOf(p provider.Provider, model string) int {
	cat := provider.Served()
	for i := range cat {
		e := &cat[i]
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) {
			return e.Context
		}
	}
	return 0
}

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
	return p.VendorHost() == "api.deepseek.com"
}

// withOwnOutput is body, in proto's words, asking p's model for the reply
// its own limit allows (askedOutput) when it names none and p's API would
// otherwise cut it short (shortByDefault). A limit the client named goes
// as it is.
func withOwnOutput(p provider.Provider, model string, proto provider.Protocol, body []byte) []byte {
	if !shortByDefault(p) || namesOutput(proto, body) {
		return body
	}
	n := roomedOutput(askedOutput(p, model), contextOf(p, model), len(body)/4)
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

// thinksByDefault is whether p's API runs a model's thinking on a request
// that names none, and counts it in the reply limit: Tencent Cloud's
// TokenHub and Token Plan serve DeepSeek with thinking "enabled" and
// reasoning_effort "high" unless the request says otherwise, and advise
// max_tokens of at least 2048 in thinking mode (cloud.tencent.com/document/
// product/1823/132248). A memory tool's max_tokens of 4096 was spent on
// reasoning alone, every reply cut at its length with no answer (WaiooT on X).
//
// Google's Gemini does the same on Google's own routes: a Gemini 2.5 or
// later thinks by default (gemini-4-hydrogen and the Pros at high), and
// the reply limit counts its thoughts ("the maximum number of tokens a
// response can generate, including thought tokens", aistudio.google.com/
// docs/thinking). Nowledge Mem's max_tokens of 8192 to Gemini 4 Hydrogen
// at AI Studio was spent on reasoning alone, every reply a 502 with no
// answer (fwxkogaia on X).
func thinksByDefault(p provider.Provider, model string) bool {
	if strings.HasSuffix(p.VendorHost(), "tencentmaas.com") && strings.Contains(strings.ToLower(model), "deepseek") {
		return true
	}
	return googleGemini(p, model) && geminiThinksByDefault(model)
}

// googleGemini is whether p serves model as Google serves it: one of
// Google's hosts (AI Studio's OpenAI-compatible and Gemini APIs, Vertex
// AI's, Code Assist's), Vertex AI, or a Google sign-in. OpenRouter's Gemini
// isn't: it holds a request's whole max_tokens against the balance ("can
// only afford"), so a raised limit would turn a small balance away. Nor
// is a relay, a proxy on this computer or the LAN included (which
// geminiCompat reads as AI Studio's for the request's shape): it may hold
// or cap the limit too, and its void is asked again for room (voidRoom).
func googleGemini(p provider.Provider, model string) bool {
	if !strings.Contains(strings.ToLower(model), "gemini") {
		return false
	}
	h := strings.ToLower(p.VendorHost())
	return h == "googleapis.com" || strings.HasSuffix(h, ".googleapis.com") ||
		p.IsVertex() || googleAccount(p) || googlePlugin(p)
}

// geminiThinksByDefault is whether a Gemini model thinks at length on a
// request that asks nothing of its thinking: 2.5 and later, but not a
// Flash-Lite (2.5's doesn't think unless asked, 3's thinks at minimal) or
// an image model; and the -latest aliases, which name a current one.
func geminiThinksByDefault(model string) bool {
	m := strings.ToLower(strings.TrimPrefix(model, "models/"))
	if strings.Contains(m, "lite") || strings.Contains(m, "image") || catalog.DrawsID(m) {
		return false
	}
	return geminiGeneration(m) >= 3 || strings.Contains(m, "gemini-2.5") ||
		strings.HasSuffix(m, "gemini-pro-latest") || strings.HasSuffix(m, "gemini-flash-latest")
}

// namesReasoning is whether body, in proto's words, says how the model is
// to reason (or not to): the client's own choice, which goes as it is.
func namesReasoning(proto provider.Protocol, body []byte) bool {
	switch proto {
	case provider.Chat:
		// extra_body's google.thinking_config is Gemini's own, through
		// its OpenAI-compatible API
		for _, k := range []string{"reasoning_effort", "thinking", "enable_thinking", "chat_template_kwargs", "reasoning", "extra_body.google.thinking_config", "thinking_config"} {
			if gjson.GetBytes(body, k).Exists() {
				return true
			}
		}
		return false
	case provider.Responses:
		return gjson.GetBytes(body, "reasoning").Exists()
	case provider.Anthropic:
		return gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "output_config.effort").Exists()
	}
	return true
}

// withThinkingRoom is body, in proto's words, with the reply limit a client
// named raised by the room a default-thinking model (thinksByDefault) needs
// to think in before it answers (replyRoom at its default effort, high),
// when the client asked for no reasoning of its own, the user set no Max
// output for the model, and the limit is a small one (at most
// thinkingRoomBelow). The model's own limit, where known, still caps it.
func withThinkingRoom(p provider.Provider, model string, proto provider.Protocol, body []byte) []byte {
	if !thinksByDefault(p, model) || namesReasoning(proto, body) || userOutput(p, model) > 0 {
		return body
	}
	var field string
	switch proto {
	case provider.Chat:
		field = "max_tokens"
		if !gjson.GetBytes(body, field).Exists() {
			field = "max_completion_tokens"
		}
	case provider.Responses:
		field = "max_output_tokens"
	case provider.Anthropic:
		field = "max_tokens"
	default:
		// a Gemini client's maxOutputTokens is in Gemini's own words,
		// which count the thoughts: its own choice
		return body
	}
	n := int(gjson.GetBytes(body, field).Int())
	if n <= 0 || n > thinkingRoomBelow {
		return body
	}
	n = replyRoom(n, "high")
	if own := askedOutput(p, model); own > 0 && n > own {
		n = own
	}
	return withFields(body, map[string]any{field: n})
}

// thinkingRoomBelow is the largest reply limit a request names that is
// raised for thinking: a client that asked for more has made room.
const thinkingRoomBelow = 16384

// voidRoomUnknown is the reply limit a model whose own isn't known is
// asked for once its API's default cut a reply that said nothing
// (voidRoom): what the OpenAI-compatible APIs of today's reasoning
// models take, four times the 8192 a relay's Gemini default cut at.
const voidRoomUnknown = 32768

// voidRoom is the reply limit to ask p's model for, a request naming
// none, after its API's own default cut a reply having spent spent
// tokens on nothing but reasoning (cutVoid): the model's own limit
// (askedOutput) when it is more than was spent, else voidRoomUnknown
// when the model's limit isn't known and that is more. 0 when there is
// no more room to ask for: the model's own was what cut it.
//
// JNDliuliuM on X (v0.1.4613): Hermes's subagents, on a Gemini model at
// a relay's Chat endpoint, named no max_tokens, as Hermes doesn't for a
// custom provider; the relay cut every reply at 8192 tokens of reasoning,
// and thirteen 502s later three subagents had failed their tasks.
func voidRoom(p provider.Provider, model string, spent int) int {
	own := askedOutput(p, model)
	switch {
	case own > spent:
		return own
	case own <= 0 && spent < voidRoomUnknown:
		return voidRoomUnknown
	}
	return 0
}

// voidRoomKey is the key of p's model in voidRooms.
func voidRoomKey(providerID, model string) string { return providerID + "\x00" + model }

// noteVoid remembers, of a reply from p's model cut at its length having
// said nothing on a request that named no reply limit, the limit to ask
// that model for from now on when a request names none (voidRoom); the
// API's default is the same every ask. It says whether there is one.
func (s *Server) noteVoid(p provider.Provider, model string, spent int) bool {
	n := voidRoom(p, model, spent)
	if n <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.voidRooms == nil {
		s.voidRooms = make(map[string]int)
	}
	k := voidRoomKey(p.ID, model)
	if s.voidRooms[k] < 0 {
		// its API turned the room asked for away (forgetVoidRoom)
		return false
	}
	s.voidRooms[k] = n
	return true
}

// voidRoomOf is the reply limit p's model is asked for when a request
// names none (noteVoid), 0 when its API's default never cut it void.
func (s *Server) voidRoomOf(providerID, model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(s.voidRooms[voidRoomKey(providerID, model)], 0)
}

// forgetVoidRoom drops the limit noteVoid remembered for p's model, its
// API having turned it away as too large: a request naming none is sent
// none there again, and no void of the model's asks for more.
func (s *Server) forgetVoidRoom(providerID, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.voidRooms == nil {
		s.voidRooms = make(map[string]int)
	}
	s.voidRooms[voidRoomKey(providerID, model)] = -1
}

// withVoidRoom is body, in proto's words, asking p's model for the room
// noteVoid remembered when it names no reply limit of its own (the
// window it shares with the prompt allowing), and whether it was. A
// limit the client named goes as it is: its own choice.
func (s *Server) withVoidRoom(p provider.Provider, model string, proto provider.Protocol, body []byte) ([]byte, bool) {
	if namesOutput(proto, body) {
		return body, false
	}
	n := roomedOutput(s.voidRoomOf(p.ID, model), contextOf(p, model), len(body)/4)
	if n <= 0 {
		return body, false
	}
	if b, ok := withReplyCap(proto, body, n); ok {
		return b, true
	}
	return body, false
}
