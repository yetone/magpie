package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// chatgpt stands in for the ChatGPT backend behind CodexBase.
func chatgpt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	setHome(t, t.TempDir()) // no sign-ins but the test's
	forgetCodexLists(t)
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return up
}

func codexPost(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// One of Codex's own models goes on to the ChatGPT backend as it came, the
// sign-in with it; a summary magpie made earlier goes as the text it holds.
func TestCodexOwnModelPassesThrough(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []byte
	var head http.Header
	var path string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		head, path = r.Header, r.URL.Path
		w.Header()["Content-Type"] = nil // as the ChatGPT backend sends it
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	sum := magpieCompaction + base64.StdEncoding.EncodeToString([]byte("did X, next Y"))
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[
	  {"type":"compaction","encrypted_content":"`+sum+`"},
	  {"type":"compaction","encrypted_content":"openai-own"},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if code != 200 || !strings.Contains(body, `"input_tokens":9`) {
		t.Fatalf("%d %s", code, body)
	}
	if u := usage.Load(time.Time{}); len(u) != 1 || u[0].Input != 9 || u[0].Output != 2 || u[0].Provider != "openai" || u[0].ResponseID != "r1" {
		t.Errorf("usage %+v", u)
	}
	if path != "/backend-api/codex/responses" || head.Get("Authorization") != "Bearer chatgpt-token" || head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("upstream %s %v", path, head)
	}
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(got, &q)
	if len(q.Input) != 3 || q.Input[0]["type"] != "message" || !strings.Contains(string(got), "did X, next Y") ||
		q.Input[1]["encrypted_content"] != "openai-own" {
		t.Errorf("input: %s", got)
	}
}

// codexChatGPTAuth signs this test's Codex in to a synthetic ChatGPT
// account. CODEX_HOME is the only auth.json it reads.
func codexChatGPTAuth(t *testing.T, email string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", dir)
	claims, err := json.Marshal(map[string]any{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := func(payload []byte) string {
		return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	}
	auth, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"id_token":      token(claims),
			"access_token":  token(exp),
			"refresh_token": "rt-synthetic",
			"account_id":    "acct-synthetic",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}

// A turn relayed on Codex's own ChatGPT sign-in is that codex account's
// in the ledger. Recorded as openai with no account, the card's window
// (keyed codex + the email) never sees it. The Routing view still calls
// the seat openai, and the row is still not a lead's answerer (#1367).
func TestCodexOwnModelRecordedAsCodexAccount(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	codexChatGPTAuth(t, "me@example.com")
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "lead-own")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	u := usage.Load(time.Time{})
	if len(u) != 1 || u[0].Provider != "codex" || u[0].ProviderAccount != "me@example.com" || u[0].Input != 9 || u[0].Output != 2 {
		t.Fatalf("usage %+v", u)
	}
	if strings.Contains(u[0].Host, " as ") {
		t.Fatalf("relay host names an account, so it would count as a routed answer: %q", u[0].Host)
	}
	if a := loggedAnswers("lead-own"); len(a) != 0 {
		t.Fatalf("own sign-in answered a lead: %+v", a)
	}
	st := s.Trace(t.Context(), 0, 0)
	if len(st.Routes) != 1 || len(st.Routes[0].Usage) != 1 || st.Routes[0].Usage[0].Provider != "openai" {
		t.Fatalf("routing view %+v", st.Routes)
	}
}

// An API-key call stays openai with no account, even while a ChatGPT
// sign-in is on disk: that spend is not the subscription card's.
func TestCodexAPIKeyRecordedAsOpenAI(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":4,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := codexAPIBase
	codexAPIBase = up.URL + "/v1"
	t.Cleanup(func() { codexAPIBase = was })
	codexChatGPTAuth(t, "me@example.com")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-synthetic")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	u := usage.Load(time.Time{})
	if len(u) != 1 || u[0].Provider != "openai" || u[0].ProviderAccount != "" || u[0].Input != 4 {
		t.Fatalf("usage %+v", u)
	}
}

// A Codex relay OpenAI refuses (an API key it turns away) is still the
// request its Routing row is: the call it records carries the route, so
// the Routing page's "Request and response" opens it while the recent
// calls have it, instead of saying it is gone (#2481, ggbdpq).
func TestCodexKeyRefusedCarriesItsRoute(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: sk-synth***","type":"invalid_request_error","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(up.Close)
	was := codexAPIBase
	codexAPIBase = up.URL + "/v1"
	t.Cleanup(func() { codexAPIBase = was })
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-synthetic")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	routeHasItsCall(t, s, http.StatusUnauthorized)
}

// A Codex relay the backend fails with nothing said (Codex shows it as
// "Unknown error" alone) is the request its Routing row is too.
func TestCodexEmptyFailureCarriesItsRoute(t *testing.T) {
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	routeHasItsCall(t, s, http.StatusInternalServerError)
}

// A compaction failed with nothing said reaches the same exit with no
// route open: Codex is still told what happened and the call is still
// recorded, with no route to carry.
func TestCodexCompactEmptyFailureRecordsNoRoute(t *testing.T) {
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","input":[]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "this compaction") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	calls := s.Recent()
	if len(calls) != 1 || calls[0].Status != http.StatusInternalServerError || calls[0].Route != 0 {
		t.Fatalf("recorded %+v", calls)
	}
}

// routeHasItsCall checks that the one route the trace has finds its call,
// the one that answered status.
func routeHasItsCall(t *testing.T, s *Server, status int) {
	t.Helper()
	st := s.Trace(t.Context(), 0, 0)
	if len(st.Routes) != 1 {
		t.Fatalf("routing view has %d routes, want the turn", len(st.Routes))
	}
	c, ok := s.RouteCall(st.Routes[0].ID)
	if !ok {
		t.Fatalf("route %d: no call in the ring, so Request and response says the request is gone though its call is one of the last 40", st.Routes[0].ID)
	}
	if c.Status != status {
		t.Errorf("route %d is the call %d (%d), not the failure (%d)", st.Routes[0].ID, c.ID, c.Status, status)
	}
}

// A compaction signed by another account is that account's, not the
// sign-in the request would have relayed on. A request naming another
// ChatGPT account than the sign-in magpie reads (a Codex in WSL, or
// under another CODEX_HOME) is not put on that sign-in, and with no
// sign-in to name it stays openai, so no row is half a codex account.
func TestCodexServedNamesTheAccountThatSigned(t *testing.T) {
	on := &provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "spare@example.com"}}
	id, acct := codexServed(provider.CodexBase, http.Header{}, on)
	if id != "codex" || acct != "spare@example.com" {
		t.Fatalf("stand-in: %s %s", id, acct)
	}
	setHome(t, t.TempDir())
	codexChatGPTAuth(t, "me@example.com")
	h := http.Header{}
	h.Set("chatgpt-account-id", "acct-synthetic")
	if id, acct = codexServed(provider.CodexBase, h, nil); id != "codex" || acct != "me@example.com" {
		t.Fatalf("its own account: %s %s", id, acct)
	}
	h.Set("chatgpt-account-id", "acct-elsewhere")
	if id, acct = codexServed(provider.CodexBase, h, nil); id != "openai" || acct != "" {
		t.Fatalf("another account's token: %s %s", id, acct)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	provider.ForgetAccounts()
	if id, acct = codexServed(provider.CodexBase, http.Header{}, nil); id != "openai" || acct != "" {
		t.Fatalf("no sign-in: %s %s", id, acct)
	}
}

// Codex's own sign-in relay is not a lead's answerer, under the name it
// was logged by before (openai) or the codex account it is logged by now.
// A row an account answered — host "as email", or another provider, or
// /v1 — still is. One codex row is not counted twice.
func TestCodexOwnRelayAnswersNoLead(t *testing.T) {
	setHome(t, t.TempDir())
	// usage.Path follows the first home a process reads; -count keeps
	// writing there, so each run's sessions are its own.
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	now := time.Now()
	row := func(name, prov, account, host, endpoint string) string {
		t.Helper()
		session := name + "-" + stamp
		usage.Append(usage.Record{
			Time: now, Agent: "codex", Provider: prov, Host: host, ProviderAccount: account,
			Model: "gpt-5.5", Input: 9, Output: 2, Status: 200, Endpoint: endpoint, Session: session,
		})
		return session
	}
	relay := row("lead-relay", "openai", "", provider.HostOf(provider.CodexBase), CodexPath+"/responses")
	row("lead-relay", "codex", "me@example.com", provider.HostOf(provider.CodexBase), CodexPath+"/responses")
	if a := loggedAnswers(relay); len(a) != 0 {
		t.Fatalf("own sign-in answered a lead: %+v", a)
	}
	routed := row("lead-routed", "codex", "me@example.com", "chatgpt.com as me@example.com", CodexPath+"/responses")
	if a := loggedAnswers(routed); len(a) != 1 || a[0].provider != "codex" || a[0].who != "codex@me@example.com" {
		t.Fatalf("routed account: %+v", a)
	}
	v1 := row("lead-v1", "codex", "me@example.com", "chatgpt.com as me@example.com", "/v1/responses")
	if a := loggedAnswers(v1); len(a) != 1 || a[0].who != "codex@me@example.com" {
		t.Fatalf("/v1 account: %+v", a)
	}
	other := row("lead-other", "sub2api", "", "relay.example.com", CodexPath+"/responses")
	if a := loggedAnswers(other); len(a) != 1 || a[0].provider != "sub2api" {
		t.Fatalf("relay: %+v", a)
	}
}

// A turn on one of Codex's own models shows in the Routing view's live
// trace, as those on magpie's do.
func TestCodexOwnModelTraced(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "codex-conversation")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"thread_title","forked_from_thread_id":"main-conversation"}`)
	s.Handler().ServeHTTP(rec, req)
	st := s.Trace(t.Context(), 0, 0)
	if len(st.Routes) != 1 {
		t.Fatalf("routes %+v", st.Routes)
	}
	r := st.Routes[0]
	if r.Session != "codex-conversation" || r.ParentSession != "main-conversation" || r.Kind != "thread_title" || len(r.Usage) != 1 || r.Usage[0].Provider != "openai" || r.Usage[0].Input != 9 || r.Usage[0].Output != 2 {
		t.Fatalf("session accounting: %+v", r)
	}
	if !r.Done || r.Status != 200 || r.Model != "gpt-5.5" || r.Tokens != 11 ||
		len(r.Order) != 1 || r.Order[0].Who != "Codex's own sign-in" || len(r.Tries) != 1 || !r.Tries[0].Done || r.Tries[0].Status != 200 {
		t.Errorf("route %+v", r)
	}
	if st.Totals.Requests != 1 {
		t.Errorf("totals %+v", st.Totals)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].RouteID != r.ID || r.ID == 0 {
		t.Fatalf("usage: %+v, route %d", recs, r.ID)
	}
}

func TestCodexOwnModelOmitsNonemptyReasoning(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1"}}`))
	})
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[
	  {"type":"reasoning","content":[{"type":"reasoning_text","text":"foreign thought"}],"encrypted_content":"foreign-token"},
	  {"type":"reasoning","content":[],"encrypted_content":"openai-own"},
	  {"type":"reasoning","encrypted_content":"openai-own-without-content"},
	  {"type":"function_call_output","call_id":"call_1","output":"result"},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	if code != 200 || !strings.Contains(body, `response.completed`) {
		t.Fatalf("%d %s", code, body)
	}
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(got, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 4 || q.Input[0]["encrypted_content"] != "openai-own" ||
		q.Input[1]["encrypted_content"] != "openai-own-without-content" ||
		q.Input[2]["type"] != "function_call_output" || q.Input[3]["role"] != "user" ||
		strings.Contains(string(got), "foreign-token") {
		t.Errorf("upstream input: %s", got)
	}
}

// Back on one of Codex's own models after another vendor's: its reasoning,
// an id with nothing sealed in it, would be looked up by OpenAI and not
// found, so it doesn't go. An item OpenAI still refuses is taken out and
// the rest asked again.
func TestCodexOwnModelAfterAnotherVendor(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got [][]byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, b)
		if bytes.Contains(b, []byte(`cmp_0217`)) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"The encrypted content for item cmp_0217abc could not be verified. Reason: Encrypted content could not be decrypted or parsed.","code":"invalid_encrypted_content"}}`)
			return
		}
		if bytes.Contains(b, []byte(`rs_other`)) {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"Item with id 'rs_other' not found. Items are not persisted when `+"`store`"+` is set to false."}}`)
			return
		}
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1"}}`))
	})
	code, body := codexPost(t, `{"model":"gpt-6-luna","stream":true,"store":false,"input":[
	  {"type":"reasoning","id":"rs_0217903544582750000","summary":[{"type":"summary_text","text":"volc thought"}],"encrypted_content":null},
	  {"type":"reasoning","id":"rs_other","summary":[],"encrypted_content":"sealed-elsewhere"},
	  {"type":"compaction","id":"cmp_0217abc","encrypted_content":"not-openai"},
	  {"type":"reasoning","id":"rs_own","summary":[],"encrypted_content":"openai-own"},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if code != 200 || !strings.Contains(body, `response.completed`) {
		t.Fatalf("%d %s", code, body)
	}
	if len(got) != 3 || bytes.Contains(got[0], []byte("volc thought")) {
		t.Fatalf("%d asks, first: %s", len(got), got[0])
	}
	last := string(got[2])
	if strings.Contains(last, "cmp_0217abc") || strings.Contains(last, "rs_other") ||
		!strings.Contains(last, "openai-own") || !strings.Contains(last, "go on") {
		t.Errorf("last ask: %s", last)
	}
}

// A refusal that names nothing magpie can take out goes to Codex as it came.
func TestCodexOwnModelRefusalPassesThrough(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	asks := 0
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		asks++
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"The model 'gpt-x' does not exist"}}`)
	})
	code, body := codexPost(t, `{"model":"gpt-x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if code != 404 || asks != 1 || !strings.Contains(body, "does not exist") {
		t.Errorf("%d asks=%d %s", code, asks, body)
	}
}

func TestCodexMagpieModelKeepsReasoningInput(t *testing.T) {
	body := `{"model":"fake/m1","input":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"thought"}],"encrypted_content":"foreign-token"}]}`
	got, compact := codexInput([]byte(body), true)
	if compact || string(got) != body {
		t.Errorf("magpie input: %s, compact: %t", got, compact)
	}
}

func TestCodexOwnModelCompactionPassesThrough(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got [][]byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, b)
		if !streamOf(b) {
			http.Error(w, `{"detail":"Stream must be set to true"}`, http.StatusBadRequest)
			return
		}
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_openai","status":"completed","output":[{"type":"compaction","id":"cmp_openai","encrypted_content":"openai-own"}]}}`))
	})
	original := `{"model":"gpt-6-sol","stream":true,"tools":[{"type":"function","name":"shell"}],"input":[{"type":"message","role":"user","content":"remember this"},{"type":"compaction_trigger"}]}`
	code, body := codexPost(t, original)
	if code != 200 || !strings.Contains(body, `"id":"cmp_openai"`) || len(got) != 1 || string(got[0]) != original {
		t.Fatalf("own compact: %d %s; upstream %q", code, body, got)
	}

	sum := magpieCompaction + base64.StdEncoding.EncodeToString([]byte("prior summary"))
	code, body = codexPost(t, `{"model":"gpt-6-sol","stream":true,"tools":[{"type":"function","name":"shell"}],"input":[{"type":"compaction","encrypted_content":"`+sum+`"},{"type":"compaction_trigger"}]}`)
	if code != 200 || len(got) != 2 {
		t.Fatalf("mixed compact: %d %s; %d upstream requests", code, body, len(got))
	}
	var q struct {
		Stream bool             `json:"stream"`
		Tools  []map[string]any `json:"tools"`
		Input  []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(got[1], &q); err != nil || !q.Stream || len(q.Tools) != 1 || len(q.Input) != 2 ||
		q.Input[0]["type"] != "message" || q.Input[1]["type"] != "compaction_trigger" ||
		!strings.Contains(string(got[1]), "prior summary") || strings.Contains(string(got[1]), codexCompactPrompt) {
		t.Errorf("mixed compact upstream: %s (%v)", got[1], err)
	}
}

