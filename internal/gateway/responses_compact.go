package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// POST /v1/responses/compact (and /responses/compact) is OpenAI's unary
// compaction: the conversation in, one response.compaction object back,
// its compaction item sealed (encrypted_content) by the server that wrote
// it. Pi's Responses compaction extension asks it of a provider set to the
// Responses API (#7281); on the generic route it was the 404 of an unknown
// path, and never reached the upstream.
//
// It is relayed as it came, to /responses/compact under the provider's
// Responses base, where that is the provider's own API: a key's provider
// whose model is asked on Responses (OpenAI, a relay or a local server
// speaking it, another magpie). Who sealed the item is remembered
// (wroteCheckpoint), so a later /responses carrying it goes back there
// (#1099).
//
// A ChatGPT or Copilot account (#7285) has no such route: the ChatGPT
// backend answers /backend-api/codex/responses/compact 404 {"detail":"Not
// Found"} to every body (tried 2026-11-22), and Codex itself (0.171)
// compacts on /responses, its input ending in {"type":"compaction_trigger"},
// the backend streaming back one compaction item. So such an account is
// asked that, as Codex asks it, streamed and gathered (gathered.compact),
// and the client gets the response.compaction OpenAI's route answers: the
// input's user messages, then the compaction item. Codex's instructions
// are the account's own (codexBody), untouched.
//
// Nothing else can make the sealed item — a model on Chat or Messages
// only, a plugin's account (#7285: no plugin carries the trigger) — so
// with none of those left the request is a 400 saying so, as CodexPath's
// is for magpie's models, for the client to fall back to compacting on
// /responses.

type unaryCompactKey struct{}

// responsesCompact serves POST /v1/responses/compact.
func (s *Server) responsesCompact(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Responses)
	if !ok {
		return
	}
	body, _, err := requestModel(body)
	if err != nil {
		writeError(w, provider.Responses, 400, err.Error())
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), unaryCompactKey{}, true))
	s.serveAgent(w, r, provider.Responses, body)
}

// unaryCompact is whether r is a /responses/compact request.
func unaryCompact(r *http.Request) bool {
	v, _ := r.Context().Value(unaryCompactKey{}).(bool)
	return v
}

// compactsUnary is whether model at p can be asked /responses/compact:
// one whose wire carries Codex's compaction and asks model on Responses —
// a key's provider, on its own route, or a ChatGPT or Copilot account,
// through the trigger (compactsByTrigger).
func (s *Server) compactsUnary(p provider.Provider, model string) bool {
	return s.carriesCompaction(p, model)
}

// compactsByTrigger is whether a /responses/compact to p is asked on
// /responses with Codex's compaction_trigger rather than relayed to
// /responses/compact: an account's, whose backend has no such route.
func compactsByTrigger(p provider.Provider) bool {
	return p.Account != nil
}

// compactInput is a Responses body's input as items: a string input is
// one user message, as the API reads it.
func compactInput(body []byte) []json.RawMessage {
	in := getIn(body, "input")
	if in.Type == gjson.String {
		b, _ := json.Marshal(map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": in.Str}}})
		return []json.RawMessage{b}
	}
	var out []json.RawMessage
	for _, it := range in.Array() {
		out = append(out, json.RawMessage(it.Raw))
	}
	return out
}

// withCompactionTrigger is a /responses/compact body made the /responses
// request Codex compacts with: its input, then {"type":"compaction_trigger"}.
func withCompactionTrigger(body []byte) []byte {
	items := append(compactInput(body), json.RawMessage(`{"type":"compaction_trigger"}`))
	return withFields(body, map[string]any{"input": items})
}

// compactKept is what of a /responses/compact input the reply keeps ahead
// of the compaction, as OpenAI's route does: the user messages.
func compactKept(body []byte) []json.RawMessage {
	var out []json.RawMessage
	for _, it := range compactInput(body) {
		r := gjson.ParseBytes(it)
		if r.Get("role").Str == "user" && (r.Get("type").Str == "message" || !r.Get("type").Exists()) {
			out = append(out, it)
		}
	}
	return out
}

// compactionReply is the response.compaction for resp, the response a
// compaction_trigger was answered with: kept, then resp's compaction
// items, with resp's id, created_at and usage. ok is false when resp holds
// no compaction item: the model answered the trigger with something else.
func compactionReply(resp json.RawMessage, kept []json.RawMessage) ([]byte, bool) {
	r := gjson.ParseBytes(resp)
	out := slices.Clone(kept)
	for _, it := range r.Get("output").Array() {
		if slices.Contains(compactionKinds, it.Get("type").Str) && it.Get("encrypted_content").Str != "" {
			out = append(out, json.RawMessage(it.Raw))
		}
	}
	if len(out) == len(kept) {
		return nil, false
	}
	id := r.Get("id").Str
	if id == "" {
		id = "cmp_" + newID()
	}
	m := map[string]any{"id": id, "object": "response.compaction", "output": out}
	if c := r.Get("created_at"); c.Exists() {
		m["created_at"] = json.RawMessage(c.Raw)
	} else {
		m["created_at"] = time.Now().Unix()
	}
	if u := r.Get("usage"); u.IsObject() {
		m["usage"] = json.RawMessage(u.Raw)
	}
	b, err := json.Marshal(m)
	return b, err == nil
}

// unaryCompactCarriers keeps of cands those compactsUnary.
func (s *Server) unaryCompactCarriers(cands []candidate, pl planned) ([]candidate, planned, bool) {
	return keepCandidates(cands, pl, func(c candidate) bool { return s.compactsUnary(c.p, c.model) })
}

// unaryCompactError is what a /responses/compact for model is told when
// nothing that serves it has the route.
func unaryCompactError(model string) string {
	return fmt.Sprintf("/responses/compact is not supported for %s: it is relayed only to a provider whose own API is Responses (an API key's provider asking the model on Responses) or a ChatGPT or Copilot account asking it there, and none of those serves %s; compact on /responses instead", model, model)
}
