package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// toolsUpstream is a Responses relay in front of a Chat model (LiteLLM's
// bridge, #4910): every ask is answered with one Bash call whose call_id is
// fresh each reply, streamed the way Responses does.
type toolsUpstream struct {
	mu   sync.Mutex
	n    int
	got  []string
	item string // the id field of the function_call item, as the bridge writes it
}

func (u *toolsUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b []byte
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	b = buf[:n]
	u.mu.Lock()
	u.n++
	call := fmt.Sprintf("call_00_%05d", u.n)
	u.got = append(u.got, string(b))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	item := u.item
	if item == "" {
		item = call // the bridge writes the model's own id on the item too
	}
	io := func(s string) { fmt.Fprint(w, s) }
	io("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_" + call + "\",\"status\":\"in_progress\"}}\n\n")
	io("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"" + item + "\",\"call_id\":\"" + call + "\",\"name\":\"Bash\",\"arguments\":\"\"}}\n\n")
	io("data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"" + item + "\",\"delta\":\"{\\\"command\\\":\\\"date\\\"}\"}\n\n")
	io("data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"" + item + "\",\"arguments\":\"{\\\"command\\\":\\\"date\\\"}\"}\n\n")
	io("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"" + item + "\",\"call_id\":\"" + call + "\",\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"date\\\"}\"}}\n\n")
	io("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_" + call + "\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"" + item + "\",\"call_id\":\"" + call + "\",\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"date\\\"}\"}],\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n")
}

func (u *toolsUpstream) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.got...)
}

// messagesUpstream is an upstream that speaks Messages itself, and answers
// every ask with Bash calls under the ids a bridge numbers by their place
// in the reply (#4910): fc_0 for the first block of every reply, so the
// same id on every turn of a session. minted is what each reply names its
// calls.
type messagesUpstream struct {
	mu      sync.Mutex
	n       int
	got     []string
	minted  []string                // what every reply names its calls; none, fc_0 each turn
	perTurn func(turn int) []string // or fresh ids of its own, one turn to the next
}

func (u *messagesUpstream) ids() []string {
	if u.perTurn != nil {
		return u.perTurn(u.n)
	}
	if len(u.minted) > 0 {
		return u.minted
	}
	return []string{"fc_0"}
}

func (u *messagesUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b []byte
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	b = buf[:n]
	u.mu.Lock()
	u.n++
	turn := u.n
	u.got = append(u.got, string(b))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if !streamOf(b) {
		// a reply that came whole: its content carries the calls directly
		var content []string
		for _, id := range u.ids() {
			content = append(content, `{"type":"tool_use","id":"`+id+`","name":"Bash","input":{"command":"date"}}`)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_l`+fmt.Sprint(turn)+`","type":"message","role":"assistant","model":"m","content":[`+strings.Join(content, ",")+`],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":3}}`)
		return
	}
	io := func(s string) { fmt.Fprint(w, s) }
	io("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_l" + fmt.Sprint(turn) + "\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
	for i, id := range u.ids() {
		io("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":" + fmt.Sprint(i) + ",\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + id + "\",\"name\":\"Bash\",\"input\":{}}}\n\n")
		io("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":" + fmt.Sprint(i) + ",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"date\\\"}\"}}\n\n")
		io("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":" + fmt.Sprint(i) + "}\n\n")
	}
	io("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n")
	io("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func (u *messagesUpstream) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.got...)
}

// messageToolUseIDs is the ids of every tool_use block of a Messages reply,
// streamed or whole, in the order the reply said them.
func messageToolUseIDs(t *testing.T, body string) []string {
	t.Helper()
	var ids []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			ContentBlock struct {
				Type string
				ID   string
			} `json:"content_block"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && ev.ContentBlock.Type == "tool_use" && ev.ContentBlock.ID != "" {
			ids = append(ids, ev.ContentBlock.ID)
		}
	}
	if len(ids) > 0 {
		return ids
	}
	var m struct {
		Content []struct {
			Type string
			ID   string
		}
	}
	json.Unmarshal([]byte(body), &m)
	for _, p := range m.Content {
		if p.Type == "tool_use" && p.ID != "" {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		t.Fatalf("no tool_use in the reply: %s", body)
	}
	return ids
}