// A magpie model is served by magpie, whatever sign-in Codex sent.
func TestCodexMagpieModelServed(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	code, body := codexPost(t, `{"model":"fake/m1","stream":true,"input":"hello"}`)
	if code != 200 || !strings.Contains(body, `"delta":"hi"`) {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(string(f.got), `"model":"m1"`) {
		t.Errorf("upstream got %s", f.got)
	}
}

// Codex compresses what it sends the ChatGPT backend; magpie reads it all
// the same.
func TestCodexReadsCompressedBody(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	enc, _ := zstd.NewWriter(nil)
	z := enc.EncodeAll([]byte(`{"model":"fake/m1","stream":true,"input":"hello"}`), nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(z))
	req.Header.Set("Content-Encoding", "zstd")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delta":"hi"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Compacting a magpie model's conversation: the model summarises, and Codex
// gets the compaction item it asked for, holding the summary.
func TestCodexCompactsMagpieModel(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"SUMM"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"ARY"},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("third-party compaction reached OpenAI")
		w.WriteHeader(400)
	})
	code, body := codexPost(t, `{"model":"fake/new-model","stream":true,
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},
	    {"type":"compaction_trigger"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(f.got), `"tools"`) || !strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("upstream got %s", f.got)
	}
	var item map[string]any
	completed := false
	for _, e := range events(body) {
		switch e["type"] {
		case "response.output_item.done":
			item = e["item"].(map[string]any)
		case "response.completed":
			completed = true
		}
	}
	enc, _ := item["encrypted_content"].(string)
	b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, magpieCompaction))
	if item["type"] != "compaction" || !strings.HasPrefix(enc, magpieCompaction) || string(b) != "SUMMARY" || !completed {
		t.Errorf("events: %s", body)
	}
	code, _ = codexPost(t, `{"model":"fake/new-model","stream":true,"input":[{"type":"compaction","encrypted_content":"`+enc+`"},{"type":"message","role":"user","content":"continue"}]}`)
	if code != 200 || !strings.Contains(string(f.got), "SUMMARY") || !strings.Contains(string(f.got), codexSummaryPrefix) || strings.Contains(string(f.got), magpieCompaction) {
		t.Errorf("compacted conversation was not restored: %d %s", code, f.got)
	}
}

// The model list is the backend's for this sign-in, then magpie's.
func TestCodexModelList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var auth string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1}]}`)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models?client_version=0.155.1", nil)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	var slugs []string
	var fake map[string]any
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		slugs = append(slugs, slug)
		if slug == "fake/m1" {
			fake = m
		}
	}
	// Other sign-ins found on the machine (Cursor's, say) may follow. The
	// ETag is the backend's with magpie's list in it.
	tag := codexcat.Tag(provider.CodexListed())
	if rec.Code != 200 || len(slugs) < 2 || slugs[0] != "gpt-5.5" || fake == nil || fake["base_instructions"] == "" ||
		rec.Header().Get("ETag") != `"v1+magpie-`+tag+`"` || auth != "Bearer chatgpt-token" {
		t.Errorf("%d %v %q %q", rec.Code, slugs, rec.Header().Get("ETag"), auth)
	}
	for _, s := range slugs {
		if strings.HasPrefix(s, "codex/") {
			t.Errorf("Codex's own models twice: %v", slugs)
		}
	}
}

