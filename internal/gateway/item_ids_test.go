package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// museReasoningID is the reasoning item id Muse Spark on OpenCode handed
// Codex, as echo_ts's error quotes it: two ids joined with a colon.
const museReasoningID = "rs_6aca2fc7533c979d5f0e4f7a:rs_01a125caa544754e9a3c70080100b009"

// museConversation is echo_ts's conversation after the switch: a user
// turn, Muse's reasoning at input[1] with its id and its sealed content,
// a call Muse made (its item id joined the same way) and its output, and
// Muse's answer.
const museConversation = `{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
  {"type":"reasoning","id":"` + museReasoningID + `","summary":[],"encrypted_content":"muse-sealed"},
  {"type":"function_call","id":"fc_6aca2fc7533c979d5f0e4f7b:fc_01a125caa544754e9a3c70080100b00a","call_id":"call_muse_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
  {"type":"function_call_output","call_id":"call_muse_1","output":"main.go"},
  {"type":"message","id":"msg_6aca2fc7533c979d5f0e4f7c:msg_01a125caa544754e9a3c70080100b00b","role":"assistant","content":[{"type":"output_text","text":"I fixed main.go"}]}`

// openaiItemID is the id OpenAI takes on an input item.
var openaiItemID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// idChecker plays OpenAI's /responses as Copilot passes it on: an input
// item whose id has another character is refused with OpenAI's 400, word
// for word as echo_ts got it (Copilot's name is magpie's, before it).
// With check off it takes any id, as OpenCode does its own.
type idChecker struct {
	mu     sync.Mutex
	check  bool
	inputs [][]map[string]any
}

func (u *idChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(body, &q)
	u.mu.Lock()
	u.inputs = append(u.inputs, q.Input)
	u.mu.Unlock()
	for i, it := range q.Input {
		id, ok := it["id"].(string)
		if !u.check || !ok {
			continue
		}
		if !openaiItemID.MatchString(id) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Invalid 'input[`+mustString(int64(i))+`].id': '`+id+`'. Expected an ID that contains letters, numbers, underscores, or dashes, but this value contained additional characters.","type":"invalid_request_error","param":"input[`+mustString(int64(i))+`].id","code":"invalid_value"}}`)
			return
		}
		// and one longer than the 64 an id may have, word for word as
		// isnine got it (#4525), ghjkllasdf-fake's 444 alike (#4527)
		if len(id) > 64 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Invalid 'input[`+mustString(int64(i))+`].id': string too long. Expected a string with maximum length 64, but got a string with length `+mustString(int64(len(id)))+` instead.","type":"invalid_request_error","param":"input[`+mustString(int64(i))+`].id","code":"string_too_long"}}`)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"DONE"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}}`))
}

func (u *idChecker) asked() [][]map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]map[string]any(nil), u.inputs...)
}

// checkSent fails unless input is museConversation as an id checker takes
// it: Muse's reasoning gone, every other item there with an id of
// [A-Za-z0-9_-], and the call still paired with its output.
func checkSent(t *testing.T, input []map[string]any) {
	t.Helper()
	var types []string
	for _, it := range input {
		types = append(types, it["type"].(string))
		if id, ok := it["id"].(string); ok && !openaiItemID.MatchString(id) {
			t.Errorf("item id %q sent", id)
		}
		if it["type"] == "reasoning" {
			t.Errorf("Muse's reasoning sent: %v", it)
		}
	}
	if got := strings.Join(types, ","); !strings.HasPrefix(got, "message,function_call,function_call_output,message") {
		t.Errorf("items sent: %s", got)
	}
	b, _ := json.Marshal(input)
	for _, want := range []string{`"fc_6aca2fc7533c979d5f0e4f7b_fc_01a125caa544754e9a3c70080100b00a"`, `"call_muse_1"`, "I fixed main.go", "fix the login bug"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("input lacks %s: %s", want, b)
		}
	}
}

