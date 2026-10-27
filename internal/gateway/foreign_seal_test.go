package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// xaiUndecryptable is what xAI answers — a Grok API key or a SuperGrok
// account alike — for reasoning another account or vendor sealed.
const xaiUndecryptable = "Could not decrypt the provided encrypted_content. Ensure the value is the unmodified encrypted_content from a previous response."

// sealer is a Grok-like Responses upstream: it seals its reasoning as
// "sealed-by-<name>" and can read only its own.
type sealer struct {
	name    string
	down    bool   // answering 429, for the conversation to move on
	stream  bool   // refusing in the stream (response.failed), not as a 400
	errJSON string // refusing as a 400 with this body, not xAI's words
	mu      sync.Mutex
	got     [][]string // the sealed reasoning each request carried
}

func (s *sealer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q struct {
		Input []sealedItem `json:"input"`
	}
	json.Unmarshal(b, &q)
	var got []string
	foreign := false
	for _, it := range q.Input {
		if it.Type == "reasoning" {
			got = append(got, it.Enc)
			foreign = foreign || it.Enc != "sealed-by-"+s.name
		}
	}
	s.mu.Lock()
	s.got = append(s.got, got)
	s.mu.Unlock()
	switch {
	case s.down:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	case foreign && !s.stream:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		if s.errJSON != "" {
			io.WriteString(w, s.errJSON)
			return
		}
		io.WriteString(w, `{"code":"Client specified an invalid argument","error":"`+xaiUndecryptable+`"}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if foreign {
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r0","model":"grok-4"}}`,
			`data: {"type":"response.failed","response":{"id":"r0","error":{"code":"invalid_request_error","message":"`+xaiUndecryptable+`"}}}`))
		return
	}
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"grok-4"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"sealed-by-`+s.name+`"}}`,
		`data: {"type":"response.output_text.delta","delta":"from `+s.name+`"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
}

func (s *sealer) sent() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

