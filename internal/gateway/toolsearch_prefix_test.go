package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// toolSearchTurns are three turns of one conversation from a Responses client
// that searches for its tools (Codex, pi): the ask; the ask, the search
// and the tools it found; and then a call to one of them.
func toolSearchTurns(model string) []string {
	head := `{"model":"` + model + `","stream":true,"instructions":"be brief",` + searchTools + `,"input":[`
	ask := `{"type":"message","role":"user","content":[{"type":"input_text","text":"make a calendar event"}]}`
	search := `{"type":"tool_search_call","call_id":"s1","execution":"client","arguments":{"query":"calendar create","limit":2}},` +
		`{"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[` +
		`{"type":"namespace","name":"mcp__cal","description":"Calendar","tools":[` +
		`{"type":"function","name":"create_event","description":"Create a calendar event.","defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}}}}]}]}`
	call := `{"type":"function_call","call_id":"c1","namespace":"mcp__cal","name":"create_event","arguments":"{\"title\":\"x\"}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"created"}`
	return []string{
		head + ask + `]}`,
		head + ask + `,` + search + `]}`,
		head + ask + `,` + search + `,` + call + `]}`,
	}
}

// searchUpstream is a Responses upstream that records every body it is
// sent and answers each with an empty turn; refuse, when set, answers a
// body it gives a code for with it.
type searchUpstream struct {
	mu     sync.Mutex
	got    [][]byte
	refuse func(body []byte) (int, string)
}

func (u *searchUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got = append(u.got, b)
	refuse := u.refuse
	u.mu.Unlock()
	if refuse != nil {
		if code, reply := refuse(b); code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, reply)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func (u *searchUpstream) sent() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.got...)
}

// dialAll is a server whose every upstream connection goes to up, whatever
// host its address names: a provider at a vendor's own address
// (api.deepseek.com, api.openai.com) is served by the test's upstream.
func dialAll(up *httptest.Server) *Server {
	s := New()
	addr := strings.TrimPrefix(up.URL, "http://")
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	return s
}

// prefixKept fails unless each body sent upstream begins as the one before
// it: the same instructions and tools, and the earlier one's input items
// first, byte for byte once JSON's whitespace is left out (the fixture's
// tools are indented, and a body magpie rewrites is sent compact; neither
// is in the prompt a vendor renders). That prefix is what a vendor's
// prompt cache reads back; a tool added to tools, or an earlier item
// rewritten, misses it.
func prefixKept(t *testing.T, sent [][]byte) {
	t.Helper()
	compact := func(v gjson.Result) string {
		var b bytes.Buffer
		if json.Compact(&b, []byte(v.Raw)) != nil {
			return v.Raw
		}
		return b.String()
	}
	for i := 1; i < len(sent); i++ {
		prev, cur := gjson.ParseBytes(sent[i-1]), gjson.ParseBytes(sent[i])
		for _, f := range []string{"instructions", "tools"} {
			if was, now := compact(prev.Get(f)), compact(cur.Get(f)); was != now {
				t.Errorf("turn %d's %s are not turn %d's:\n%s\n%s", i+1, f, i, was, now)
			}
		}
		was, now := prev.Get("input").Array(), cur.Get("input").Array()
		if len(now) < len(was) {
			t.Errorf("turn %d has %d input items, turn %d had %d", i+1, len(now), i, len(was))
			continue
		}
		for j := range was {
			if a, b := compact(was[j]), compact(now[j]); a != b {
				t.Errorf("turn %d's input[%d] is not turn %d's:\n%s\n%s", i+1, j, i, a, b)
			}
		}
	}
}

// postTurns sends each turn to s as a Responses client does.
func postTurns(t *testing.T, s *Server, turns []string) {
	t.Helper()
	for i, body := range turns {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("turn %d: status %d: %s", i+1, rec.Code, rec.Body)
		}
	}
}

