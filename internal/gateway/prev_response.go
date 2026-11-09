package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A Responses client may send only a turn's new input and name the
// response it follows (previous_response_id), as OpenAI's own API lets it
// (kevinzhow on X: every request carried the whole conversation, images
// and all). magpie keeps each response it gave such a client, its input
// and its output, under the response's id, and a request that names one
// is given the conversation whole before anything else reads it: a
// translation, a hook, the masking. A provider that keeps responses itself
// (prevKeeps), and answered the one named, is sent the new input alone
// after its own id, so what it holds isn't sent again.
//
// What is kept is the client's own form, before masking: the conversation
// it is given back into goes through the masking as every request does.
// It is kept in memory alone, bounded (prevCap) and for prevTTL, under the
// caller's gateway key (prevScope): one key never reads another's.
// A client that says store false (Codex over HTTP, whose requests never
// name a previous response: codex-rs/codex-api/src/common.rs keeps
// previous_response_id to its websocket's response.create) has nothing
// kept, but for another magpie asking its turns kept (remote_delta.go).

const (
	prevTTL = 6 * time.Hour
	// prevCap bounds the bytes kept, counted as each turn adds them: a
	// turn's history is the earlier turn's items, shared, not copied
	prevCap = 256 << 20
	// prevReplyMax is the most of one reply read for its output
	prevReplyMax = 64 << 20
)

type prevKey struct{ scope, id string }

// prevTurn is one response kept: the conversation up to and with it.
type prevTurn struct {
	key   prevKey
	items []json.RawMessage // input, then the response's output
	own   int               // bytes this turn added to what it follows
	at    time.Time
	// the provider that keeps it too, if one does: the id it gave it
	// and which provider and key (prevRoute)
	upstreamID, route string
	// the provider and key whose reply the output is, as relayed
	// (prevRoute), kept or not; empty for a translated reply
	made string
}

type prevStore struct {
	mu    sync.Mutex
	m     map[prevKey]*list.Element
	order list.List // front the newest
	size  int
	cap   int
	ttl   time.Duration
	now   func() time.Time
}

var prevResponses = newPrevStore(prevCap, prevTTL)

func newPrevStore(cap int, ttl time.Duration) *prevStore {
	return &prevStore{m: map[prevKey]*list.Element{}, cap: cap, ttl: ttl, now: time.Now}
}

func (s *prevStore) get(k prevKey) (*prevTurn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[k]
	if !ok {
		return nil, false
	}
	t := e.Value.(*prevTurn)
	if s.now().Sub(t.at) > s.ttl {
		s.drop(e)
		return nil, false
	}
	s.order.MoveToFront(e)
	return t, true
}

func (s *prevStore) put(t *prevTurn) {
	if t.own > s.cap/2 {
		return // one turn would push out nearly everything
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[t.key]; ok {
		s.drop(e)
	}
	t.at = s.now()
	s.m[t.key] = s.order.PushFront(t)
	s.size += t.own
	for s.size > s.cap {
		s.drop(s.order.Back())
	}
	// and what has outlived its time, from the oldest
	for e := s.order.Back(); e != nil && s.now().Sub(e.Value.(*prevTurn).at) > s.ttl; e = s.order.Back() {
		s.drop(e)
	}
}

func (s *prevStore) drop(e *list.Element) {
	t := e.Value.(*prevTurn)
	s.order.Remove(e)
	delete(s.m, t.key)
	s.size -= t.own
}

// prevScope is whose responses a request may name: its gateway key, the
// computer a remote magpie passed it on from, and for a request with no
// key from another computer, that computer's address.
func prevScope(r *http.Request) string {
	keyID := access.Caller(r.Context()).KeyID
	s := "key:" + keyID
	if via := callerOf(r).via; via != "" {
		s += "\x00via:" + via
	}
	if keyID == "" && !local(r) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		s += "\x00ip:" + host
	}
	return s
}

// prevRoute is the provider and API key a response came from: one the
// provider keeps is named to it again only with the same key.
func prevRoute(p provider.Provider) string {
	sum := sha256.Sum256([]byte(p.Key))
	return p.ID + "\x00" + hex.EncodeToString(sum[:8])
}