// Switching a Codex conversation between two Grok accounts of a routing
// group mid-way (waroy): the second can't decrypt the first's sealed
// reasoning. It's asked again without it, and later turns leave out what
// it refused while its own sealed reasoning still goes along.
func TestForeignSealAcrossGroupMembers(t *testing.T) {
	for _, inStream := range []bool{false, true} {
		fresh(t)
		refusedSeals.Lock()
		refusedSeals.m = map[string]sealsRefused{}
		refusedSeals.Unlock()
		a, b := &sealer{name: "a"}, &sealer{name: "b", stream: inStream}
		for _, u := range []struct {
			id string
			h  http.Handler
		}{{"grok1", a}, {"grok2", b}} {
			up := httptest.NewServer(u.h)
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: u.id, Name: "Grok (SuperGrok)", Key: "k-" + u.id, Models: []string{"grok-4"}, Responses: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.SaveGroup(provider.Group{Name: "Grok", Members: []string{"grok1/grok-4", "grok2/grok-4"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		turn := func(items ...string) (int, string) {
			t.Helper()
			in := `{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}`
			for _, it := range items {
				in += "," + it
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/grok","stream":true,"input":[`+in+`]}`))
			req.Header.Set("session_id", "thread-1")
			s.Handler().ServeHTTP(rec, req)
			return rec.Code, rec.Body.String()
		}
		reasoning := func(by string) string {
			return `{"type":"reasoning","id":"rs_` + by + `","summary":[],"encrypted_content":"sealed-by-` + by + `"}`
		}
		call := func(id string) string {
			return `{"type":"function_call","call_id":"` + id + `","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"` + id + `","output":"ok"}`
		}

		// a answers, and reads back what it sealed
		if code, body := turn(); code != 200 || !strings.Contains(body, "from a") {
			t.Fatalf("stream=%v first: %d %s", inStream, code, body)
		}
		if code, body := turn(reasoning("a"), call("c1")); code != 200 || !strings.Contains(body, "from a") {
			t.Fatalf("stream=%v second: %d %s", inStream, code, body)
		}
		if got := a.sent(); len(got) != 2 || strings.Join(got[1], ",") != "sealed-by-a" {
			t.Fatalf("stream=%v a was sent %q", inStream, got)
		}

		// a goes down: b takes the conversation over, without a's seal
		a.down = true
		code, body := turn(reasoning("a"), call("c1"), reasoning("a"), call("c2"))
		if code != 200 || !strings.Contains(body, "from b") {
			t.Fatalf("stream=%v after the switch: %d %s", inStream, code, body)
		}
		if got := b.sent(); len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 0 {
			t.Fatalf("stream=%v b was sent %q", inStream, got)
		}

		// the next turn doesn't hit it again: a's seal is left out up front,
		// b's own goes along
		code, body = turn(reasoning("a"), call("c1"), reasoning("a"), call("c2"), reasoning("b"), call("c3"))
		if code != 200 || !strings.Contains(body, "from b") {
			t.Fatalf("stream=%v next turn: %d %s", inStream, code, body)
		}
		if got := b.sent(); len(got) != 1 || strings.Join(got[0], ",") != "sealed-by-b" {
			t.Fatalf("stream=%v b was then sent %q", inStream, got)
		}
		a.sent()
	}
}

// Each vendor's words for reasoning it can't read are known; a request
// at fault some other way is not taken for one.
func TestForeignReasoningWords(t *testing.T) {
	for _, m := range []string{
		xaiUndecryptable,
		`{"error":{"message":"The encrypted content for item rs_1 could not be verified.","code":"invalid_encrypted_content"}}`,
		"Grok (SuperGrok): Could not decrypt the provided encrypted_content.",
		`{"error":{"message":"invalid encrypted summary","type":"invalid_request_error","param":null,"code":"validation_error"}}`,
		relayCompaction,
		"Could not decode the compaction blob. Ensure it is unmodified from the compact response.",
	} {
		if !foreignReasoning.MatchString(m) {
			t.Errorf("not matched: %s", m)
		}
	}
	for _, m := range []string{"Invalid value for reasoning.effort", "model not found", "could not parse the JSON body", "Could not decode the request body."} {
		if foreignReasoning.MatchString(m) {
			t.Errorf("matched: %s", m)
		}
	}
}

// openaiUnverifiable is what the ChatGPT backend answers for reasoning
// another vendor or account sealed; the item's id is left blank, as the
// request carries no ids (store is false).
const openaiUnverifiable = "The encrypted content for item rs_ could not be verified. Reason: Encrypted content could not be decrypted or parsed."

// chatgptBackend is a ChatGPT-backend-like upstream for a Codex sign-in:
// it seals its reasoning as "sealed-by-openai" and refuses any other, as a
// 400 or in the stream, after the backend's own first events.
type chatgptBackend struct {
	stream bool
	mu     sync.Mutex
	got    [][]string
}

func (c *chatgptBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q struct {
		Input []sealedItem `json:"input"`
	}
	json.Unmarshal(b, &q)
	var got []string
	foreign := false
	for _, it := range q.Input {
		if it.Enc != "" {
			got = append(got, it.Enc)
			foreign = foreign || it.Enc != "sealed-by-openai"
		}
	}
	c.mu.Lock()
	c.got = append(c.got, got)
	c.mu.Unlock()
	if foreign && !c.stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"`+openaiUnverifiable+`","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`)
		return
	}
	// the ChatGPT backend streams without a Content-Type, and tells of the
	// account's limits before anything else
	lead := []string{
		`event: codex.rate_limits` + "\n" + `data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":3,"window_minutes":300,"reset_after_seconds":100}}}`,
		`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"r0","model":"gpt-5.5"}}`,
		`event: response.in_progress` + "\n" + `data: {"type":"response.in_progress","response":{"id":"r0","model":"gpt-5.5"}}`,
	}
	if foreign {
		io.WriteString(w, sse(append(lead,
			`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"r0","status":"failed","error":{"code":"invalid_encrypted_content","message":"`+openaiUnverifiable+`"}}}`)...))
		return
	}
	io.WriteString(w, sse(append(lead,
		`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_o","summary":[],"encrypted_content":"sealed-by-openai"}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from openai"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`)...))
}

func (c *chatgptBackend) sent() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got = nil
	return out
}

// A routing group of a Grok member and Codex's GPT on a ChatGPT sign-in
// (waroy: The encrypted content for item rs_ could not be verified): the
// conversation moves from Grok to GPT, which can't read Grok's sealed
// reasoning. It's asked again without it, whether the backend says so as a
// 400 or in the stream after its first events, and later turns leave out
// what it refused while its own sealed reasoning still goes along.
func TestForeignSealGrokToCodexAccount(t *testing.T) {
	for _, inStream := range []bool{false, true} {
		fresh(t)
		home := os.Getenv("HOME")
		refusedSeals.Lock()
		refusedSeals.m = map[string]sealsRefused{}
		refusedSeals.Unlock()
		claims := func(m map[string]any) string {
			b, _ := json.Marshal(m)
			return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
		}
		os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
		os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": "me@example.com"}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r", "account_id": "acct-1"}}), 0o600)
		provider.ForgetAccounts()
		t.Cleanup(provider.ForgetAccounts)

		g, o := &sealer{name: "grok"}, &chatgptBackend{stream: inStream}
		gup := httptest.NewServer(g)
		t.Cleanup(gup.Close)
		oup := httptest.NewServer(o)
		t.Cleanup(oup.Close)
		was := provider.CodexBase
		provider.CodexBase = oup.URL + "/backend-api/codex"
		t.Cleanup(func() { provider.CodexBase = was })
		if err := provider.Save(provider.Provider{ID: "grok1", Name: "Grok (SuperGrok)", Key: "k", Models: []string{"grok-4"}, Responses: gup.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"grok1/grok-4", "codex/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		turn := func(items ...string) (int, string) {
			t.Helper()
			in := `{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}`
			for _, it := range items {
				in += "," + it
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/mixed","stream":true,"input":[`+in+`]}`))
			req.Header.Set("session_id", "thread-2")
			s.Handler().ServeHTTP(rec, req)
			return rec.Code, rec.Body.String()
		}
		reasoning := func(by string) string {
			return `{"type":"reasoning","id":"rs_` + by + `","summary":[],"content":null,"encrypted_content":"sealed-by-` + by + `"}`
		}
		call := func(id string) string {
			return `{"type":"function_call","call_id":"` + id + `","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"` + id + `","output":"ok"}`
		}

		if code, body := turn(); code != 200 || !strings.Contains(body, "from grok") {
			t.Fatalf("stream=%v first: %d %s", inStream, code, body)
		}
		g.down = true
		code, body := turn(reasoning("grok"), call("c1"))
		if code != 200 || !strings.Contains(body, "from openai") || strings.Contains(body, "could not be verified") {
			t.Fatalf("stream=%v after the switch: %d %s", inStream, code, body)
		}
		if got := o.sent(); len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 0 {
			t.Fatalf("stream=%v openai was sent %q", inStream, got)
		}
		code, body = turn(reasoning("grok"), call("c1"), reasoning("openai"), call("c2"))
		if code != 200 || !strings.Contains(body, "from openai") {
			t.Fatalf("stream=%v next turn: %d %s", inStream, code, body)
		}
		if got := o.sent(); len(got) != 1 || strings.Join(got[0], ",") != "sealed-by-openai" {
			t.Fatalf("stream=%v openai was then sent %q", inStream, got)
		}
		g.sent()
	}
}

// azureSummary is what a relay in front of an Azure OpenAI deployment
// passes back for reasoning another backend sealed (snowshadow): it says
// "invalid encrypted summary", not invalid_encrypted_content, and names
// no item.
const azureSummary = `{"error":{"message":"invalid encrypted summary","type":"invalid_request_error","param":null,"code":"validation_error"}}`

// relayCompaction is what a key relay that proxies Grok passes back for
// the reasoning item Codex hands it with "content": null beside its
// encrypted_content (zminds #4412): xAI's own compaction-blob refusal,
// spelled by the relay in front of it. It names no item and says nothing
// of encrypted content at all.
const relayCompaction = `{"error":{"code":null,"message":"OpenCode Go: Could not decode the compaction blob. Ensure it is unmodified from the compact response.","param":null,"type":"invalid_request_error"}}`

// grokRelay is a relay-like Responses upstream that proxies Grok: a null
// content beside a sealed blob it refuses with the compaction-blob words
// (a null beside no blob goes by — the issue counted 99 such items pass),
// everything else it answers.
type grokRelay struct {
	mu  sync.Mutex
	got [][]string // the sealed reasoning each request carried
}

func (g *grokRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q struct {
		Input []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Enc     string          `json:"encrypted_content"`
		} `json:"input"`
	}
	json.Unmarshal(b, &q)
	var got []string
	refused := false
	for _, it := range q.Input {
		if it.Type != "reasoning" {
			continue
		}
		if it.Enc != "" {
			got = append(got, it.Enc)
			refused = refused || strings.TrimSpace(string(it.Content)) == "null"
		}
	}
	g.mu.Lock()
	g.got = append(g.got, got)
	g.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, relayCompaction)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"grok-4.7"}}`,
		`data: {"type":"response.output_text.delta","delta":"from relay"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
}

