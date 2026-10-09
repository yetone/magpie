package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// chatgpt stands in for the ChatGPT backend behind CodexBase.
func chatgpt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	setHome(t, t.TempDir()) // no sign-ins but the test's
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

// Responses over a WebSocket are turned away so Codex uses HTTP at once.
func TestCodexWebSocketUpgradeRequired(t *testing.T) {
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Errorf("%d", rec.Code)
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