// echo_ts, in Codex: a conversation on Muse Spark (OpenCode), switched to
// GPT on GitHub Copilot. Muse's items, ids joined with a colon, went to
// Copilot as Codex sent them, and every turn was turned away "Invalid
// 'input[1].id'". Copilot checks ids as OpenAI does, so they go as it
// takes them from the first ask.
func TestCopilotTakesAnotherVendorsItemIDs(t *testing.T) {
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "echo", "oauth_token": "gho_echo"},
	}), 0o600)
	up := &idChecker{check: true}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-6.1","name":"GPT-6.1","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}}}]}`)
		case "/responses":
			up.ServeHTTP(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	defer gh.Close()
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	defer func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser }()

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"copilot/gpt-6.1","stream":true,"store":false,"include":["reasoning.encrypted_content"],
	  "input":[`+museConversation+`,{"type":"message","role":"user","content":[{"type":"input_text","text":"now add a test"}]}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	asked := up.asked()
	if len(asked) != 1 {
		t.Fatalf("asked Copilot %d times", len(asked))
	}
	checkSent(t, asked[0])
}

// A relay in front of OpenAI, which magpie can't tell from any other
// upstream, refuses the ids as OpenAI does: asked once more with ids it
// takes, and from then on asked that way at once.
func TestRelayRefusingItemIDsAskedAgain(t *testing.T) {
	fresh(t)
	up := &idChecker{check: true}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for turn := range 2 {
		rec := codexPostTo(s, `{"model":"relay/gpt-6.1","stream":true,"store":false,"input":[`+museConversation+`]}`)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("turn %d: %d %s", turn, rec.Code, rec.Body.String())
		}
	}
	asked := up.asked()
	if len(asked) != 3 {
		t.Fatalf("asked the relay %d times, want 2 for the first turn and 1 for the second", len(asked))
	}
	checkSent(t, asked[1])
	checkSent(t, asked[2])
}

// OpenCode, where Muse made them, gets Muse's items as Codex sent them:
// its reasoning, id and seal, is its own to read back.
func TestMuseKeepsItsOwnItemIDs(t *testing.T) {
	fresh(t)
	up := &idChecker{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "zen", Name: "OpenCode", Key: "k", Models: []string{"muse-spark-1.3-contributor"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	rec := codexPostTo(New(), `{"model":"zen/muse-spark-1.3-contributor","stream":true,"store":false,"input":[`+museConversation+`]}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	asked := up.asked()
	if len(asked) != 1 {
		t.Fatalf("asked %d times", len(asked))
	}
	b, _ := json.Marshal(asked[0])
	if !strings.Contains(string(b), `"`+museReasoningID+`"`) || !strings.Contains(string(b), "muse-sealed") {
		t.Errorf("Muse's reasoning not sent as it was: %s", b)
	}
}

// Back on one of Codex's own models, on the ChatGPT backend, which checks
// ids as OpenAI does.
func TestCodexOwnModelTakesAnotherVendorsItemIDs(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	up := &idChecker{check: true}
	chatgpt(t, up.ServeHTTP)
	code, body := codexPost(t, `{"model":"gpt-6-luna","stream":true,"store":false,"input":[`+museConversation+`]}`)
	if code != 200 || !strings.Contains(body, "DONE") {
		t.Fatalf("%d %s", code, body)
	}
	asked := up.asked()
	if len(asked) != 1 {
		t.Fatalf("asked the backend %d times", len(asked))
	}
	checkSent(t, asked[0])
}

// copilotLongMessageID and copilotLongReasoningID are ids of the lengths
// Copilot hands out and Codex carries back: 444 as ghjkllasdf-fake
// recorded from an approval session (#4527), 420 as isnine's three
// sessions each showed (#4525) — every one of the characters OpenAI
// takes an id of, so only their length makes an upstream that checks
// ids turn the request away.
var (
	copilotLongMessageID   = "msg_" + strings.Repeat("A", 440)
	copilotLongReasoningID = "rs_" + strings.Repeat("b", 417)
)