func (g *grokRelay) sent() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.got
	g.got = nil
	return out
}

// A Codex thread the ChatGPT backend served, resumed on a relay in front
// of an Azure deployment (snowshadow): the deployment can't read the
// reasoning the ChatGPT backend sealed and refuses it as "invalid
// encrypted summary". It's asked again without it, and the next turn
// leaves it out up front, instead of 400ing on every turn.
func TestForeignSealAzureSummary(t *testing.T) {
	fresh(t)
	refusedSeals.Lock()
	refusedSeals.m = map[string]sealsRefused{}
	refusedSeals.Unlock()
	az := &sealer{name: "azure", errJSON: azureSummary}
	up := httptest.NewServer(az)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "az1", Name: "Azure", Key: "k", Models: []string{"gpt-6.1-sol"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	turn := func(items ...string) (int, string) {
		t.Helper()
		in := `{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}`
		for _, it := range items {
			in += "," + it
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"az1/gpt-6.1-sol","stream":true,"input":[`+in+`]}`))
		req.Header.Set("session_id", "thread-azure")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	sealed := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"sealed-by-chatgpt"}`
	call := func(id string) string {
		return `{"type":"function_call","call_id":"` + id + `","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"` + id + `","output":"ok"}`
	}

	// the seal the ChatGPT backend wrote is refused as an invalid
	// encrypted summary, and it's asked again without it
	if code, body := turn(sealed, call("c1")); code != 200 || !strings.Contains(body, "from azure") {
		t.Fatalf("first: %d %s", code, body)
	}
	if got := az.sent(); len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 0 {
		t.Fatalf("first turn sent %q, want the seal once, then none", got)
	}
	// the next turn doesn't hit it again: it's left out up front
	if code, body := turn(sealed, call("c1")); code != 200 || !strings.Contains(body, "from azure") {
		t.Fatalf("next turn: %d %s", code, body)
	}
	if got := az.sent(); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("next turn sent %q, want one request without the seal", got)
	}
}

// A Codex conversation that reaches a key relay that proxies Grok
// (zminds #4412): the relay is not api.x.ai, so the #3392 shaping
// (xaiBody's null-content removal) never runs on its requests, and the
// reasoning item Codex hands back — "content": null beside its
// encrypted_content — comes out as xAI's "Could not decode the
// compaction blob", a 400 that names no item and no encrypted content.
// It's asked again without the reasoning, and the next turn leaves the
// seal out up front, instead of 400ing on every turn with nothing
// retrying.
func TestForeignSealGrokRelayCompactionBlob(t *testing.T) {
	fresh(t)
	refusedSeals.Lock()
	refusedSeals.m = map[string]sealsRefused{}
	refusedSeals.Unlock()
	rel := &grokRelay{}
	up := httptest.NewServer(rel)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay1", Name: "OpenCode Go", Key: "k", Models: []string{"grok-4.7"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	turn := func(items ...string) (int, string) {
		t.Helper()
		in := `{"type":"message","role":"user","content":[{"type":"input_text","text":"count the lines"}]}`
		for _, it := range items {
			in += "," + it
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"relay1/grok-4.7","stream":true,"input":[`+in+`]}`))
		req.Header.Set("session_id", "thread-relay")
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// Codex's shape: the null content beside the seal (#4412's input[681])
	sealed := `{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"gAABsealed"}`
	call := func(id string) string {
		return `{"type":"function_call","call_id":"` + id + `","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"` + id + `","output":"ok"}`
	}

	// the relay refuses the null content beside the blob, and it's asked
	// again without the reasoning
	if code, body := turn(sealed, call("c1")); code != 200 || !strings.Contains(body, "from relay") {
		t.Fatalf("first: %d %s", code, body)
	}
	if got := rel.sent(); len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 0 {
		t.Fatalf("first turn sent %q, want the seal once, then none", got)
	}
	// the next turn doesn't hit it again: the seal is left out up front
	if code, body := turn(sealed, call("c1")); code != 200 || !strings.Contains(body, "from relay") {
		t.Fatalf("next turn: %d %s", code, body)
	}
	if got := rel.sent(); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("next turn sent %q, want one request without the seal", got)
	}
}