// The user's model picks on the ChatGPT account narrow the backend's own
// list too: Codex is shown only the native models kept, not every one the
// account can reach.
func TestCodexModelListNarrowedByPicks(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"slug":"gpt-6-sol","priority":1},{"slug":"gpt-5.5","priority":2},{"slug":"gpt-5-codex","priority":3}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models", nil)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	var native []string
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		// the backend's own models are the gpt-* ones; magpie's added
		// entries carry their own slugs and stay
		if strings.HasPrefix(slug, "gpt-") {
			native = append(native, slug)
		}
	}
	if rec.Code != 200 || len(native) != 1 || native[0] != "gpt-6-sol" {
		t.Errorf("native models after picks = %v (want just gpt-6-sol); code %d", native, rec.Code)
	}
}

// The picks narrow only the models a picker shows. A hidden entry of the
// backend's, such as codex-auto-review, isn't one: no picker lists it, and
// Codex's auto-review runs on it when the list has it. Taken out, the review
// fell back to the conversation's model at low effort, which a relay model
// charged to the relay (#1460).
func TestCodexModelListPicksKeepHidden(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"slug":"gpt-6-sol","priority":1,"visibility":"list"},{"slug":"gpt-5.5","priority":2,"visibility":"list"},{"slug":"codex-auto-review","priority":3,"visibility":"hide"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models", nil)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	got := map[string]bool{}
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		got[slug] = true
	}
	if rec.Code != 200 || !got["gpt-6-sol"] || got["gpt-5.5"] || !got["codex-auto-review"] {
		t.Errorf("after picks: %v (want gpt-6-sol and codex-auto-review, not gpt-5.5); code %d", got, rec.Code)
	}
}