// copilotOverlongConversation is a conversation Codex sends Copilot on a
// later turn: the user's ask, Copilot's own reasoning (sealed, its to
// read), Copilot's answer under its overlong id, and the next ask.
var copilotOverlongConversation = `{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply exactly OK."}]},
  {"type":"reasoning","id":"` + copilotLongReasoningID + `","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"gAAAA"},
  {"type":"message","id":"` + copilotLongMessageID + `","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"Reply exactly OK again."}]}`

// checkBoundSent fails unless input is copilotOverlongConversation as an
// id checker takes it: every item there, each id of at most 64 of the
// characters OpenAI takes, and Copilot's reasoning kept — it made that
// one, its seal is its own to read, unlike another vendor's.
func checkBoundSent(t *testing.T, input []map[string]any) {
	t.Helper()
	var types []string
	for _, it := range input {
		types = append(types, it["type"].(string))
		id, _ := it["id"].(string)
		if id == "" {
			continue
		}
		if len(id) > 64 {
			t.Errorf("item id of %d characters sent: %v", len(id), it)
		}
		if !openaiItemID.MatchString(id) {
			t.Errorf("item id %q sent", id)
		}
	}
	if got := strings.Join(types, ","); got != "message,reasoning,message,message" {
		t.Errorf("items sent: %s", got)
	}
	var reasoning bool
	for _, it := range input {
		if it["type"] == "reasoning" {
			reasoning = true
			if enc, _ := it["encrypted_content"].(string); enc != "gAAAA" {
				t.Errorf("Copilot's reasoning sent without its seal: %v", it)
			}
		}
	}
	if !reasoning {
		t.Error("Copilot's reasoning not sent: it made that one, its seal is its own to read")
	}
}