// A Responses client's tool search keeps the prompt's prefix on every
// upstream that can load a found tool where it was found, as it does on a
// Codex account. DeepSeek's own Responses API runs a client's tool search
// for deepseek-flash, loading the tools after the history, so it is sent
// the search as the client sent it. OpenAI's API, and a provider the user
// says takes additional_tools, is sent the search as a function and the
// tools it found in an additional_tools item after its result. Before,
// both were sent the found tools in tools, which changed from the turn a
// search found one (tourweint on X).
func TestToolSearchKeepsThePrefix(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		p           provider.Provider
	}{
		{name: "deepseek-flash on DeepSeek's API", model: "deepseek/deepseek-flash",
			p: provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Responses: "http://api.deepseek.com/v1", Models: []string{"deepseek-flash"}}},
		{name: "OpenAI's API", model: "openai/gpt-5.5",
			p: provider.Provider{ID: "openai", Name: "OpenAI", Key: "k", Responses: "http://api.openai.com/v1", Models: []string{"gpt-5.5"}}},
		{name: "additional-tools=on", model: "relay/m1",
			p: provider.Provider{ID: "relay", Name: "Relay", Key: "k", Responses: "http://relay.example.invalid/v1", Models: []string{"m1"}, AdditionalTools: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up := &searchUpstream{}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			if err := provider.Save(tc.p); err != nil {
				t.Fatal(err)
			}
			postTurns(t, dialAll(srv), toolSearchTurns(tc.model))
			sent := up.sent()
			if len(sent) != 3 {
				t.Fatalf("upstream asked %d times, want 3", len(sent))
			}
			prefixKept(t, sent)
		})
	}
}

// The same three turns on a Codex account go as Codex sent them, and kept
// the prefix before this change as after.
func TestToolSearchKeepsThePrefixOnCodex(t *testing.T) {
	codexSignedIn(t)
	up := &searchUpstream{}
	chatgptBase(t, up)
	postTurns(t, New(), toolSearchTurns("codex/gpt-5.5"))
	sent := up.sent()
	if len(sent) != 3 {
		t.Fatalf("upstream asked %d times, want 3", len(sent))
	}
	prefixKept(t, sent)
	if !strings.Contains(string(sent[2]), `"tool_search_call"`) || !strings.Contains(string(sent[2]), `"type":"tool_search"`) {
		t.Fatalf("Codex's search did not go as Codex sent it: %s", sent[2])
	}
}

// DeepSeek's own API is sent a deepseek-flash turn's tool search as the
// client sent it, the found tools in the search's output where they were
// found; a model it doesn't run the search for (deepseek-v4-pro ignores
// the tool and its items) and another host serving DeepSeek's models get
// it as a function, as before.
func TestToolSearchToDeepSeekScope(t *testing.T) {
	for _, tc := range []struct {
		name, model, at string
		native          bool
	}{
		{name: "deepseek-flash", model: "deepseek-flash", at: "http://api.deepseek.com/v1", native: true},
		{name: "deepseek-v4.1-flash", model: "deepseek-v4.1-flash", at: "http://api.deepseek.com/v1", native: true},
		{name: "deepseek-v4-pro", model: "deepseek-v4-pro", at: "http://api.deepseek.com/v1"},
		{name: "another host", model: "deepseek-flash", at: "http://relay.example.invalid/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up := &searchUpstream{}
			srv := httptest.NewServer(up)
			t.Cleanup(srv.Close)
			if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Responses: tc.at, Models: []string{tc.model}}); err != nil {
				t.Fatal(err)
			}
			postTurns(t, dialAll(srv), toolSearchTurns("ds/" + tc.model)[2:])
			sent := up.sent()
			if len(sent) != 1 {
				t.Fatalf("upstream asked %d times, want once", len(sent))
			}
			got := string(sent[0])
			native := strings.Contains(got, `"tool_search_call"`) && strings.Contains(got, `"tool_search_output"`) &&
				strings.Contains(got, `"type":"tool_search"`) && strings.Contains(got, `"defer_loading":true`)
			if native != tc.native {
				t.Fatalf("search sent as the client sent it: %v, want %v: %s", native, tc.native, got)
			}
			if !tc.native && (!strings.Contains(got, `"name":"tool_search"`) || strings.Contains(got, `"tool_search_call"`)) {
				t.Fatalf("not sent as a function: %s", got)
			}
		})
	}
}