// A native model taken out of Codex's list on the Agents page is dropped
// from the backend's list too, and the ETag changes so Codex asks again.
func TestCodexModelListHidesOnAgentsPage(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5", "gpt-5-codex"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1},{"slug":"gpt-5-codex","priority":2}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	list := func() ([]string, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		var native []string
		for _, m := range got.Models {
			if slug, _ := m["slug"].(string); strings.HasPrefix(slug, "gpt-") {
				native = append(native, slug)
			}
		}
		return native, rec.Header().Get("ETag")
	}
	all, before := list()
	if len(all) != 2 {
		t.Fatalf("native models %v", all)
	}
	var id string
	for _, e := range provider.Catalog() {
		if acc := e.Provider.Account; acc != nil && acc.Agent == "codex" && e.Model == "gpt-5-codex" {
			id = e.ID
		}
	}
	if id == "" {
		t.Fatal("no catalog entry for the account's gpt-5-codex")
	}
	if err := provider.SetHiddenModels("codex", []string{id}); err != nil {
		t.Fatal(err)
	}
	native, after := list()
	if len(native) != 1 || native[0] != "gpt-5.5" || after == before {
		t.Errorf("native %v, ETag %q then %q", native, before, after)
	}
}

// The codex provider set to "Only through routing groups", or a group that
// keeps its members out of the lists (Group.Unlists), keeps the ChatGPT
// account's own models out of other agents' lists, never out of Codex's own:
// they are Codex's models, and the Agents page offers them in Codex's
// picker. #1602 dropped them, and 士奇 (Discord, 10-28) turned a group on
// to find GPT in the picker and gone from the Codex app. Codex's list, its
// "Codex" section on the Agents page, is where they are hidden from Codex,
// and that still holds.
func TestCodexModelListKeepsOwnForGroups(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[{"slug":"gpt-6-sol","priority":1,"visibility":"list"},{"slug":"gpt-5.5","priority":2,"visibility":"list"},{"slug":"codex-auto-review","priority":3,"visibility":"hide"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	list := func() (map[string]bool, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		out := map[string]bool{}
		for _, m := range got.Models {
			slug, _ := m["slug"].(string)
			out[slug] = true
		}
		return out, rec.Header().Get("ETag")
	}
	got, before := list()
	if !got["gpt-6-sol"] || !got["gpt-5.5"] || !got["codex-auto-review"] {
		t.Fatalf("listed: %v", got)
	}
	var id string
	for _, e := range provider.Catalog() {
		if provider.CodexOwn(e) && e.Model == "gpt-5.5" {
			id = e.ID
		}
	}
	if id == "" {
		t.Fatal("no catalog entry for the account's gpt-5.5")
	}

	// a group keeping its members out of the lists leaves them in Codex's
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{id}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := list(); !got["gpt-6-sol"] || !got["gpt-5.5"] || !got["codex-auto-review"] {
		t.Errorf("group unlisting gpt-5.5: %v", got)
	}

	// and so does the provider only through routing groups
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol", "gpt-5.5"}, Unlisted: true}); err != nil {
		t.Fatal(err)
	}
	got, unlisted := list()
	if !got["gpt-6-sol"] || !got["gpt-5.5"] || !got["codex-auto-review"] {
		t.Errorf("only through routing groups: %v", got)
	}
	// other agents still don't see them
	claude, _ := provider.ListedFor("claude")
	for _, e := range claude {
		if provider.CodexOwn(e) {
			t.Errorf("claude lists %s", e.ID)
		}
	}

	// spawn_agent names them to a lead, as Codex's list does: from
	// models_cache.json, which Codex wrote from a list before
	os.WriteFile(filepath.Join(os.Getenv("HOME"), ".codex", "models_cache.json"), []byte(`{"etag":"\"v1\"","models":[{"slug":"gpt-6-sol","visibility":"list","priority":1},{"slug":"gpt-5.5","visibility":"list","priority":2}]}`), 0o600)
	offered := map[string]bool{}
	for _, c := range spawnChoices(httptest.NewRequest("POST", CodexPath+"/responses", nil), "group/mine", true) {
		offered[c.slug] = true
	}
	if !offered["gpt-6-sol"] || !offered["gpt-5.5"] {
		t.Errorf("spawn_agent offers %v", offered)
	}

	// hidden in Codex's list, gpt-5.5 goes from the app, and the ETag says so
	if err := provider.SetHiddenModels("codex", []string{id}); err != nil {
		t.Fatal(err)
	}
	got, hidden := list()
	if !got["gpt-6-sol"] || got["gpt-5.5"] || !got["codex-auto-review"] || hidden == unlisted || hidden == before {
		t.Errorf("hidden in Codex's list: %v, ETag %q, %q then %q", got, before, unlisted, hidden)
	}
}

// A group listing alone (Group.Unlists, 成员模型不单独列出) keeps a signed-in
// subscription's models out of /v1/models for an agent's key too, not only
// for a client that is no agent (shaoyang. on Discord, 11-03: the list
// fetched still had every model of the group). Codex keeps its own
// ChatGPT models, by name too (/v1/models/{id}); with its models listed too
// they are listed again.
func TestGroupAloneKeepsSubscriptionModelsOffAgentsModelList(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, e := range provider.Catalog() {
		if provider.CodexOwn(e) {
			refs = append(refs, e.ID)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("the account's models: %v", refs)
	}
	h := New().Handler()
	list := func(key string) map[string]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", key, rec.Code, rec.Body)
		}
		var got struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		out := map[string]bool{}
		for _, m := range got.Data {
			out[m.ID] = true
		}
		return out
	}
	found := func(key, id string) bool {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models/"+id, nil)
		req.Header.Set("x-api-key", key)
		h.ServeHTTP(rec, req)
		return rec.Code == 200
	}
	g := provider.Group{ID: "mine", Name: "Mine", Members: refs, Unlists: true}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", Token, Token + "-hermes", Token + "-claude", Token + "-opencode"} {
		got := list(key)
		if !got["group/mine"] {
			t.Errorf("%q: the group isn't listed: %v", key, got)
		}
		for _, ref := range refs {
			if got[ref] {
				t.Errorf("%q: %s listed beside the group listing alone", key, ref)
			}
		}
	}
	if found(Token+"-hermes", refs[0]) {
		t.Errorf("/v1/models/%s found for Hermes", refs[0])
	}
	if got := list(Token + "-codex"); !got[refs[0]] || !got[refs[1]] {
		t.Errorf("Codex lost its own models: %v", got)
	}
	if !found(Token+"-codex", refs[0]) {
		t.Errorf("/v1/models/%s not found for Codex", refs[0])
	}
	g.Unlists = false
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", Token + "-hermes"} {
		if got := list(key); !got[refs[0]] || !got[refs[1]] || !got["group/mine"] {
			t.Errorf("%q, its models too: %v", key, got)
		}
	}
}