// isnine (#4525) and ghjkllasdf-fake (#4527), in Codex: a conversation on
// Copilot, whose items come with ids hundreds of characters long, goes
// back with them on every later turn, and Copilot turns the whole request
// away "Invalid 'input[N].id': string too long … maximum length 64". The
// ids go shortened from the first ask, the same way each turn, and
// Copilot's own reasoning keeps its place with its seal.
func TestCopilotTakesAnotherVendorsOverlongItemIDs(t *testing.T) {
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "echo", "oauth_token": "gho_echo"},
	}), 0o600)
	up := &idChecker{check: true}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-6.1","name":"GPT-6.1","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}}}]}`)
		case "/responses":
			up.ServeHTTP(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	defer gh.Close()
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	defer func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser }()

	body := `{"model":"copilot/gpt-6.1","stream":true,"store":false,"include":["reasoning.encrypted_content"],
	  "input":[` + copilotOverlongConversation + `]}`
	s := New()
	for turn := range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("turn %d: %d %s", turn, rec.Code, rec.Body.String())
		}
	}
	asked := up.asked()
	if len(asked) != 2 {
		t.Fatalf("asked Copilot %d times", len(asked))
	}
	for _, input := range asked {
		checkBoundSent(t, input)
	}
	// the same long id goes to the same shorter one every turn: an item
	// named by its id on one turn is still found on the next
	first, next := map[string]bool{}, map[string]bool{}
	for _, it := range asked[0] {
		if id, _ := it["id"].(string); id != "" {
			first[id] = true
		}
	}
	for _, it := range asked[1] {
		if id, _ := it["id"].(string); id != "" {
			next[id] = true
		}
	}
	if !maps.Equal(first, next) {
		t.Errorf("ids of turn 2 differ from turn 1's: %v then %v", first, next)
	}
}

// An id of 64 of the characters OpenAI takes goes as it came; one of 65
// goes shorter, as its kind's prefix and the sha256 of the whole of it,
// the same every time and never two ids to one — a plain cut could share
// its first 64, as ids of a vendor can begin alike. Copilot's own
// reasoning, overlong and sealed, keeps its place; another vendor's
// (Muse's, its id joined with a colon) still goes. One cut stays cut.
func TestPlainItemIDsBoundItemIDs(t *testing.T) {
	at64 := "msg_" + strings.Repeat("a", 60)
	body := []byte(`{"model":"copilot/gpt-6.1","store":false,"input":[
	  {"type":"message","role":"user","id":"` + at64 + `","content":[{"type":"input_text","text":"hi"}]}]}`)
	if got := plainItemIDs(body); !bytes.Equal(got, body) {
		t.Fatalf("an id of 64 characters was changed: %s", got)
	}

	long := "msg_" + strings.Repeat("A", 440)
	like := "msg_" + strings.Repeat("A", 435) + "1111"
	other := "msg_" + strings.Repeat("A", 435) + "2222"
	rs := "rs_" + strings.Repeat("b", 417)
	body = []byte(`{"model":"copilot/gpt-6.1","store":false,"input":[
	  {"type":"message","role":"user","id":"msg_keep","content":[{"type":"input_text","text":"hi"}]},
	  {"type":"message","role":"assistant","id":"` + long + `","status":"completed","content":[{"type":"output_text","text":"OK"}]},
	  {"type":"reasoning","id":"` + rs + `","summary":[],"encrypted_content":"gAAAA"},
	  {"type":"reasoning","id":"` + museReasoningID + `","summary":[],"encrypted_content":"muse-sealed"},
	  {"type":"web_search_call","id":"` + like + `","status":"completed","action":{"type":"search","query":"a"}},
	  {"type":"web_search_call","id":"` + other + `","status":"completed","action":{"type":"search","query":"b"}}]}`)
	got := plainItemIDs(body)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(got, &q) != nil {
		t.Fatalf("not a request any more: %s", got)
	}
	var types []string
	var bound string
	for i, it := range q.Input {
		typ, _ := it["type"].(string)
		types = append(types, typ)
		id, _ := it["id"].(string)
		if id == "" {
			continue
		}
		if len(id) > 64 || !openaiItemID.MatchString(id) {
			t.Errorf("id %q sent as an upstream that checks ids takes them", id)
		}
		if i == 1 {
			bound = id
		}
	}
	if got := strings.Join(types, ","); got != "message,message,reasoning,web_search_call,web_search_call" {
		t.Fatalf("items: %s", got)
	}
	if q.Input[0]["id"] != "msg_keep" {
		t.Errorf("an id that fits was changed: %q", q.Input[0]["id"])
	}
	// a message may go without its id, and goes so when it was too long
	if _, ok := q.Input[1]["id"]; ok {
		t.Errorf("the message's overlong id sent: %q", bound)
	}
	boundRS, _ := q.Input[2]["id"].(string)
	if boundRS == "" || !strings.HasPrefix(boundRS, "rs_") {
		t.Errorf("Copilot's own reasoning not sent under its kind's prefix: %q", boundRS)
	}
	if boundRS == copilotLongReasoningID {
		t.Errorf("the reasoning's id sent as it came: %q", boundRS)
	}
	// Copilot's seal kept (the item is its own to read back)
	if !bytes.Contains(got, []byte("gAAAA")) {
		t.Error("Copilot's reasoning sent without its seal")
	}
	// two ids that begin alike go to two (a plain cut of 64 would share
	// its first 440, so these two would land on one)
	var callIDs []string
	for _, it := range q.Input {
		if it["type"] == "web_search_call" {
			id, _ := it["id"].(string)
			if !strings.HasPrefix(id, "ws_") {
				t.Errorf("a web search call's id not bound under its kind's prefix: %q", id)
			}
			callIDs = append(callIDs, id)
		}
	}
	if len(callIDs) != 2 || callIDs[0] == callIDs[1] {
		t.Errorf("two ids that begin alike went to one: %v", callIDs)
	}
	got = plainItemIDs(got)
	var q2 struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(got, &q2) != nil {
		t.Fatalf("not a request any more: %s", got)
	}
	for i, it := range q2.Input {
		id, _ := it["id"].(string)
		want, _ := q.Input[i]["id"].(string)
		if id != want {
			t.Errorf("a cut id was cut again: %q then %q", want, id)
		}
	}
}

// copilotOpaqueCallID is a function call's id as Copilot hands it out on
// the direct path: 452 characters of standard Base64, + and / in it and no
// fc_ at the front (echo_ts, after Codex's native compaction).
var copilotOpaqueCallID = func() string {
	b := make([]byte, 339)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	return base64.StdEncoding.EncodeToString(b)
}()

// opaqueCopilot plays Copilot's /responses as echo_ts and isnine found
// it: an id it handed out goes back as it came; an item without its id
// goes; any other id must be of at most 64 characters and begin with its
// kind's prefix, or the whole request is turned away with Copilot's
// empty 400, word for word.
type opaqueCopilot struct {
	mu     sync.Mutex
	own    map[string]bool
	bodies []map[string]any
}

func (u *opaqueCopilot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(body, &q)
	u.mu.Lock()
	u.bodies = append(u.bodies, q)
	u.mu.Unlock()
	input, _ := q["input"].([]any)
	for _, raw := range input {
		it, _ := raw.(map[string]any)
		id, ok := it["id"].(string)
		if !ok || u.own[id] {
			continue
		}
		typ, _ := it["type"].(string)
		prefix := map[string]string{"message": "msg_", "function_call": "fc_", "reasoning": "rs_", "web_search_call": "ws_"}[typ]
		if len(id) > 64 || !openaiItemID.MatchString(id) || prefix != "" && !strings.HasPrefix(id, prefix) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"","code":"invalid_request_body"}}`)
			return
		}
	}
	done := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"DONE"}]}`
	if q["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":`+done+`}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[`+done+`],"usage":{"input_tokens":5,"output_tokens":1}}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[`+done+`],"usage":{"input_tokens":5,"output_tokens":1}}`)
}