// callAndResults asks for a call on a turn of its own, then answers it the
// way Claude Code does, under the id it was given, and asks again on a new
// prompt: the ids the client saw on the first and the second reply, the
// id its answer went back under, and the upstream's second request.
func callAndResults(t *testing.T, s http.Handler, up *messagesUpstream, model string) (first, second, answered string) {
	t.Helper()
	post := func(prompt string, history string) string {
		rec := httptest.NewRecorder()
		body := `{"model":"` + model + `","max_tokens":100,"stream":true,` +
			`"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],` +
			`"messages":[{"role":"user","content":"Call the Bash tool with command: ` + prompt + `"}` + history + `]}`
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	turns := func() int {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.n
	}
	firstIDs := messageToolUseIDs(t, post("echo alpha", ""))
	first = firstIDs[0]
	before := turns()
	quoted, _ := json.Marshal(first)
	secondIDs := messageToolUseIDs(t, post("echo beta", `,
 {"role":"assistant","content":[{"type":"tool_use","id":`+string(quoted)+`,"name":"Bash","input":{"command":"date"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":`+string(quoted)+`,"content":"Thu Oct  8 10:00:00 CST 2026"}]}`))
	second = secondIDs[0]
	if turns() != before+1 {
		t.Fatalf("the answer made %d requests at the upstream, want 1", turns()-before)
	}
	// the answer to the first call goes back under the id the client was
	// given: on the reply it answers and on the result answering it
	body := func() string {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.got[len(up.got)-1]
	}()
	var q struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("upstream request: %v: %s", err, body)
	}
	var uses, results []string
	for _, m := range q.Messages {
		var blocks []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue // a content of text alone
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				uses = append(uses, b.ID)
			case "tool_result":
				results = append(results, b.ToolUseID)
			}
		}
	}
	if len(uses) != 1 || len(results) != 1 {
		t.Fatalf("upstream request carried calls %q, results %q: %s", uses, results, body)
	}
	return first, second, uses[0] + " " + results[0]
}

// A tool call on a turn of its own, then one on a later turn, at an
// upstream that speaks Messages itself and is relayed as it came: whatever
// bridge numbered the call's id, the client may not see the one id twice
// across a session. Claude Code keeps every tool_use id of a session in one
// Set and drops a block whose id it has seen again (#4910): the whole later
// turn then collapses into "[Tool use interrupted]". The first id passes as
// it came, the second goes out salted, and the upstream that made the call
// gets its own back on the result answering it.
func TestMessagesRelayedToolUseIDsUniqueAcrossTurns(t *testing.T) {
	fresh(t)
	up := &messagesUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "k", Models: []string{"ds-v4"}, Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	first, second, answered := callAndResults(t, New().Handler(), up, "bridge/ds-v4")
	if first != "fc_0" {
		t.Fatalf("the first turn's id %q was changed; an id the client has seen no more goes as it came", first)
	}
	if second == first {
		t.Fatalf("both turns carried the one tool_use id %q; Claude Code drops the second as one it has seen (#4910)", first)
	}
	if want := "fc_0 fc_0"; answered != want {
		t.Fatalf("the answer went back under %q, want %q: the upstream gets the id it gave, on the call and on the result", answered, want)
	}
}

// Two calls of one reply that a bridge numbered alike are two to the
// client, or the second is dropped the same as a later turn's (#4910).
func TestMessagesRelayedToolUseIDsUniqueInATurn(t *testing.T) {
	fresh(t)
	up := &messagesUpstream{minted: []string{"fc_0", "fc_0"}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "k", Models: []string{"ds-v4"}, Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body := `{"model":"bridge/ds-v4","max_tokens":100,"stream":true,` +
		`"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],` +
		`"messages":[{"role":"user","content":"Call the Bash tool twice"}]}`
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	ids := messageToolUseIDs(t, rec.Body.String())
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("one reply carried tool_use ids %q, want two different ones", ids)
	}
}