// A reply's X-Models-Etag carries magpie's list too: Codex refetches its
// model list on a new one, and only on the backend's it never would when a
// provider was added.
func TestCodexModelsEtagTagged(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Models-Etag", `W/"v1"`)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1"}}`))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	tag := codexcat.Tag(provider.CodexListed())
	if got := rec.Header().Get("X-Models-Etag"); got != `W/"v1+magpie-`+tag+`"` || !codexcat.Tagged(got, tag) {
		t.Errorf("%d X-Models-Etag %q", rec.Code, got)
	}
}

// A routing group in Codex's model list is named as one, not as its first
// member's provider, which would read as that provider's own model.
func TestCodexModelsNameGroups(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"models":[]}`) })
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", CodexPath+"/models", nil))
	var list struct {
		Models []struct {
			Slug string `json:"slug"`
			Name string `json:"display_name"`
		} `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	names := map[string]string{}
	for _, m := range list.Models {
		names[m.Slug] = m.Name
	}
	if names["group/g"] != "G · routing group" || !strings.HasSuffix(names["fake/m1"], " · Fake") {
		t.Fatalf("%v", names)
	}
}

func TestCodexOwnModelKeepsReasoningWithNullContent(t *testing.T) {
	body := `{"model":"gpt-5.5","input":[{"type":"reasoning","content":null,"encrypted_content":"openai-own"}]}`
	if got, _ := codexInput([]byte(body), false); string(got) != body {
		t.Errorf("input: %s", got)
	}
}

// Codex signed in with an API key OpenAI refuses, asked for one of its own
// models: the refusal says what happened and what to do.
func TestCodexOwnModelKeyRefused(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: sk-A2sz0****owqK.","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(up.Close)
	was := codexAPIBase
	codexAPIBase = up.URL
	t.Cleanup(func() { codexAPIBase = was })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-astra","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-relay-key")
	New().Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 401 || !strings.Contains(body, "Incorrect API key provided") || !strings.Contains(body, "pick one of magpie's models") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

// Catalog visibility must not decide whether a namespaced model goes to OpenAI.
func TestCodexNamespacedRouting(t *testing.T) {
	for _, tc := range []struct{ name, model, wantModel string }{
		{"known", "fake/m1", "m1"},
		{"outside_catalog", "fake/new-model", "new-model"},
		{"renamed_provider", "old-fake/m1", "m1"},
		{"nested_model", "fake/vendor/model", "vendor/model"},
		{"whitespace", " fake/m1 ", "m1"},
		{"group", "group/audit", "m1"},
		{"unknown_provider", "missing/m1", ""},
		{"unknown_group", "group/missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t, ctype: "application/json", reply: `{"id":"r1","output":[]}`}
			setup(t, provider.Responses, f)
			p, err := provider.Find("fake")
			if err != nil {
				t.Fatal(err)
			}
			p.Was, p.Unlisted = []string{"old-fake"}, true
			if err := provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{ID: "audit", Members: []string{"fake/m1"}}); err != nil {
				t.Fatal(err)
			}
			up := chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("namespaced request reached OpenAI")
				w.WriteHeader(400)
			})
			was := codexAPIBase
			codexAPIBase = up.URL + "/v1"
			t.Cleanup(func() { codexAPIBase = was })
			for _, auth := range []string{"Bearer chatgpt-token", "Bearer sk-test"} {
				f.calls = 0
				r := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(mustJSON(map[string]any{"model": tc.model, "input": "hi"})))
				r.Header.Set("Authorization", auth)
				w := httptest.NewRecorder()
				New().Handler().ServeHTTP(w, r)
				if tc.wantModel == "" {
					if w.Code != 404 || f.calls != 0 {
						t.Fatalf("%d %s, calls %d", w.Code, w.Body, f.calls)
					}
				} else if w.Code != 200 || f.calls != 1 || modelOf(f.got) != tc.wantModel {
					t.Fatalf("%d %s, upstream %s, calls %d", w.Code, w.Body, f.got, f.calls)
				}
			}
		})
	}
}

func TestCodexNativeModelStaysNative(t *testing.T) {
	f := &fake{t: t}
	setup(t, provider.Responses, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	// A third party also serves this bare slug. Codex still means its own model.
	p.Models = []string{"gpt-future"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	var calls int
	up := chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if modelOf(b) != "gpt-future" {
			t.Errorf("upstream model: %s", b)
		}
		io.WriteString(w, `{"id":"r1","output":[]}`)
	})
	was := codexAPIBase
	codexAPIBase = up.URL + "/v1"
	t.Cleanup(func() { codexAPIBase = was })
	for _, auth := range []string{"Bearer chatgpt-token", "Bearer sk-test"} {
		for _, path := range []string{"/responses", "/responses/compact"} {
			r := httptest.NewRequest("POST", CodexPath+path, strings.NewReader(`{"model":" gpt-future ","input":"hi"}`))
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			New().Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		}
	}
	if calls != 4 || f.calls != 0 {
		t.Fatalf("OpenAI calls %d, third-party calls %d", calls, f.calls)
	}
}

func TestCodexThirdPartyCompactEndpointRejected(t *testing.T) {
	f := &fake{t: t}
	setup(t, provider.Responses, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("third-party compaction reached OpenAI")
		w.WriteHeader(400)
	})
	code, body := post(t, CodexPath+"/responses/compact", `{"model":"fake/m1","input":[]}`)
	if code != 400 || !strings.Contains(body, "not supported") || f.calls != 0 {
		t.Fatalf("%d %s, calls %d", code, body, f.calls)
	}
}

func TestCodexBackendKeepsNativeSession(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("Session_id", "native-thread")
	req.Header.Set(SessionHeader, "routing-override")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	rs := usage.Load(time.Time{})
	if len(rs) != 1 || rs[0].NativeSession != "native-thread" || rs[0].Session != "routing-override" {
		t.Fatalf("lost native session: %+v", rs)
	}
}

// A codex provider switched off narrows nothing: it serves no agent
// anything, so the account's own list is left whole, its picks kept for
// when it is switched on again.
func TestCodexModelListNotNarrowedWhileOff(t *testing.T) {
	codexSignedIn(t)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol"}, Off: true}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"slug":"gpt-6-sol","priority":1},{"slug":"gpt-5.5","priority":2}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()

	native := func() []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		var list struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &list)
		var out []string
		for _, m := range list.Models {
			slug, _ := m["slug"].(string)
			if strings.HasPrefix(slug, "gpt-") {
				out = append(out, slug)
			}
		}
		return out
	}
	if got := native(); len(got) != 2 {
		t.Errorf("switched off, the account's own models = %v (want both kept whole)", got)
	}
	if err := provider.SetOff("codex", false); err != nil {
		t.Fatal(err)
	}
	if got := native(); len(got) != 1 || got[0] != "gpt-6-sol" {
		t.Errorf("switched on, the picks narrow again = %v (want just gpt-6-sol)", got)
	}
}

// Body-only title metadata and ordinary Luna Reserve turns have the same kind
// in the native Codex trace, request log and usage ledger.
func TestCodexOwnModelKindAccounting(t *testing.T) {
	for _, tc := range []struct{ name, metadata, reserve, kind, parent string }{
		{"reserve", `{"thread_source":"user"}`, "1", "luna_reserve", ""},
		{"body title", `{"thread_source":"thread_title","parent_thread_id":"main"}`, "", "thread_title", "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t, provider.Chat, &fake{t: t})
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
			})
			body, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "stream": true, "input": "hi",
				"client_metadata": map[string]string{"x-codex-turn-metadata": tc.metadata}})
			s := New()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("session_id", "child")
			req.Header.Set("x-openai-codex-luna-reserve", tc.reserve)
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			routes := s.Trace(t.Context(), 0, 0).Routes
			if len(routes) != 1 || routes[0].Kind != tc.kind || routes[0].ParentSession != tc.parent {
				t.Fatalf("trace: %+v", routes)
			}
			if calls := s.Recent(); len(calls) != 1 || calls[0].Kind != tc.kind {
				t.Fatalf("request log: %+v", calls)
			}
			recs := usage.Load(time.Time{})
			if len(recs) != 1 || recs[0].Kind != tc.kind {
				t.Fatalf("usage: %+v", recs)
			}
		})
	}
}

// A turn on one of Codex's own models logs the reasoning Codex asked for,
// as the others' turns do: the request log's reasoning column was blank
// for every one (Karen, #feedback), its body sent zstd as Codex sends it.
func TestCodexOwnModelLogsEffort(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []byte
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	enc, _ := zstd.NewWriter(nil)
	z := enc.EncodeAll([]byte(`{"model":"gpt-5.5","stream":true,"input":"hi","reasoning":{"effort":"xhigh","summary":"auto"}}`), nil)
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(z))
	req.Header.Set("Content-Encoding", "zstd")
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(string(got), `"effort":"xhigh"`) {
		t.Fatalf("%d %s, sent %s", rec.Code, rec.Body, got)
	}
	if u := usage.Load(time.Time{}); len(u) != 1 || u[0].Effort != "xhigh" {
		t.Errorf("usage %+v", u)
	}
	if rs := s.Trace(t.Context(), 0, 0).Routes; len(rs) != 1 || rs[0].Effort != "xhigh" || rs[0].Tries[0].Effort != "xhigh" {
		t.Errorf("trace %+v", rs)
	}
}

// A Codex that reaches magpie for account failover alone, not connected to
// it, is handed only its own models, and an ETag that is neither the full
// list's nor contains it, so moving between the two has Codex ask again
// (#1385: 62 of magpie's models were in its list). Its replies' models ETag
// carries the same tag.
func TestCodexModelListOwnOnlyForFailover(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("ETag", `"v1"`)
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1}]}`)
			return
		}
		w.Header().Set("X-Models-Etag", `W/"v1"`)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1"}}`))
	})
	ownOnly := false
	was := CodexOwnOnly
	CodexOwnOnly = func() bool { return ownOnly }
	t.Cleanup(func() { CodexOwnOnly = was })
	list := func() ([]string, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		New().Handler().ServeHTTP(rec, req)
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		var slugs []string
		for _, m := range got.Models {
			slug, _ := m["slug"].(string)
			slugs = append(slugs, slug)
		}
		return slugs, rec.Header().Get("ETag")
	}
	full, fullTag := list()
	if len(full) < 2 || !strings.Contains(strings.Join(full, " "), "fake/m1") {
		t.Fatalf("connected list %v", full)
	}
	ownOnly = true
	own, ownTag := list()
	if len(own) != 1 || own[0] != "gpt-5.5" {
		t.Errorf("failover-only list = %v, want just gpt-5.5", own)
	}
	if ownTag == fullTag || ownTag != `"v1+magpie-`+provider.CodexOwnListTag()+`"` ||
		codexcat.Tagged(ownTag, provider.CodexListTag()) || codexcat.Tagged(fullTag, provider.CodexOwnListTag()) {
		t.Errorf("ETag own %q, full %q", ownTag, fullTag)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Models-Etag"); rec.Code != 200 || !codexcat.Tagged(got, provider.CodexOwnListTag()) {
		t.Errorf("%d X-Models-Etag %q", rec.Code, got)
	}
}