// DeepSeek turning the search away (a model or a day it doesn't run it
// for) is asked again with it as a function, and the provider is sent it
// so from then on.
func TestToolSearchRefusedByDeepSeek(t *testing.T) {
	fresh(t)
	up := &searchUpstream{refuse: func(b []byte) (int, string) {
		if strings.Contains(string(b), `"tool_search_call"`) || strings.Contains(string(b), `"type":"tool_search"`) {
			return http.StatusBadRequest, `{"error":{"message":"Unsupported tool type: tool_search","type":"invalid_request_error"}}`
		}
		return 0, ""
	}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Responses: "http://api.deepseek.com/v1", Models: []string{"deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	s := dialAll(srv)
	turns := toolSearchTurns("deepseek/deepseek-flash")
	postTurns(t, s, turns[2:])
	postTurns(t, s, turns[2:])
	sent := up.sent()
	if len(sent) != 3 {
		t.Fatalf("upstream asked %d times, want the refused one, its retry and the next", len(sent))
	}
	for _, b := range sent[1:] {
		if strings.Contains(string(b), `"tool_search_call"`) || !strings.Contains(string(b), `"name":"tool_search"`) {
			t.Fatalf("not asked again with the search as a function: %s", b)
		}
	}
}

// A provider that turns additional_tools away is asked again with the
// found tools in tools, as before, and not sent the item again.
func TestToolSearchInPlaceRefused(t *testing.T) {
	fresh(t)
	up := &searchUpstream{refuse: func(b []byte) (int, string) {
		if strings.Contains(string(b), `"additional_tools"`) {
			return http.StatusBadRequest, `{"error":{"message":"Invalid value: 'additional_tools'. Supported values are: 'message', 'function_call'.","type":"invalid_request_error","param":"input[3].type"}}`
		}
		return 0, ""
	}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Responses: "http://relay.example.invalid/v1", Models: []string{"m1"}, AdditionalTools: true}); err != nil {
		t.Fatal(err)
	}
	s := dialAll(srv)
	turns := toolSearchTurns("relay/m1")
	postTurns(t, s, turns[2:])
	postTurns(t, s, turns[2:])
	sent := up.sent()
	if len(sent) != 3 {
		t.Fatalf("upstream asked %d times, want the refused one, its retry and the next", len(sent))
	}
	if !strings.Contains(string(sent[0]), `"additional_tools"`) {
		t.Fatalf("the first ask had no additional_tools: %s", sent[0])
	}
	for _, b := range sent[1:] {
		got := gjson.ParseBytes(b)
		if strings.Contains(got.Raw, `"additional_tools"`) || strings.Join(tsNames(got.Get("tools")), ",") != "exec_command,tool_search,mcp__cal" {
			t.Fatalf("not asked again with the found tools in tools: %s", b)
		}
	}
}

// Translated for a Responses API that takes the item, a search's found
// tools go in an additional_tools item after its result, not in tools.
func TestToolSearchTranslatedInPlace(t *testing.T) {
	turns := toolSearchTurns("m")
	var sent [][]byte
	for _, body := range turns {
		r, err := parseResponses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		r.LoadedInPlace = true
		sent = append(sent, buildResponses(r, "m", "api.openai.com", false))
	}
	prefixKept(t, sent)
	got := gjson.ParseBytes(sent[2])
	if names := strings.Join(tsNames(got.Get("tools")), ","); names != "exec_command,tool_search" {
		t.Fatalf("tools = %s", names)
	}
	in := got.Get("input").Array()
	if len(in) < 4 || in[2].Get("type").String() != "function_call_output" || in[3].Get("type").String() != "additional_tools" ||
		in[3].Get("tools.0.name").String() != "mcp__cal__create_event" {
		t.Fatalf("input = %s", got.Get("input").Raw)
	}
}