// openaiUndecryptable is what OpenAI told hheei (#3887) of a web search
// another ChatGPT account ran, with code invalid_encrypted_content, and
// magpie passed on as "Codex: Encrypted content could not be decrypted or
// parsed.". It names no item.
const openaiUndecryptable = "Encrypted content could not be decrypted or parsed."

// accountSealer is the ChatGPT backend behind several of Codex's
// accounts, told apart by chatgpt-account-id. Each seals what it writes
// as "gAAAA-<account>-…" and reads no other account's seal: neither its
// sealed reasoning nor, with output token replay, the sealed copy of a
// message, a call or a web search. It refuses one sealed elsewhere as a
// 400, or in the stream after the backend's first events: reasoning as
// openaiUnverifiable, anything else as hheei was told. An account in out
// answers as that says instead.
type accountSealer struct {
	stream bool
	mu     sync.Mutex
	out    map[string]func(w http.ResponseWriter)
	asked  []sealedAsk
}

// sealedAsk is a request accountSealer was sent: the account, and its
// input as heldItems reads it.
type sealedAsk struct{ acct, input string }

func (c *accountSealer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	acct := r.Header.Get("chatgpt-account-id")
	var q struct {
		Input []sealedItem `json:"input"`
	}
	json.Unmarshal(b, &q)
	why := ""
	for _, it := range q.Input {
		if it.Enc != "" && !strings.HasPrefix(it.Enc, "gAAAA-"+acct+"-") && why != openaiUnverifiable {
			why = openaiUndecryptable
			if it.Type == "reasoning" {
				why = openaiUnverifiable
			}
		}
	}
	c.mu.Lock()
	c.asked = append(c.asked, sealedAsk{acct, heldItems(b)})
	out := c.out[acct]
	c.mu.Unlock()
	if out != nil {
		out(w)
		return
	}
	if why != "" && !c.stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"`+why+`","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`)
		return
	}
	lead := []string{
		`event: codex.rate_limits` + "\n" + `data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":3,"window_minutes":300,"reset_after_seconds":100}}}`,
		`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"r0","model":"gpt-5.5"}}`,
		`event: response.in_progress` + "\n" + `data: {"type":"response.in_progress","response":{"id":"r0","model":"gpt-5.5"}}`,
	}
	if why != "" {
		io.WriteString(w, sse(append(lead,
			`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"r0","status":"failed","error":{"code":"invalid_encrypted_content","message":"`+why+`"}}}`)...))
		return
	}
	// most of it read from the account's cache, which keeps the
	// conversation there while it is warm
	io.WriteString(w, sse(append(lead,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from `+acct+`"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":24000,"input_tokens_details":{"cached_tokens":20000},"output_tokens":5}}}`)...))
}

func (c *accountSealer) setOut(acct string, answer func(w http.ResponseWriter)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out == nil {
		c.out = map[string]func(w http.ResponseWriter){}
	}
	c.out[acct] = answer
}

// took is what was asked since the last call.
func (c *accountSealer) took() []sealedAsk {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.asked
	c.asked = nil
	return out
}

// on puts c behind Codex's sign-in, for the test.
func (c *accountSealer) on(t *testing.T) {
	t.Helper()
	up := httptest.NewServer(c)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
}

// heldItems is a Responses request's input as these tests check it: each
// item's type, a message's role, a call's call_id, a web search's query,
// and its seal.
func heldItems(body []byte) string {
	var q struct {
		Input []struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			CallID string `json:"call_id"`
			Action struct {
				Query string `json:"query"`
			} `json:"action"`
			Enc string `json:"encrypted_content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &q) != nil {
		return "unreadable"
	}
	var out []string
	for _, it := range q.Input {
		out = append(out, strings.Join(strings.Fields(strings.Join([]string{it.Type, it.Role, it.CallID, it.Action.Query, it.Enc}, " ")), " "))
	}
	return strings.Join(out, ", ")
}