// A lead a provider answered before it was renamed was answered by the
// id it has now, the one its candidates go by (cybercaius).
func TestRenamedProviderAnsweredItsLead(t *testing.T) {
	setHome(t, t.TempDir())
	if err := provider.Save(provider.Provider{ID: "myrelay", Name: "My relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	session := "lead-renamed-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	usage.Append(usage.Record{
		Time: time.Now(), Agent: "codex", Provider: "myrelay", Host: "relay.example.com", ProviderKeyID: "fp",
		Model: "gpt-5.5", Input: 9, Output: 2, Status: 200, Endpoint: CodexPath + "/responses", Session: session,
	})
	if err := provider.Rename("myrelay", "newrelay"); err != nil {
		t.Fatal(err)
	}
	if a := loggedAnswers(session); len(a) != 1 || a[0].provider != "newrelay" || a[0].who != "newrelay#fp" {
		t.Fatalf("answers %+v", a)
	}
}

// A turn on Codex's own sign-in goes out through magpie's client as a
// routed try does, so its try says where its time went as one does
// (TryNet): the Routing page showed nothing for it (Discord, lute: some
// requests showed magpie's and the vendor's time and some didn't). The
// backend's wait before its headers is the vendor's.
func TestCodexOwnSignInTellsWhereItsTimeWent(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	const hold = 200 * time.Millisecond
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		time.Sleep(hold)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	st := s.Trace(t.Context(), 0, 0)
	if rec.Code != 200 || len(st.Routes) != 1 || len(st.Routes[0].Tries) != 1 {
		t.Fatalf("%d %+v", rec.Code, st.Routes)
	}
	n := st.Routes[0].Tries[0].Net
	if n == nil {
		t.Fatal("no timing for a turn on Codex's own sign-in")
	}
	holdMs := float64(hold.Milliseconds())
	if n.Asks != 1 || n.Wait < holdMs-1 || n.Head < n.Wait {
		t.Fatalf("timing %+v for a %v hold", *n, hold)
	}
}

// A turn on Codex's own sign-in that was masked keeps what masking took,
// on its route and in its usage record, as a routed one does: the
// Routing page showed no masking time for it.
func TestCodexOwnSignInKeepsItsMaskingTime(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	withoutMasker(t)
	m := heldMasker{let: make(chan struct{}), entered: make(chan struct{}), once: new(sync.Once)}
	t.Cleanup(gwmw.Mask(m))
	const hold = 120 * time.Millisecond
	go func() {
		<-m.entered
		time.Sleep(hold)
		close(m.let)
	}()
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	st := s.Trace(t.Context(), 0, 0)
	if rec.Code != 200 || len(st.Routes) != 1 {
		t.Fatalf("%d %+v", rec.Code, st.Routes)
	}
	if got := st.Routes[0].Masking; got < hold.Milliseconds() {
		t.Fatalf("route's masking %d ms, held %s", got, hold)
	}
	recs := usage.Load(time.Time{})
	if len(recs) == 0 || recs[len(recs)-1].Masking != st.Routes[0].Masking {
		t.Fatalf("usage records %+v, route's masking %d", recs, st.Routes[0].Masking)
	}
}