// prevKeeps is whether p keeps the responses it gives and takes
// previous_response_id: OpenAI's API and Azure OpenAI, and Ark's own
// /api/v3 (tried with a real key on 10-29: a stored response's word came
// back after its id, and an unknown id is a 400 PreviousResponseNotFound).
// A subscription's or a plugin's account is never asked so.
var prevKeptBy = prevKeeps

func prevKeeps(p provider.Provider) bool {
	if p.Account != nil || p.PluginProvider() != "" {
		return false
	}
	host := p.Host()
	if host == "api.openai.com" || p.IsAzure() {
		return true
	}
	return host == "ark.cn-beijing.volces.com" && strings.Contains(p.Base(provider.Responses), "/api/v3")
}

// prevAsk is what serveAgent made of a request's previous_response_id,
// for the tries of it (attempt, passthrough).
type prevAsk struct {
	id    string // as the client named it
	known bool   // the conversation was given back whole
	// the items of the whole conversation, and how many at its end are
	// the client's own new input
	total, fresh int
	upstreamID   string // the id the provider that answered it keeps it under
	route        string
	made         string // the route whose reply the named response was
	// which try answered: its route if it was relayed to a provider
	// that keeps what it is sent, empty otherwise; and by, its route
	// for any relayed try
	mu           sync.Mutex
	answered, by string
}

type prevAskKey struct{}

func prevAskOf(ctx context.Context) *prevAsk {
	a, _ := ctx.Value(prevAskKey{}).(*prevAsk)
	return a
}

// prevHeld is whether the request named a response magpie holds and was
// given its conversation whole.
func prevHeld(r *http.Request) bool {
	a := prevAskOf(r.Context())
	return a != nil && a.known
}

// prevLost is the id a request named that magpie doesn't hold, which a
// try that doesn't relay it to a provider as written can't honour.
func prevLost(r *http.Request) string {
	if a := prevAskOf(r.Context()); a != nil && a.id != "" && !a.known {
		return a.id
	}
	return ""
}

// writePrevNotFound is OpenAI's own answer to an id it doesn't hold.
func writePrevNotFound(w http.ResponseWriter, id string) (int, string) {
	msg := "Previous response with id '" + id + "' not found."
	return writeErrorSaid(w, provider.Responses, 400, msg, "previous_response_not_found", "previous_response_id"), msg
}

// noteAnswered is called by each try: the last is the one that answered.
// route is the provider and key if it keeps what it is sent, by the
// provider and key a relayed try went to (both empty for a translation).
func noteAnswered(r *http.Request, route, by string) {
	if a := prevAskOf(r.Context()); a != nil {
		a.mu.Lock()
		a.answered, a.by = route, by
		a.mu.Unlock()
	}
}

