package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Claude's thinking block is signed for the conversation it was made in
// and the account that made it. A block an upstream doesn't take as its
// own is refused with the whole request (ferock on Discord, Command Code's
// plugin): 400 "messages.3.content.20: Invalid signature in thinking
// block. The block is bound to a different conversation. Remove the
// block, or set thinking.block_binding.prefix_mismatch_behavior to
// "drop_block"" (that setting needs a beta magpie doesn't send for an
// agent). Such a block comes from a conversation that moved between
// accounts or providers (a routing group's fallback, an account switch,
// a relay that serves Claude from more than one account of its own) or
// whose earlier turns changed since.
//
// forward does what the refusal asks: it leaves out the block it names
// and every thinking block after it (their signatures cover the turns
// before them, the left-out block among them), asks once more, and
// remembers those signatures for that provider and account
// (thinkingRefused), so the conversation's later turns go without them
// the first time and stay the conversation the model has gone on in.
// Other blocks, and every request to another provider or account, go as
// the agent wrote them. Only Claude's refusal text starts it, so no other
// vendor's request changes.

// boundRefusal is Claude's refusal of a thinking block it didn't sign for
// this conversation or account: the signature's wording, the newer
// binding's, and a redacted_thinking block's.
var boundRefusal = regexp.MustCompile("(?i)invalid `?signature`? in `?thinking`? block|bound to a different conversation|not created in this conversation|invalid data in `?redacted_thinking`? block")

// boundAt is where the refusal says the block is: messages.<m>.content.<b>.
var boundAt = regexp.MustCompile(`messages\.(\d+)\.content\.(\d+)`)

// thinkingRefusedMost bounds the signatures remembered; past it the
// memory starts again, and a conversation still going is mended by one
// more refusal.
const thinkingRefusedMost = 1 << 14

// thinkingOwner is who a refusal is remembered for: the provider, and the
// account signed in to it.
func thinkingOwner(p provider.Provider) string {
	if p.Account != nil {
		return p.ID + "\x00" + p.Account.User
	}
	return p.ID
}

// thinkingKey names a thinking or redacted_thinking block by its
// signature (a redacted block's data), or its text where it has neither.
func thinkingKey(b gjson.Result) string {
	k := b.Get("signature").String()
	if k == "" {
		k = b.Get("data").String()
	}
	if k == "" {
		k = b.Get("thinking").String()
	}
	h := sha256.Sum256([]byte(b.Get("type").String() + "\x00" + k))
	return hex.EncodeToString(h[:16])
}

func isThinkingBlock(b gjson.Result) bool {
	t := b.Get("type").String()
	return t == "thinking" || t == "redacted_thinking"
}

// thinkingRefused tells whether p refused the block with key.
func (s *Server) thinkingRefused(owner, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refusedThinking[owner+"\x00"+key]
}

func (s *Server) refuseThinking(owner string, keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusedThinking == nil || len(s.refusedThinking)+len(keys) > thinkingRefusedMost {
		s.refusedThinking = make(map[string]bool)
	}
	for _, k := range keys {
		s.refusedThinking[owner+"\x00"+k] = true
	}
}

// withoutRefusedThinking is a Messages body without the thinking blocks p
// refused before; an assistant message left with nothing goes too. A body
// with none of them goes byte for byte.
func (s *Server) withoutRefusedThinking(p provider.Provider, body []byte) []byte {
	if !bytes.Contains(body, []byte(`thinking"`)) {
		return body
	}
	owner := thinkingOwner(p)
	s.mu.Lock()
	none := len(s.refusedThinking) == 0
	s.mu.Unlock()
	if none {
		return body
	}
	return withoutThinkingBlocks(body, func(_, _ int, b gjson.Result) bool {
		return s.thinkingRefused(owner, thinkingKey(b))
	})
}