// codexUser is the user's message in a Codex conversation.
func codexUser(text string) string {
	return `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}`
}

// replayedTurn is what turn n on acct leaves in a Codex conversation with
// output token replay on: its sealed reasoning, a web search it ran
// (hheei's), a call with its output, and its answer, the search, the call
// and the answer each with its sealed copy.
func replayedTurn(acct, n string) string {
	seal := func(what string) string { return `"encrypted_content":"gAAAA-` + acct + `-` + what + n + `"` }
	return `{"type":"reasoning","id":"rs_` + n + `","summary":[],"content":null,` + seal("r") + `},` +
		`{"type":"web_search_call","id":"ws_` + n + `","status":"completed","action":{"type":"search","query":"上海天气","sources":[{"type":"url","url":"https://www.weather.com.cn/weather/101020100.shtml"}]},` + seal("w") + `},` +
		`{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c` + n + `",` + seal("f") + `},` +
		`{"type":"function_call_output","call_id":"c` + n + `","output":"a.go"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"晴，22 度"}],` + seal("m") + `}`
}

// replayedHeld is replayedTurn as heldItems reads it: as acct sealed it,
// or with its reasoning gone and no seal left on the rest.
func replayedHeld(acct, n string, sealed bool) string {
	if !sealed {
		return "web_search_call 上海天气, function_call c" + n + ", function_call_output c" + n + ", message assistant"
	}
	s := " gAAAA-" + acct + "-"
	return "reasoning" + s + "r" + n + ", web_search_call 上海天气" + s + "w" + n + ", function_call c" + n + s + "f" + n +
		", function_call_output c" + n + ", message assistant" + s + "m" + n
}