// previousResponse gives a Responses request that names a response magpie
// holds the conversation whole, and keeps the reply for the next turn.
// Its done is deferred by the caller.
func previousResponse(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) (http.ResponseWriter, *http.Request, []byte, func()) {
	nothing := func() {}
	if from != provider.Responses {
		return w, r, body, nothing
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return w, r, body, nothing
	}
	scope := prevScope(r)
	ask := &prevAsk{}
	var prior []json.RawMessage
	if raw := q["previous_response_id"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var id string
		if json.Unmarshal(raw, &id) == nil && id != "" {
			ask.id = id
			if t, ok := prevResponses.get(prevKey{scope, id}); ok {
				prior = t.items
				ask.known, ask.upstreamID, ask.route, ask.made = true, t.upstreamID, t.route, t.made
			}
		}
	}
	input, fresh := prevInput(q["input"])
	if fresh == nil {
		// an input magpie can't read as items: left as it came
		return w, r, body, nothing
	}
	if ask.known {
		input = append(prior[:len(prior):len(prior)], fresh...)
		if b, err := json.Marshal(input); err == nil {
			q["input"] = b
			delete(q, "previous_response_id")
			if nb, err := json.Marshal(q); err == nil {
				body = nb
			}
		}
		ask.total, ask.fresh = len(input), len(fresh)
	}
	if ask.id != "" {
		r = r.WithContext(context.WithValue(r.Context(), prevAskKey{}, ask))
	}
	var store bool
	if json.Unmarshal(q["store"], &store) == nil && !store && !keepsTurnFor(r) {
		return w, r, body, nothing // the client asked that nothing be kept
	}
	if keepsTurnFor(r) {
		// another magpie asked it kept, store false or not, and is told
		// it is (remote_delta.go)
		w.Header().Set(KeptTurnHeader, "1")
	}
	// what this turn adds: its input, copied out of the body. A view held
	// the whole body, tools and instructions with it, on every turn kept,
	// while own counted only the items, so the cap bounded far less than
	// was held.
	kept := ownItems(fresh)
	if ask.known {
		kept = append(prior[:len(prior):len(prior)], kept...)
	}
	own := 0
	for _, it := range fresh {
		own += len(it)
	}
	if ask.id == "" {
		// the answer note is wanted for a request naming none too
		r = r.WithContext(context.WithValue(r.Context(), prevAskKey{}, ask))
	}
	sink := &conversationWriter{captureResponseWriter: &captureResponseWriter{
		ResponseWriter: w, full: &spool{limit: prevReplyMax, pattern: "magpie-prevresp-*"},
	}}
	return sink, r, body, func() {
		defer sink.full.discard()
		if sink.status != 0 && sink.status != http.StatusOK || sink.full.cut() {
			return
		}
		reply, err := sink.full.read()
		if err != nil || len(reply) == 0 {
			return
		}
		id, output, ok := prevReply(sink.Header().Get("Content-Type"), reply)
		if !ok {
			return
		}
		for _, it := range output {
			own += len(it)
		}
		t := &prevTurn{key: prevKey{scope, id}, items: append(kept, output...), own: own}
		ask.mu.Lock()
		if ask.answered != "" {
			t.upstreamID, t.route = id, ask.answered
		}
		t.made = ask.by
		ask.mu.Unlock()
		prevResponses.put(t)
	}
}

// prevInput is a Responses input as items: a string is one user message.
// fresh is nil for an input that is neither. The items share the body's
// bytes, which nothing writes once read; what is kept past the request
// copies them (ownItems).
func prevInput(raw json.RawMessage) (all, fresh []json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, nil
		}
		m, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": s})
		return []json.RawMessage{m}, []json.RawMessage{m}
	}
	items, ok := inputList(raw)
	if !ok || raw[0] != '[' {
		return nil, nil
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return items, items
}

