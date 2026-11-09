package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// prevEnv is a sandbox with one provider, fresh responses kept, and an
// upstream that records every request and answers with answer.
type prevUp struct {
	mu   sync.Mutex
	got  [][]byte
	srv  *httptest.Server
	next func(n int, body []byte) (int, string, string) // status, content type, body
}

func (u *prevUp) last() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	var q map[string]any
	json.Unmarshal(u.got[len(u.got)-1], &q)
	return q
}

func (u *prevUp) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.got)
}

func newPrevUp(t *testing.T, next func(n int, body []byte) (int, string, string)) *prevUp {
	u := &prevUp{next: next}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.got = append(u.got, b)
		n := len(u.got)
		u.mu.Unlock()
		code, ct, reply := u.next(n, b)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(code)
		io.WriteString(w, reply)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func prevSandbox(t *testing.T, p provider.Provider) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := prevResponses
	prevResponses = newPrevStore(prevCap, prevTTL)
	t.Cleanup(func() { prevResponses = old })
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New()
}

// chatSSEReply is a Chat stream that says text.
func chatSSEReply(text string) string {
	c, _ := json.Marshal(text)
	return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":" + string(c) + "},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
}

func askResponses(t *testing.T, s *Server, keyID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.Background()
	if keyID != "" {
		ctx = access.WithIdentity(ctx, access.Identity{KeyID: keyID, KeyName: keyID})
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:5555"
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func responseID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var r struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &r) != nil || r.ID == "" {
		t.Fatalf("no id in %s", rec.Body.String())
	}
	return r.ID
}

// A client that sends only the new input after previous_response_id
// (kevinzhow on X) has the conversation given back whole, here to a
// provider on Chat: the model sees the earlier turn and its own answer.
func TestPreviousResponseGivenWholeToChat(t *testing.T) {
	up := newPrevUp(t, func(n int, _ []byte) (int, string, string) {
		return 200, "text/event-stream", chatSSEReply(fmt.Sprintf("answer %d", n))
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","instructions":"be brief","input":"my name is Ada"}`))
	id2 := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":[{"role":"user","content":"what is my name?"}]}`))
	sent, _ := json.Marshal(up.last()["messages"])
	for _, want := range []string{"my name is Ada", "answer 1", "what is my name?"} {
		if !strings.Contains(string(sent), want) {
			t.Fatalf("turn 2 went without %q: %s", want, sent)
		}
	}
	// instructions go with the request that gives them, as OpenAI does
	if strings.Contains(string(sent), "be brief") {
		t.Fatalf("turn 1's instructions carried: %s", sent)
	}
	// and a third turn after the second has all of it
	responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id2+`","input":"and again?"}`))
	sent, _ = json.Marshal(up.last()["messages"])
	for _, want := range []string{"my name is Ada", "answer 1", "what is my name?", "answer 2", "and again?"} {
		if !strings.Contains(string(sent), want) {
			t.Fatalf("turn 3 went without %q: %s", want, sent)
		}
	}
}

