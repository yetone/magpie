package gateway

import (
	"bytes"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// thinkingBackRefusal is DeepSeek's model asking for an earlier turn's
// thinking, wherever it is served: on its Messages API "The
// `content[].thinking` in the thinking mode must be passed back to the
// API", on its Chat API the same of `reasoning_content` (Kimi's says it
// without the backticks). Checked against api.deepseek.com on 2026-11-02:
// an assistant turn of the tool loop being continued with no thinking is
// refused, one with a space is taken, as are finished turns and plain
// replies without.
var thinkingBackRefusal = lazyre.New("(?i)`?(?:content\\[\\]\\.thinking|reasoning_content)`? in the thinking mode must be passed back")

// reasoningBackWords are a model asking for an earlier assistant
// message's reasoning in other words than thinkingBackRefusal's:
// Reflection's Beam on Command Code, 400 "Missing required parameter:
// 'messages[1].reasoning_content'" for a second turn whose first reply
// has none (EDwinpzy on Discord, magpie 0.1.2717), and the like that name
// reasoning_content as missing or required. A request missing what every
// API requires ("Missing required parameter: 'messages'") isn't one.
var reasoningBackWords = lazyre.New(`(?i)\breasoning_content\b[^"]{0,40}\b(?:is (?:missing|required)|must be (?:passed|provided|included))|\b(?:missing|required)\b[^"]{0,80}\breasoning_content\b`)

// reasoningBackRefused says a model turned the request away for an
// earlier turn's reasoning it didn't have: a demand of that model's on the
// history, which another member's model, that asks nothing of the kind,
// may well take as it is (shapeRefused).
func reasoningBackRefused(body []byte) bool {
	return thinkingBackRefusal.Match(body) || reasoningBackWords.Match(body)
}

// thinkingBackNeeded is how unfit remembers a provider's model asking
// for its thinking back on every tool turn (thinkingBackRefusal).
func thinkingBackNeeded(model string) string { return "thinking back\x00" + model }

// withThinkingBack is body with each tool turn's thinking filled in
// (withThinkingOnToolTurns, withReasoningOnToolTurns), for a provider's
// model that asked for it before though neither its host nor its name
// says DeepSeek: a relay, or a model the user named otherwise. Those
// two are filled when the body is built (anthropicReplaysThinking,
// chatReplaysReasoning), so this is the learned rest of the class.
func (s *Server) withThinkingBack(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Anthropic && to != provider.Chat || s.fits(p.ID, thinkingBackNeeded(modelOf(body)), to) {
		return body
	}
	return fillThinkingBack(to, body)
}

func fillThinkingBack(to provider.Protocol, body []byte) []byte {
	if to == provider.Anthropic {
		return withThinkingOnToolTurns(body)
	}
	return withReasoningOnToolTurns(body)
}

// refuseThinkingBack reads a 400 from a Messages or Chat upstream: when
// it is thinkingBackRefusal and filling body's tool turns changes it,
// the provider's model is remembered as asking for it, and true says
// to ask once more (forwardOnce fills them from then on). res's body
// is left readable either way.
func (s *Server) refuseThinkingBack(p provider.Provider, to provider.Protocol, body []byte, res *http.Response) bool {
	if to != provider.Anthropic && to != provider.Chat || !badRequest(res.StatusCode) {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	model := modelOf(body)
	if !thinkingBackRefusal.Match(wordsOf(b)) || !s.fits(p.ID, thinkingBackNeeded(model), to) {
		return false
	}
	if bytes.Equal(fillThinkingBack(to, body), body) {
		return false
	}
	s.markUnfit(p.ID, thinkingBackNeeded(model), to)
	return true
}