func (u *opaqueCopilot) asked() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.bodies...)
}

// onOpaqueCopilot signs a fresh HOME in to a Copilot whose /responses is
// up, with one GPT model on /responses.
func onOpaqueCopilot(t *testing.T, up *opaqueCopilot) {
	t.Helper()
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "echo", "oauth_token": "gho_echo"},
	}), 0o600)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-6.1","name":"GPT-6.1","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}}}]}`)
		case "/responses":
			up.ServeHTTP(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.Close)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	t.Cleanup(gh.Close)
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	t.Cleanup(func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser })
}

// echo_ts, in Codex on GitHub Copilot: after Codex's native compaction,
// the turn that carried Copilot's tool call back was turned away with an
// empty 400 "invalid_request_body". The call's 452-character id had gone
// as a bare sha256 digest, which Copilot refuses, where its own id, or
// none, goes. The call goes without its id, paired with its output by
// call_id, streamed or not.
func TestCopilotTakesItsOwnOpaqueCallID(t *testing.T) {
	if len(copilotOpaqueCallID) != 452 || !strings.ContainsAny(copilotOpaqueCallID, "+/") {
		t.Fatalf("fixture not of Copilot's shape: %d %q", len(copilotOpaqueCallID), copilotOpaqueCallID[:20])
	}
	up := &opaqueCopilot{own: map[string]bool{copilotOpaqueCallID: true}}
	onOpaqueCopilot(t, up)

	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
	  {"type":"function_call","id":"` + copilotOpaqueCallID + `","call_id":"call_Xq7","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
	  {"type":"function_call_output","call_id":"call_Xq7","output":"main.go"}]`
	s := New()
	for _, stream := range []bool{true, false} {
		body := `{"model":"copilot/gpt-6.1","stream":` + strconv.FormatBool(stream) + `,"store":false,"input":` + input + `}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("stream %v: %d %s", stream, rec.Code, rec.Body.String())
		}
	}
	asked := up.asked()
	if len(asked) != 2 {
		t.Fatalf("asked Copilot %d times", len(asked))
	}
	for _, q := range asked {
		items, _ := q["input"].([]any)
		var call, out map[string]any
		for _, raw := range items {
			it, _ := raw.(map[string]any)
			switch it["type"] {
			case "function_call":
				call = it
			case "function_call_output":
				out = it
			}
		}
		if call == nil || out == nil {
			t.Fatalf("call or its output not sent: %v", items)
		}
		if id, ok := call["id"]; ok && id != copilotOpaqueCallID {
			t.Errorf("Copilot's call sent under an id it never gave: %v", id)
		}
		if call["call_id"] != "call_Xq7" || out["call_id"] != "call_Xq7" {
			t.Errorf("the call and its output not paired as they were: %v %v", call["call_id"], out["call_id"])
		}
	}
}

// An overlong id of a kind that can't go without one (OpenAI's schema has
// it required), another vendor's or Copilot's opaque one, is bound under the
// prefix OpenAI gives that kind, else under its own; one of a kind that may
// go without its id goes without it, its call_id kept; and another vendor's
// reasoning of other characters still goes.
func TestPlainItemIDsBindsOtherKinds(t *testing.T) {
	long := strings.Repeat("Z", 300)
	body := []byte(`{"model":"gpt-6.1","store":false,"input":[
	  {"type":"reasoning","id":"` + long + `","summary":[],"encrypted_content":"gAAAA"},
	  {"type":"reasoning","id":"` + museReasoningID + `","summary":[],"encrypted_content":"muse-sealed"},
	  {"type":"web_search_call","id":"` + long + `","status":"completed","action":{"type":"search","query":"q"}},
	  {"type":"file_search_call","id":"` + long + `","status":"completed","queries":["q"]},
	  {"type":"computer_call","id":"` + long + `","call_id":"cu_call","action":{"type":"screenshot"},"pending_safety_checks":[],"status":"completed"},
	  {"type":"image_generation_call","id":"` + long + `","status":"completed","result":"iVBOR"},
	  {"type":"code_interpreter_call","id":"` + long + `","status":"completed","code":"1","container_id":"cntr_1","outputs":[]},
	  {"type":"mcp_list_tools","id":"` + long + `","server_label":"s","tools":[]},
	  {"type":"mcp_approval_request","id":"` + long + `","server_label":"s","name":"n","arguments":"{}"},
	  {"type":"mcp_call","id":"` + long + `","server_label":"s","name":"n","arguments":"{}"},
	  {"type":"item_kind_x","id":"xk_` + long + `"},
	  {"type":"item_kind_y","id":"` + long + `"},
	  {"type":"tool_search_call","id":"` + long + `","call_id":"ts_1","execution":"client","arguments":{}},
	  {"type":"tool_search_output","id":"` + long + `","call_id":"ts_1","execution":"client","tools":[]},
	  {"type":"custom_tool_call","id":"fc_` + long + `","call_id":"cc_1","name":"apply_patch","input":"x"},
	  {"type":"custom_tool_call_output","id":"` + long + `","call_id":"cc_1","output":"ok"},
	  {"type":"function_call_output","id":"` + long + `","call_id":"fn_1","output":"ok"},
	  {"type":"computer_call_output","id":"` + long + `","call_id":"cu_call","output":{"type":"computer_screenshot","image_url":"data:,"}},
	  {"type":"shell_call","id":"` + long + `","call_id":"sh_1","action":{"commands":["ls"]}},
	  {"type":"shell_call_output","id":"` + long + `","call_id":"sh_1","output":[]},
	  {"type":"apply_patch_call","id":"` + long + `","call_id":"ap_1","status":"completed","operation":{"type":"delete_file","path":"x"}},
	  {"type":"apply_patch_call_output","id":"` + long + `","call_id":"ap_1","status":"completed"},
	  {"type":"mcp_approval_response","id":"` + long + `","approval_request_id":"mcpr_1","approve":true},
	  {"type":"compaction","id":"` + long + `","encrypted_content":"gAAAB"}]}`)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(plainItemIDs(body), &q); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range q.Input {
		typ := it["type"].(string)
		id, has := it["id"].(string)
		if !has {
			ids = append(ids, typ+" without")
			if it["call_id"] == nil && strings.Contains(typ, "_call") {
				t.Errorf("%s sent without its call_id: %v", typ, it)
			}
			continue
		}
		if len(id) != 64 || !openaiItemID.MatchString(id) {
			t.Errorf("id %q not bound to 64 of the characters OpenAI takes", id)
		}
		prefix := ""
		if i := strings.IndexByte(id, '_'); i >= 0 {
			prefix = id[:i+1]
		}
		ids = append(ids, typ+"="+prefix)
	}
	got := strings.Join(ids, ",")
	want := "reasoning=rs_,web_search_call=ws_,file_search_call=fs_,computer_call=cu_," +
		"image_generation_call=ig_,code_interpreter_call=ci_,mcp_list_tools=mcpl_," +
		"mcp_approval_request=mcpr_,mcp_call=mcp_,item_kind_x=xk_,item_kind_y=," +
		"tool_search_call without,tool_search_output without,custom_tool_call without," +
		"custom_tool_call_output without,function_call_output without,computer_call_output without," +
		"shell_call without,shell_call_output without,apply_patch_call without," +
		"apply_patch_call_output without,mcp_approval_response without,compaction without"
	if got != want {
		t.Errorf("ids\n got %s\nwant %s", got, want)
	}
}

// echo_ts, in Codex on GitHub Copilot with native web search: Copilot
// hands a web_search_call an id of ~450 characters of Base64, no ws_ at
// the front, and Codex carries it back on the next turn. It went as a bare
// sha256 digest and Copilot turned the whole request away with its empty
// 400 "invalid_request_body". A web search call can't go without its id
// (OpenAI's schema has it required), so it goes bound under ws_, and the
// turn goes through, streamed or not.
func TestCopilotTakesItsOwnOpaqueSearchCallID(t *testing.T) {
	b := make([]byte, 339)
	for i := range b {
		b[i] = byte(i*197 + 3)
	}
	searchID := base64.StdEncoding.EncodeToString(b)
	if len(searchID) != 452 || strings.HasPrefix(searchID, "ws_") || !strings.ContainsAny(searchID, "+/") {
		t.Fatalf("fixture not of Copilot's shape: %d %q", len(searchID), searchID[:20])
	}
	up := &opaqueCopilot{}
	onOpaqueCopilot(t, up)
	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"what's new in Go 1.26?"}]},
	  {"type":"web_search_call","id":"` + searchID + `","status":"completed","action":{"type":"search","query":"Go 1.26 release notes"}},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Go 1.26 adds new(expr)."}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"and 1.25?"}]}]`
	s := New()
	for _, stream := range []bool{true, false} {
		body := `{"model":"copilot/gpt-6.1","stream":` + strconv.FormatBool(stream) + `,"store":false,"tools":[{"type":"web_search"}],"input":` + input + `}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("stream %v: %d %s", stream, rec.Code, rec.Body.String())
		}
	}
	asked := up.asked()
	if len(asked) != 2 {
		t.Fatalf("asked Copilot %d times", len(asked))
	}
	for _, q := range asked {
		items, _ := q["input"].([]any)
		var search map[string]any
		for _, raw := range items {
			if it, _ := raw.(map[string]any); it["type"] == "web_search_call" {
				search = it
			}
		}
		if search == nil {
			t.Fatalf("the web search call not sent: %v", items)
		}
		id, _ := search["id"].(string)
		if !strings.HasPrefix(id, "ws_") || len(id) != 64 {
			t.Errorf("the web search call sent under %q", id)
		}
	}
}