// An upstream whose ids are its own, fresh one turn to the next, is
// relayed as it came: no id changes on the way out, and the answer goes
// back under it untouched.
func TestMessagesRelayedOwnToolUseIDsUntouched(t *testing.T) {
	fresh(t)
	up := &messagesUpstream{perTurn: func(turn int) []string {
		return []string{fmt.Sprintf("toolu_01A09q90qw90lq9178%02dlq9", turn)}
	}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "k", Models: []string{"ds-v4"}, Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	first, second, answered := callAndResults(t, New().Handler(), up, "bridge/ds-v4")
	one, two := "toolu_01A09q90qw90lq917801lq9", "toolu_01A09q90qw90lq917802lq9"
	if first != one || second != two || answered != one+" "+one {
		t.Fatalf("an upstream's own ids went out %q and %q and were answered %q; they go as they came", first, second, answered)
	}
}

// A tool call on a turn of its own, then one on a later turn, translated
// from a Responses upstream (a relay in front of LiteLLM's): the ids are
// the model's own there already, one fresh a reply.
func TestMessagesTranslatedToolUseIDsUniqueAcrossTurns(t *testing.T) {
	for _, c := range []struct {
		name string
		item string // the function_call item's own id field
	}{
		{"item id like its call id", ""},
		{"item id the bridge numbered", "fc_0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &toolsUpstream{item: c.item}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			if err := provider.Save(provider.Provider{ID: "litellm", Name: "LiteLLM", Key: "k", Models: []string{"ds-v4"}, Responses: srv.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			s := New().Handler()
			post := func(prompt string) string {
				rec := httptest.NewRecorder()
				body := `{"model":"litellm/ds-v4","max_tokens":100,"stream":true,` +
					`"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],` +
					`"messages":[{"role":"user","content":"Call the Bash tool with command: ` + prompt + `"}]}`
				s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				return rec.Body.String()
			}
			first := messageToolUseIDs(t, post("echo alpha"))[0]
			second := messageToolUseIDs(t, post("echo beta"))[0]
			if first == second {
				t.Fatalf("both turns carried the one tool_use id %q (#4910)", first)
			}
		})
	}
}

// The same disease in a reply that came whole, which the relay hands on as
// one JSON body: an id the client holds goes out salted there too (#4910).
func TestMessagesRelayedWholeToolUseIDsUniqueAcrossTurns(t *testing.T) {
	fresh(t)
	up := &messagesUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "k", Models: []string{"ds-v4"}, Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	s := New().Handler()
	ask := func(prompt string, history string) string {
		rec := httptest.NewRecorder()
		body := `{"model":"bridge/ds-v4","max_tokens":100,` +
			`"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],` +
			`"messages":[{"role":"user","content":"Call the Bash tool with command: ` + prompt + `"}` + history + `]}`
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	first := messageToolUseIDs(t, ask("echo alpha", ""))
	second := messageToolUseIDs(t, ask("echo beta", `,
 {"role":"assistant","content":[{"type":"tool_use","id":"fc_0","name":"Bash","input":{"command":"date"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_0","content":"Thu Oct  8 10:00:00 CST 2026"}]}`))
	if len(first) != 1 || first[0] != "fc_0" {
		t.Fatalf("the first turn's id %q was changed; an id the client has seen no more goes as it came", first)
	}
	if second[0] == first[0] {
		t.Fatalf("both turns carried the one tool_use id %q (#4910)", first)
	}
	// and the whole reply is still a whole reply the client can parse
	var m map[string]any
	if err := json.Unmarshal([]byte(ask("echo gamma", "")), &m); err != nil {
		t.Fatalf("a whole reply came back as %v: %s", err, ask("echo gamma", ""))
	}
}

// A salted id (#4910) that a later turn sends to an upstream of another
// wire — a group that moved on — goes there as the client holds it, never
// decoded into the upstream's id and the salt's NUL, and the ids
// anthropicOutID made for other vendors still come back as they were.
func TestSaltedToolUseIDTranslatedAsHeld(t *testing.T) {
	salted := saltCallID("fc_0", newID())
	if got := anthropicInID(salted); got != salted || strings.ContainsRune(got, 0) {
		t.Fatalf("salted id to a translated upstream: %q, want %q", got, salted)
	}
	for _, id := range []string{"Bash:0#a65b", "call_1", "mp_x"} {
		if got := anthropicInID(anthropicOutID(id)); got != id {
			t.Errorf("anthropicInID(anthropicOutID(%q)) = %q", id, got)
		}
	}
}