// withoutThinkingBlocks leaves out of a Messages body each thinking or
// redacted_thinking block drop says to, at message m, block i, and an
// assistant message that had only those.
func withoutThinkingBlocks(body []byte, drop func(m, i int, b gjson.Result) bool) []byte {
	var q map[string]json.RawMessage
	var msgs []json.RawMessage
	if decodeJSON(body, &q) != nil || decodeJSON(q["messages"], &msgs) != nil {
		return body
	}
	out := make([]json.RawMessage, 0, len(msgs))
	changed := false
	for mi, raw := range msgs {
		content := gjson.GetBytes(raw, "content")
		if !content.IsArray() {
			out = append(out, raw)
			continue
		}
		var kept []json.RawMessage
		gone := false
		for bi, b := range content.Array() {
			if isThinkingBlock(b) && drop(mi, bi, b) {
				gone = true
				continue
			}
			kept = append(kept, json.RawMessage(b.Raw))
		}
		if !gone {
			out = append(out, raw)
			continue
		}
		changed = true
		if len(kept) == 0 {
			continue
		}
		var m map[string]json.RawMessage
		if decodeJSON(raw, &m) != nil {
			out = append(out, raw)
			continue
		}
		m["content"], _ = json.Marshal(kept)
		nb, err := json.Marshal(m)
		if err != nil {
			return body
		}
		out = append(out, nb)
	}
	if !changed {
		return body
	}
	q["messages"], _ = json.Marshal(out)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

// refuseBoundThinking reads a 400 from an Anthropic upstream: when it is
// Claude refusing a thinking block's signature, the block it names and
// every thinking block after it in body (as forwardOnce sends it, without
// those refused before) are remembered as refused by p. fresh is whether
// any was new, so asking again can go otherwise.
func (s *Server) refuseBoundThinking(p provider.Provider, errBody, body []byte) (fresh bool) {
	if !boundRefusal.Match(errBody) {
		return false
	}
	sent := s.withoutForeignThinking(p, s.withoutRefusedThinking(p, body))
	msgs := gjson.GetBytes(sent, "messages").Array()
	// from the block named, or from the start when it names none, or one
	// that isn't a thinking block in what was sent
	fromM, fromB := 0, 0
	if at := boundAt.FindSubmatch(errBody); at != nil {
		m, _ := strconv.Atoi(string(at[1]))
		b, _ := strconv.Atoi(string(at[2]))
		if m < len(msgs) {
			if blocks := msgs[m].Get("content").Array(); b < len(blocks) && isThinkingBlock(blocks[b]) {
				fromM, fromB = m, b
			}
		}
	}
	owner := thinkingOwner(p)
	var keys []string
	for mi := fromM; mi < len(msgs); mi++ {
		for bi, b := range msgs[mi].Get("content").Array() {
			if mi == fromM && bi < fromB || !isThinkingBlock(b) {
				continue
			}
			if k := thinkingKey(b); !s.thinkingRefused(owner, k) {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return false
	}
	s.refuseThinking(owner, keys)
	return true
}

// A conversation that ran on DeepSeek's Anthropic endpoint first (a
// routing group with DeepSeek unticked, then Claude: GgbondHi6930 on X)
// holds thinking blocks DeepSeek signed with its reply's id, a UUID.
// Claude refuses each one, 400 "messages.1.content.0: Invalid `signature`
// in `thinking` block" (checked on 2026-11-03), which forward mends only
// where the refusal reaches it as Claude's own 400, and with a request
// spent. A conversation that ran on a model magpie translates for (Grok
// on Cursor's plugin, a Chat or Responses vendor) holds thinking blocks
// signed empty: magpie gives every block a client gets a signature, and
// one it has none for is "" (thinking_signature.go). Claude Code keeps
// them through a compaction and sends them on to whatever the group
// picks next (citricdu on Discord: Grok 4.7, then Claude Opus 5.5 on
// Kiro, which answered 13 turns in a row with a bare
// invalid_request_error that refuseBoundThinking can't read; the same
// request without the block was answered). A block with no signature
// can't be Claude's own, as Claude signs every block it makes, and where
// it sits in a tool loop Claude checks it.
//
// withoutForeignThinking leaves both out of a request to an upstream
// serving Claude (api.anthropic.com, or a claude- model on any route:
// a relay, Bedrock's and Vertex AI's ids, a plugin such as Kiro's) before
// it sees them, and an assistant message left with nothing goes too. Its
// text, tool calls and the results after them stay. Claude's own
// signatures (base64, 3,000 of Claude Code's 800 characters and longer)
// go as they were, as do redacted_thinking and every other vendor's
// signature, which refuseBoundThinking still mends. Every other upstream,
// DeepSeek's among them (which wants its thinking back and takes an empty
// signature), gets the body byte for byte, and so does a model that asked
// for its tool turns' thinking back (thinking_back.go: DeepSeek behind a
// name that doesn't say so), whose filled-in blocks are signed empty.
func (s *Server) withoutForeignThinking(p provider.Provider, body []byte) []byte {
	if !s.fits(p.ID, thinkingBackNeeded(modelOf(body)), provider.Anthropic) {
		return body
	}
	return withoutForeignThinking(p, body)
}

func withoutForeignThinking(p provider.Provider, body []byte) []byte {
	if !bytes.Contains(body, []byte(`"signature"`)) && !thinkingTyped.Match(body) {
		return body
	}
	model := modelOf(body)
	if anthropicReplaysThinking(p.Host(), model) || p.Host() != "api.anthropic.com" && !anthropicModel.MatchString(model) {
		return body
	}
	return withoutThinkingBlocks(body, func(_, _ int, b gjson.Result) bool {
		if b.Get("type").String() != "thinking" {
			return false
		}
		sig := b.Get("signature").String()
		return strings.TrimSpace(sig) == "" || deepseekSignature.MatchString(sig)
	})
}

// thinkingTyped is a body with a thinking block in it, for one that says
// no signature at all.
var thinkingTyped = regexp.MustCompile(`"type"\s*:\s*"thinking"`)

// deepseekSignature is the signature DeepSeek's Anthropic endpoint gives
// its thinking: the reply's id, a UUID.
var deepseekSignature = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