// prevReply is a Responses reply's id and output items, a stream's or a
// whole body's; ok false for one that didn't complete.
func prevReply(ctype string, reply []byte) (id string, output []json.RawMessage, ok bool) {
	var resp json.RawMessage
	if strings.Contains(ctype, "event-stream") || bytes.HasPrefix(reply, []byte("event:")) || bytes.HasPrefix(reply, []byte("data:")) {
		items := map[int]json.RawMessage{}
		readSSE(bytes.NewReader(reply), func(event, data string) error {
			var e struct {
				Type        string          `json:"type"`
				Response    json.RawMessage `json:"response"`
				Item        json.RawMessage `json:"item"`
				OutputIndex int             `json:"output_index"`
			}
			if json.Unmarshal([]byte(data), &e) != nil {
				return nil
			}
			if e.Type == "" {
				e.Type = event
			}
			switch e.Type {
			case "response.output_item.done":
				if len(e.Item) > 0 {
					items[e.OutputIndex] = e.Item
				}
			case "response.completed", "response.incomplete":
				resp = e.Response
			}
			return nil
		})
		if len(resp) == 0 {
			return "", nil, false
		}
		resp = withStreamedOutput(resp, items)
	} else {
		resp = reply
	}
	var r struct {
		ID     string            `json:"id"`
		Object string            `json:"object"`
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(resp, &r) != nil || r.ID == "" || r.Status == "failed" || r.Status == "cancelled" {
		return "", nil, false
	}
	return r.ID, r.Output, true
}

// prevDelta is body, a Responses request magpie gave its conversation
// whole, with the new input alone after the id p keeps the previous
// response under, when p is the provider and key that answered it and
// keeps what it is sent. The tries' changes to the input are kept: the
// new items are the body's own last ones, and only when the body still
// has as many items as magpie gave it.
func prevDelta(r *http.Request, p provider.Provider, body []byte) ([]byte, bool) {
	a := prevAskOf(r.Context())
	if a == nil || !a.known || a.upstreamID == "" || a.route != prevRoute(p) || !prevKeptBy(p) {
		return body, false
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body, false
	}
	var store bool
	if json.Unmarshal(q["store"], &store) == nil && !store {
		return body, false
	}
	items, ok := inputList(q["input"])
	if !ok || len(items) != a.total || a.fresh > len(items) {
		return body, false
	}
	in, err := json.Marshal(items[len(items)-a.fresh:])
	if err != nil {
		return body, false
	}
	id, _ := json.Marshal(a.upstreamID)
	out := make(map[string]json.RawMessage, len(q)+1)
	for k, v := range q {
		out[k] = v
	}
	out["input"], out["previous_response_id"] = in, id
	nb, err := json.Marshal(out)
	if err != nil {
		return body, false
	}
	return nb, true
}

// prevRefused is an upstream's answer that it doesn't hold the previous
// response, or takes no such field: the conversation is sent whole.
func prevRefused(status int, b []byte) bool {
	if !badRequest(status) && status != http.StatusNotFound {
		return false
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "previous_response") || strings.Contains(s, "previousresponse")
}

// prevForeign is an upstream's refusal of an item, or the response named,
// that another of its resources made: Azure OpenAI's, behind a relay that
// spreads one key over several resources (hykql on Discord: "The
// requested item was created under a different Azure OpenAI resource. Use
// the same resource that created the item to access it."). The text is
// the report's; it was not reproduced against Azure itself.
func prevForeign(status int, b []byte) bool {
	if !badRequest(status) && status != http.StatusNotFound {
		return false
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "created under a different") && strings.Contains(s, "resource")
}

// prevElsewhere is body, a Responses request magpie gave its conversation
// whole, with the items it brought back no longer naming what the
// upstream that made them keeps them under, when they go to another
// provider or key than the one whose reply they were (or always, when
// force: one upstream said they are another resource's). An item that
// can go without its id (idOptional) goes without it, its text and
// call_id kept; reasoning keeps its place only with its encrypted_content,
// as Codex sends it, since what it held lives only where it was made. Any
// other kind, and the client's own new input, stay as they were. A body
// whose items the tries changed in number is left as it is, as prevDelta
// leaves it.
func prevElsewhere(r *http.Request, p provider.Provider, body []byte, force bool) []byte {
	a := prevAskOf(r.Context())
	if a == nil || !a.known || !force && a.made == prevRoute(p) {
		return body
	}
	n := a.total - a.fresh
	if n <= 0 || !bytes.Contains(body, []byte(`"id"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return body
	}
	items, ok := inputList(q["input"])
	if !ok || len(items) != a.total {
		return body
	}
	out := make([]json.RawMessage, 0, len(items))
	changed := false
	for i, raw := range items {
		var it map[string]json.RawMessage
		if i >= n || json.Unmarshal(raw, &it) != nil {
			out = append(out, raw)
			continue
		}
		if _, has := it["id"]; !has {
			out = append(out, raw)
			continue
		}
		var typ string
		json.Unmarshal(it["type"], &typ)
		switch {
		case typ == "reasoning" && gjson.GetBytes(raw, "encrypted_content").String() == "":
			changed = true
			continue
		case typ == "reasoning" || idOptional[typ]:
			delete(it, "id")
			b, err := marshalPlain(it)
			if err != nil {
				return body
			}
			out, changed = append(out, b), true
		default:
			out = append(out, raw)
		}
	}
	if !changed {
		return body
	}
	in, err := marshalPlain(out)
	if err != nil {
		return body
	}
	m := make(map[string]json.RawMessage, len(q))
	for k, v := range q {
		m[k] = v
	}
	m["input"] = in
	nb, err := marshalPlain(m)
	if err != nil {
		return body
	}
	return nb
}

// storeOff is whether a Responses body says store false.
func storeOff(body []byte) bool {
	v := gjson.GetBytes(body, "store")
	return v.Exists() && v.Type == gjson.False
}