// askThread is Codex signed in to ChatGPT as signedIn asking s at path for
// model, in its thread 3887, with the conversation's items.
func askThread(s *Server, path, model, signedIn string, items ...string) (int, string) {
	body := `{"model":"` + model + `","stream":true,"store":false,` +
		`"include":["reasoning.encrypted_content","output.encrypted_content"],"prompt_cache_key":"thread-3887",` +
		`"tools":[{"type":"web_search"},{"type":"function","name":"shell","parameters":{"type":"object"}}],` +
		`"input":[` + strings.Join(items, ",") + `]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", signedIn)
	req.Header.Set("session_id", "thread-3887")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Where a Codex conversation is asked through magpie's Codex accounts: one
// of Codex's own models signed in, the codex provider's model, and a
// routing group of it.
var codexAccountRoutes = []struct{ name, path, model string }{
	{"signed in", CodexPath + "/responses", "gpt-5.5"},
	{"magpie model", "/v1/responses", "codex/gpt-5.5"},
	{"group", "/v1/responses", "group/g"},
}

var sealRefusals = []struct {
	name   string
	stream bool
}{{"400", false}, {"in the stream", true}}

// A Codex conversation with a web search in it (hheei, #3887), and with
// output token replay on a sealed copy on each message and call, moves
// from one ChatGPT account to another as the first is used up or rate
// limited. The second can't read what the first sealed and says so; it is
// asked again with the reasoning left out and the seal taken off the
// rest, and the next turn goes to it so up front, its own seals on.
// Before, only the reasoning went, the search was refused again, and
// Codex got "Encrypted content could not be decrypted or parsed.".
func TestForeignSealedItemsMoveToAnotherAccount(t *testing.T) {
	for _, x := range codexSpent {
		if x.name != "429 json" && x.name != "response.failed rate_limit_exceeded" {
			continue
		}
		for _, on := range codexAccountRoutes {
			for _, refusal := range sealRefusals {
				t.Run(x.name+", "+on.name+", refused "+refusal.name, func(t *testing.T) {
					codexSignedIn(t, "spare@example.com")
					if err := provider.SetRouting("codex", provider.Ordered); err != nil {
						t.Fatal(err)
					}
					up := &accountSealer{stream: refusal.stream}
					up.on(t)
					if on.model == "group/g" {
						refusalGroup(t, "codex/gpt-5.5")
					}
					s := New()

					// the account that sealed it all reads it back as it came
					turn := []string{codexUser("上海天气怎么样"), replayedTurn("acct-1", "1"), codexUser("明天呢")}
					if code, body := askThread(s, on.path, on.model, "acct-1", turn...); code != 200 || !strings.Contains(body, "from acct-1") {
						t.Fatalf("first: %d %s", code, body)
					}
					if got := up.took(); len(got) != 1 || got[0] != (sealedAsk{"acct-1", "message user, " + replayedHeld("acct-1", "1", true) + ", message user"}) {
						t.Fatalf("acct-1 was sent %q", got)
					}

					up.setOut("acct-1", x.refuse)
					turn = append(turn, replayedTurn("acct-1", "2"), codexUser("后天呢"))
					code, body := askThread(s, on.path, on.model, "acct-1", turn...)
					if code != 200 || !strings.Contains(body, "from acct-2") {
						t.Fatalf("moved on: %d %s", code, body)
					}
					sealed := "message user, " + replayedHeld("acct-1", "1", true) + ", message user, " + replayedHeld("acct-1", "2", true) + ", message user"
					bare := "message user, " + replayedHeld("acct-1", "1", false) + ", message user, " + replayedHeld("acct-1", "2", false) + ", message user"
					if got := up.took(); len(got) != 3 || got[0].acct != "acct-1" || got[1] != (sealedAsk{"acct-2", sealed}) || got[2] != (sealedAsk{"acct-2", bare}) {
						t.Fatalf("asked %q", got)
					}
					if r := lastRoute(s); len(r.Tries) != 3 || r.Tries[1].Fail != failForeign || r.Tries[2].Status != 200 {
						t.Fatalf("tries: %+v", r.Tries)
					}

					// what acct-2 refused stays out of the next turn up front,
					// and what it sealed itself goes along
					turn = append(turn, replayedTurn("acct-2", "3"), codexUser("谢谢"))
					if code, body := askThread(s, on.path, on.model, "acct-1", turn...); code != 200 || !strings.Contains(body, "from acct-2") {
						t.Fatalf("next turn: %d %s", code, body)
					}
					if got := up.took(); len(got) != 1 || got[0] != (sealedAsk{"acct-2", bare + ", " + replayedHeld("acct-2", "3", true) + ", message user"}) {
						t.Fatalf("then asked %q", got)
					}
				})
			}
		}
	}
}

// The same conversation moved by its affinity going cold, as hheei's did
// ("why": "cold", two hours on): the account that answered it last did so
// over an hour ago, so routing's first is asked, and it reads none of the
// other's seals.
func TestForeignSealedItemsAfterAColdSwitch(t *testing.T) {
	for _, on := range codexAccountRoutes {
		for _, refusal := range sealRefusals {
			t.Run(on.name+", refused "+refusal.name, func(t *testing.T) {
				codexSignedIn(t, "spare@example.com")
				if err := provider.SetRouting("codex", provider.Ordered); err != nil {
					t.Fatal(err)
				}
				up := &accountSealer{stream: refusal.stream}
				up.on(t)
				if on.model == "group/g" {
					refusalGroup(t, "codex/gpt-5.5")
				}
				s := New()

				// acct-1's safety filter refuses the first turn, so acct-2
				// answers it, and the conversation stays there (#248)
				up.setOut("acct-1", refusing400)
				turn := []string{codexUser("上海天气怎么样")}
				if code, body := askThread(s, on.path, on.model, "acct-1", turn...); code != 200 || !strings.Contains(body, "from acct-2") {
					t.Fatalf("first: %d %s", code, body)
				}
				up.setOut("acct-1", nil)
				up.took()
				sticks.Lock()
				for k, st := range sticks.m {
					st.at = st.at.Add(-2 * time.Hour)
					sticks.m[k] = st
				}
				sticks.Unlock()

				turn = append(turn, replayedTurn("acct-2", "1"), codexUser("明天呢"))
				code, body := askThread(s, on.path, on.model, "acct-1", turn...)
				if code != 200 || !strings.Contains(body, "from acct-1") {
					t.Fatalf("gone cold: %d %s", code, body)
				}
				if r := lastRoute(s); r.Affinity == nil || r.Affinity.Why != "cold" {
					t.Fatalf("affinity %+v", r.Affinity)
				}
				bare := "message user, " + replayedHeld("acct-2", "1", false) + ", message user"
				if got := up.took(); len(got) != 2 || got[0] != (sealedAsk{"acct-1", "message user, " + replayedHeld("acct-2", "1", true) + ", message user"}) || got[1] != (sealedAsk{"acct-1", bare}) {
					t.Fatalf("asked %q", got)
				}

				turn = append(turn, replayedTurn("acct-1", "2"), codexUser("后天呢"))
				if code, body := askThread(s, on.path, on.model, "acct-1", turn...); code != 200 || !strings.Contains(body, "from acct-1") {
					t.Fatalf("next turn: %d %s", code, body)
				}
				if got := up.took(); len(got) != 1 || got[0] != (sealedAsk{"acct-1", bare + ", " + replayedHeld("acct-1", "2", true) + ", message user"}) {
					t.Fatalf("then asked %q", got)
				}
			})
		}
	}
}

// Codex's own sign-in alone, relayed as it came, now on another account
// (signed in again, or moved on once the first was spent,
// provider.SwitchWhenSpent): the backend's 400 for the first account's
// seals is answered by asking again without them, the web search's and
// the replayed copies too.
func TestForeignSealedItemsRelayedToAnotherSignIn(t *testing.T) {
	codexSignedIn(t)
	up := &accountSealer{}
	up.on(t)
	turn := []string{codexUser("上海天气怎么样"), replayedTurn("acct-1", "1"), codexUser("明天呢")}
	code, body := askThread(New(), CodexPath+"/responses", "gpt-5.5", "acct-2", turn...)
	if code != 200 || !strings.Contains(body, "from acct-2") {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.took(); len(got) != 2 || got[0] != (sealedAsk{"acct-2", "message user, " + replayedHeld("acct-1", "1", true) + ", message user"}) ||
		got[1] != (sealedAsk{"acct-2", "message user, " + replayedHeld("acct-1", "1", false) + ", message user"}) {
		t.Fatalf("asked %q", got)
	}
}

// What another account sealed is taken out with every other byte as it
// came: reasoning whole, and of a web search, a message and a call only
// their sealed copy, wherever it sits in the item. A compaction's seal
// stays for withoutCompaction. What was refused is left out of the
// conversation's later requests to who refused it, and only that.
func TestSealsTakenOutLeaveTheRest(t *testing.T) {
	fresh(t)
	in := `{"input":[` + codexUser("上海天气怎么样") + `,` +
		`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA-r1"},` +
		`{"encrypted_content":"gAAAA-w1","type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"上海天气"}},` +
		`{"type":"function_call","name":"shell","encrypted_content":"gAAAA-f1","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"},` +
		`{"type":"function_call_output","call_id":"c1","output":"a.go"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"晴"}],"encrypted_content":"gAAAA-m1"},` +
		`{"type":"compaction","encrypted_content":"gAAAA-c1"},{"type":"context_compaction","encrypted_content":"gAAAA-x1"}],"model":"gpt-5.5","stream":true}`
	want := `{"input":[` + codexUser("上海天气怎么样") + `,` +
		`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"上海天气"}},` +
		`{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"},` +
		`{"type":"function_call_output","call_id":"c1","output":"a.go"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"晴"}]},` +
		`{"type":"compaction","encrypted_content":"gAAAA-c1"},{"type":"context_compaction","encrypted_content":"gAAAA-x1"}],"model":"gpt-5.5","stream":true}`
	left, ok := withoutSeals([]byte(in))
	if !ok || string(left) != want {
		t.Fatalf("got %s", left)
	}

	// reasoning alone is taken out as it was before
	alone := `{"input":[` + codexUser("hi") + `,{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA-r1"},` +
		`{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}],"model":"gpt-5.5"}`
	was, _ := withoutKinds([]byte(alone), "reasoning")
	if got, ok := withoutSeals([]byte(alone)); !ok || string(got) != string(was) {
		t.Fatalf("reasoning alone: %s, was %s", got, was)
	}
	// nothing but a compaction sealed: nothing to take out here
	if got, ok := withoutSeals([]byte(`{"input":[{"type":"compaction","encrypted_content":"gAAAA-c1"},{"type":"message","role":"user","content":"hi"}]}`)); ok {
		t.Fatalf("compaction: %s", got)
	}

	// the next turn, with what the one that refused sealed itself
	refused("thread", "codex@b", []byte(in), left)
	own := `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"多云"}],"encrypted_content":"gAAAA-b2"}`
	next := strings.Replace(in, `],"model"`, own+`],"model"`, 1)
	if got, ok := withoutRefused("thread", "codex@b", []byte(next)); !ok || string(got) != strings.Replace(want, `],"model"`, own+`],"model"`, 1) {
		t.Fatalf("next turn: %s", got)
	}
	for _, other := range [][2]string{{"thread", "codex@a"}, {"another", "codex@b"}} {
		if got, ok := withoutRefused(other[0], other[1], []byte(next)); ok {
			t.Fatalf("%v: %s", other, got)
		}
	}
}