// An id magpie doesn't hold, to a provider it would be translated for,
// is OpenAI's own 400; so is one another gateway key's response, and one
// the client asked not be kept (store false).
func TestPreviousResponseNotFound(t *testing.T) {
	up := newPrevUp(t, func(int, []byte) (int, string, string) {
		return 200, "text/event-stream", chatSSEReply("ok")
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	notFound := func(rec *httptest.ResponseRecorder, id string) {
		t.Helper()
		var e struct {
			Error struct{ Message, Type, Param, Code string }
		}
		json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != 400 || e.Error.Code != "previous_response_not_found" || e.Error.Param != "previous_response_id" || e.Error.Type != "invalid_request_error" || !strings.Contains(e.Error.Message, id) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	notFound(askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"resp_nowhere","input":"hi"}`), "resp_nowhere")
	if up.calls() != 0 {
		t.Fatalf("sent upstream without its history: %d", up.calls())
	}
	a := responseID(t, askResponses(t, s, "alice", `{"model":"fake/m1","input":"secret plan"}`))
	notFound(askResponses(t, s, "bob", `{"model":"fake/m1","previous_response_id":"`+a+`","input":"what was it?"}`), a)
	notFound(askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+a+`","input":"what was it?"}`), a)
	responseID(t, askResponses(t, s, "alice", `{"model":"fake/m1","previous_response_id":"`+a+`","input":"what was it?"}`))
	off := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","store":false,"input":"forget me"}`))
	notFound(askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+off+`","input":"again"}`), off)
}

// An id magpie doesn't hold, to a provider on Responses the request is
// relayed to, goes as it came: a response the provider made before
// magpie restarted is still its own to find.
func TestUnknownPreviousResponseRelayedAsSent(t *testing.T) {
	up := newPrevUp(t, func(int, []byte) (int, string, string) {
		return 200, "application/json", `{"id":"resp_up","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"resp_before","input":"hi"}`))
	if got := up.last()["previous_response_id"]; got != "resp_before" {
		t.Fatalf("sent %v", got)
	}
}

// A provider that keeps responses (OpenAI's, Ark's) and answered the one
// named is sent the new input alone after its own id: the images it holds
// aren't sent again. One that no longer holds it is sent the whole.
func TestPreviousResponseKeptUpstreamSentNewInputOnly(t *testing.T) {
	defer func(old func(provider.Provider) bool) { prevKeptBy = old }(prevKeptBy)
	prevKeptBy = func(p provider.Provider) bool { return p.ID == "fake" }
	var forget atomic.Bool
	up := newPrevUp(t, func(n int, body []byte) (int, string, string) {
		if forget.Load() && strings.Contains(string(body), "previous_response_id") {
			return 400, "application/json", `{"error":{"message":"Previous response with id 'resp_up1' not found.","type":"invalid_request_error","param":"previous_response_id","code":"previous_response_not_found"}}`
		}
		return 200, "application/json", fmt.Sprintf(`{"id":"resp_up%d","object":"response","status":"completed","output":[{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"answer %d"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`, n, n, n)
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	img := `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("QUFB", 50000) + `"},{"type":"input_text","text":"what is this?"}]}`
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":[`+img+`]}`))
	if id != "resp_up1" {
		t.Fatalf("client given %s", id)
	}
	rec := askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":"and now?"}`)
	if responseID(t, rec) != "resp_up2" {
		t.Fatal(rec.Body.String())
	}
	q := up.last()
	in, _ := json.Marshal(q["input"])
	up.mu.Lock()
	sent := len(up.got[1])
	up.mu.Unlock()
	if q["previous_response_id"] != "resp_up1" || strings.Contains(string(in), "QUFB") || !strings.Contains(string(in), "and now?") || sent > 2000 {
		t.Fatalf("sent %d bytes: prev %v input %s", sent, q["previous_response_id"], in)
	}
	// the upstream lost it: asked again with the conversation whole
	forget.Store(true)
	responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"resp_up2","input":"once more"}`))
	q = up.last()
	in, _ = json.Marshal(q["input"])
	if _, has := q["previous_response_id"]; has || !strings.Contains(string(in), "QUFB") || !strings.Contains(string(in), "answer 1") || !strings.Contains(string(in), "and now?") || !strings.Contains(string(in), "once more") {
		t.Fatalf("whole: prev %v input %.300s", q["previous_response_id"], in)
	}
}

// What a response named brings back is masked as the client's own words
// are: a key in an earlier turn never reaches the vendor.
func TestPreviousResponseMasked(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	up := newPrevUp(t, func(int, []byte) (int, string, string) {
		return 200, "text/event-stream", chatSSEReply("noted")
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":"my key is `+key+`"}`))
	responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":"what was it?"}`))
	sent, _ := json.Marshal(up.last())
	if strings.Contains(string(sent), key) || !strings.Contains(string(sent), "my key is {{") {
		t.Fatalf("sent %s", sent)
	}
}

// The kept responses are bounded by bytes and time, the oldest out first.
func TestPrevStoreBounded(t *testing.T) {
	st := newPrevStore(100, prevTTL)
	item := json.RawMessage(strings.Repeat("x", 40))
	for i := range 4 {
		st.put(&prevTurn{key: prevKey{"s", fmt.Sprint(i)}, items: []json.RawMessage{item}, own: 40})
	}
	if _, ok := st.get(prevKey{"s", "0"}); ok {
		t.Fatal("oldest kept past the cap")
	}
	if _, ok := st.get(prevKey{"s", "3"}); !ok || st.size > 100 {
		t.Fatalf("newest gone, size %d", st.size)
	}
	st.put(&prevTurn{key: prevKey{"s", "big"}, own: 60})
	if _, ok := st.get(prevKey{"s", "big"}); ok {
		t.Fatal("a turn past half the cap kept")
	}
	now := st.now()
	st.now = func() time.Time { return now.Add(prevTTL + 1) }
	if _, ok := st.get(prevKey{"s", "3"}); ok {
		t.Fatal("kept past its time")
	}
}

// TestKeptTurnOwnsItsInput: a kept response holds its input, not the body
// it was read from with the request's tools and instructions, and counts
// what it holds.
func TestKeptTurnOwnsItsInput(t *testing.T) {
	old := prevResponses
	prevResponses = newPrevStore(prevCap, prevTTL)
	t.Cleanup(func() { prevResponses = old })
	body := []byte(`{"model":"m","instructions":"` + strings.Repeat("rules ", 1000) + `","input":[{"type":"message","role":"user","content":"hi"}]}`)
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	w, r, _, done := previousResponse(httptest.NewRecorder(), r, provider.Responses, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":"ok"}]}`)
	done()
	for i := range body {
		body[i] = 'x'
	}
	kept, ok := prevResponses.get(prevKey{prevScope(r), "resp_1"})
	if !ok {
		t.Fatal("nothing kept")
	}
	want := `[{"type":"message","role":"user","content":"hi"},{"type":"message","role":"assistant","content":"ok"}]`
	if got, _ := json.Marshal(kept.items); string(got) != want {
		t.Fatalf("kept after the body was written over: %s", got)
	}
	if n := len(want) - len("[,]"); kept.own != n {
		t.Fatalf("own %d, want the %d bytes kept", kept.own, n)
	}
}

// foreignUp is an upstream that, once foreign is set, turns away a
// previous_response_id or an input item id another of its resources made,
// as Azure OpenAI behind a relay spreading one key over several resources
// does (hykql on Discord), and answers each turn with reasoning and a
// message that carry ids.
func foreignUp(t *testing.T, foreign *atomic.Bool) *prevUp {
	return newPrevUp(t, func(n int, body []byte) (int, string, string) {
		s := string(body)
		if foreign.Load() && (strings.Contains(s, "previous_response_id") || strings.Contains(s, `"id":"msg_`) || strings.Contains(s, `"id":"rs_`)) {
			return 400, "application/json", `{"error":{"message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it.","type":"invalid_request_error"}}`
		}
		return 200, "application/json", fmt.Sprintf(`{"id":"resp_up%d","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs_%d","summary":[]},{"type":"reasoning","id":"rs_e%d","summary":[],"encrypted_content":"sealed%d"},{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"answer %d"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`, n, n, n, n, n, n)
	})
}

// wholeWithoutIDs fails unless q, a request's body, is the conversation
// whole with none of the earlier reply's ids, its sealed reasoning kept.
func wholeWithoutIDs(t *testing.T, q map[string]any) {
	t.Helper()
	in, _ := json.Marshal(q["input"])
	s := string(in)
	if _, has := q["previous_response_id"]; has {
		t.Fatalf("previous_response_id still sent: %.400s", s)
	}
	if strings.Contains(s, `"id":"msg_`) || strings.Contains(s, `"id":"rs_`) {
		t.Fatalf("the earlier reply's ids still sent: %.400s", s)
	}
	for _, want := range []string{"first turn", "answer 1", "sealed1", "second turn"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q missing from the whole: %.400s", want, s)
		}
	}
}

// An upstream that keeps responses, behind which another resource
// answers: the delta naming the earlier response is turned away as
// another resource's, and the conversation goes whole, its items naming
// nothing, rather than the 400 reaching the client.
func TestPreviousResponseOtherResourceDeltaSentWhole(t *testing.T) {
	defer func(old func(provider.Provider) bool) { prevKeptBy = old }(prevKeptBy)
	prevKeptBy = func(p provider.Provider) bool { return p.ID == "fake" }
	var foreign atomic.Bool
	up := foreignUp(t, &foreign)
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":"first turn"}`))
	foreign.Store(true)
	rec := askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":"second turn"}`)
	if rec.Code != 200 {
		t.Fatalf("client given %d: %s", rec.Code, rec.Body.String())
	}
	if n := up.calls(); n != 3 {
		t.Fatalf("%d upstream calls, want the delta and the whole once", n)
	}
	wholeWithoutIDs(t, up.last())
}

// The same behind a provider that doesn't keep responses, sent the whole
// each turn: the items magpie brought back are what is turned away, and
// they go once more without their ids. A refusal with nothing left to
// take out is passed on, not asked again as it was.
func TestPreviousResponseOtherResourceWholeSentWithoutIDs(t *testing.T) {
	defer func(old func(provider.Provider) bool) { prevKeptBy = old }(prevKeptBy)
	prevKeptBy = func(provider.Provider) bool { return false }
	var foreign atomic.Bool
	up := foreignUp(t, &foreign)
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":"first turn"}`))
	foreign.Store(true)
	rec := askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":"second turn"}`)
	if rec.Code != 200 {
		t.Fatalf("client given %d: %s", rec.Code, rec.Body.String())
	}
	if n := up.calls(); n != 3 {
		t.Fatalf("%d upstream calls, want the whole and the whole without ids", n)
	}
	wholeWithoutIDs(t, up.last())

	// the client's own item with an id: nothing of magpie's to take out
	rec = askResponses(t, s, "", `{"model":"fake/m1","input":[{"type":"message","id":"msg_mine","role":"user","content":"hi"}]}`)
	if rec.Code != 400 || up.calls() != 4 {
		t.Fatalf("client given %d after %d calls, want the 400 after one", rec.Code, up.calls()-3)
	}
}

// A conversation going on with another provider than the one whose reply
// it holds goes without that reply's ids at once: the other never made
// them. The same provider and key is sent them as before.
func TestPreviousResponseToAnotherProviderSentWithoutIDs(t *testing.T) {
	var foreign atomic.Bool
	up := foreignUp(t, &foreign)
	other := foreignUp(t, &foreign)
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.srv.URL + "/v1"})
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k2", Models: []string{"m1"}, Responses: other.srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	id := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":"first turn"}`))
	id = responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+id+`","input":"again"}`))
	if in, _ := json.Marshal(up.last()["input"]); !strings.Contains(string(in), `"id":"msg_1"`) {
		t.Fatalf("the provider that made them not sent its own ids: %.400s", in)
	}
	responseID(t, askResponses(t, s, "", `{"model":"other/m1","previous_response_id":"`+id+`","input":"second turn"}`))
	if n := other.calls(); n != 1 {
		t.Fatalf("%d calls to the other provider, want one", n)
	}
	wholeWithoutIDs(t, other.last())
}
