package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func cursorFrame(msg pb) []byte { return connectFrame(msg) }

// cursorDuplex has a fake Cursor answer a Run while its body is still
// open, as Cursor does, and close the connection after the reply. A Go
// server in full duplex that ends a reply before the request's body has
// ended reads the rest of it in finishRequest, after it has stopped its
// background read, and that read's EOF starts another; the connection's
// next request then reads beside it and panics "invalid concurrent
// Body.Read call" (net/http recovers it, and the test goes on), in the
// log of CI Test run 37791950740 at 8db24fd6 and of green runs. A
// connection closed after the reply has no next request.
func cursorDuplex(w http.ResponseWriter) {
	http.NewResponseController(w).EnableFullDuplex()
	w.Header().Set("Connection", "close")
}

func cursorEnd(js string) []byte {
	b := connectFrame([]byte(js))
	b[0] = 2
	return b
}

func cursorUpdate(num int, msg pb) []byte {
	return cursorFrame(pb{}.bytes(1, pb{}.bytes(num, msg)))
}

// cursorCallFrame is the server handing the client a call of an MCP tool.
func cursorCallFrame(id uint64, callID, name, city string) []byte {
	arg := pb{}.str(1, "city").bytes(2, pb{}.str(3, city))
	return cursorFrame(pb{}.bytes(2, pb{}.varint(1, id).str(15, "exec-1").
		bytes(11, pb{}.str(1, "magpie-"+name).bytes(2, arg).str(3, callID).str(5, name))))
}

// cursorDecoded runs decode over frames, and is what it said and what it
// sent back.
func cursorDecoded(t *testing.T, blobs map[string][]byte, frames ...[]byte) ([]Event, [][]pbField, int) {
	t.Helper()
	pr, pw := io.Pipe()
	st := &cursorStream{pw: pw, blobs: blobs}
	sent := make(chan [][]pbField)
	go func() {
		var got [][]pbField
		br := bufio.NewReader(pr)
		for {
			f, err := readConnectFrame(br)
			if err != nil {
				sent <- got
				return
			}
			got = append(got, pbFields(f.data))
		}
	}()
	out := make(chan Event, 256)
	st.decode(context.Background(), bufio.NewReader(bytes.NewReader(bytes.Join(frames, nil))), out, nil)
	pw.Close()
	var evs []Event
	for ev := range out {
		evs = append(evs, ev)
	}
	return evs, <-sent, st.status
}

func TestCursorDecodesTextAndCalls(t *testing.T) {
	evs, sent, _ := cursorDecoded(t, map[string][]byte{"k1": []byte("blob")},
		cursorFrame(pb{}.bytes(4, pb{}.varint(1, 7).bytes(2, pb{}.str(1, "k1")))),
		cursorFrame(pb{}.bytes(4, pb{}.varint(1, 8).bytes(3, pb{}.str(1, "k2")))),
		cursorUpdate(4, pb{}.str(1, "Hmm.")),
		cursorUpdate(1, pb{}.str(1, "Let me ")),
		cursorUpdate(1, pb{}.str(1, "look.")),
		cursorUpdate(27, pb{}.varint(1, 2)),
		cursorCallFrame(3, "call_a\nfc_1", "get_weather", "Paris"),
		cursorCallFrame(4, "call_b\nfc_2", "get_weather", "Lima"),
		cursorUpdate(1, pb{}.str(1, "never read")),
	)
	want := `|think:Hmm.|text:Let me look.|call:call_a__fc_1/get_weather|args:{"city":"Paris"}|call:call_b__fc_2/get_weather|args:{"city":"Lima"}|stop:tool`
	if got := said(evs); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// the blob asked for is given, the one to keep acked
	if len(sent) != 2 || sent[0][0].num != 3 || sent[1][0].num != 3 {
		t.Fatalf("sent %v", sent)
	}
	got := pbFields(sent[0][0].data)
	if got[0].n != 7 || string(pbFields(got[1].data)[0].data) != "blob" {
		t.Fatalf("get_blob answered %v", got)
	}
	if cursorCallID("call_a__fc_1") != "call_a\nfc_1" || cursorCallID("toolu_x__fc_1") != "toolu_x__fc_1" {
		t.Fatal("call ids don't come back")
	}
}

func TestCursorDecodesTheEnd(t *testing.T) {
	evs, _, _ := cursorDecoded(t, nil,
		cursorUpdate(1, pb{}.str(1, "Hi")),
		cursorUpdate(14, pb{}.varint(1, 120).varint(2, 30).varint(3, 7)),
	)
	if got := said(evs); got != "|text:Hi|stop:stop" {
		t.Fatal(got)
	}
	for _, ev := range evs {
		if ev.Kind == KUsage && (ev.Usage.Input != 113 || ev.Usage.Output != 30 || ev.Usage.CacheRead != 7) {
			t.Fatalf("usage %+v", ev.Usage)
		}
	}

	evs, _, status := cursorDecoded(t, nil, cursorEnd(`{"error":{"code":"resource_exhausted","message":"Error","details":[{"debug":{"details":{"title":"Usage limit","detail":"You're out."}}}]}}`))
	if got := said(evs); !strings.HasSuffix(got, "Usage limit: You're out.") || status != 429 {
		t.Fatal(got, status)
	}
}

func TestCursorMessages(t *testing.T) {
	r := &Request{
		System: "Be brief.",
		Tools:  []Tool{{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "orphan", Text: "x"}, {Kind: Text, Text: "look at a"}}},
			{Role: "assistant", Parts: []Part{
				{Kind: Text, Text: "Reading."},
				{Kind: ToolCall, ID: "call_1__fc_1", Name: "read", Args: json.RawMessage(`{"p":"a"}`)},
				{Kind: ToolCall, ID: "c2", Name: "gone"},
			}},
			{Role: "user", Parts: []Part{
				{Kind: ToolResult, CallID: "call_1__fc_1", Text: `{"ok":1}`, IsError: true},
				{Kind: Image, MediaType: "image/png", Data: "AAE="},
				{Kind: Text, Text: "and this"},
			}},
		},
	}
	var got []string
	for _, m := range cursorMessages(r, bridgeTools(r)) {
		got = append(got, string(m))
	}
	lt, gt := `\u003c`, `\u003e` // as encoding/json escapes < and >
	sys := `{"content":"Be brief.\n\n` + lt + "dynamic_tool_catalog" + gt
	if !strings.HasPrefix(got[0], sys) || !strings.Contains(got[0], lt+`tool name=\"read\"`+gt+`\nRead a file\ninput schema: {\"type\":\"object\"}`) {
		t.Fatalf("system %s", got[0])
	}
	want := []string{
		`{"content":[{"text":"look at a","type":"text"}],"role":"user"}`,
		`{"content":[{"text":"Reading.","type":"text"},{"args":{"arguments":{"p":"a"},"namespace":"magpie","toolName":"read"},"toolCallId":"call_1\nfc_1","toolName":"CallDynamicTool","type":"tool-call"},{"args":{"arguments":{},"namespace":"magpie","toolName":"gone"},"toolCallId":"c2","toolName":"CallDynamicTool","type":"tool-call"}],"role":"assistant"}`,
		`{"content":[{"experimental_content":[{"text":"{\"ok\":1}","type":"text"}],"isError":true,"result":{"ok":1},"toolCallId":"call_1\nfc_1","toolName":"CallDynamicTool","type":"tool-result"},{"experimental_content":[{"text":"` + devinNoResult + `","type":"text"}],"isError":true,"result":"` + devinNoResult + `","toolCallId":"c2","toolName":"CallDynamicTool","type":"tool-result"}],"role":"tool"}`,
		`{"content":[{"image":{"__type":"Uint8Array","hex":"0001"},"mimeType":"image/png","type":"image"},{"text":"and this","type":"text"}],"role":"user"}`,
	}
	if strings.Join(got[1:], "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got[1:], "\n"), strings.Join(want, "\n"))
	}
}

func TestBuildCursorRun(t *testing.T) {
	msgs := [][]byte{[]byte(`{"role":"user","content":"hi"}`)}
	tools := []bridgeTool{{Name: "read", InputSchema: json.RawMessage(`{"type":"object","required":["p"]}`)}}
	run, blobs := buildCursorRun(msgs, "hi", tools, "gpt-5.4", "", false)
	rr := pbFields(pbFields(run)[0].data)
	var state []pbField
	var models, mcp []string
	for _, f := range rr {
		switch f.num {
		case 1:
			state = pbFields(f.data)
		case 3, 9:
			models = append(models, string(pbFields(f.data)[0].data))
		case 4:
			mcp = append(mcp, string(pbFields(pbFields(f.data)[0].data)[0].data))
		}
	}
	if strings.Join(models, ",") != "gpt-5.4,gpt-5.4" || strings.Join(mcp, ",") != "read" {
		t.Fatalf("models %v, tools %v", models, mcp)
	}
	var ids int
	for _, f := range state {
		switch f.num {
		case 1:
			ids++
			if string(blobs[string(f.data)]) != string(msgs[0]) {
				t.Fatal("the message's blob is missing")
			}
		case 8: // the turn, whose user message names the words
			turn := pbFields(blobs[string(f.data)])
			user := blobs[string(pbFields(turn[0].data)[0].data)]
			if string(pbFields(user)[0].data) != "hi" {
				t.Fatalf("turn %q", user)
			}
		}
	}
	if ids != 1 || len(blobs) != 3 {
		t.Fatalf("%d ids, %d blobs", ids, len(blobs))
	}
	// a schema goes as a google.protobuf.Value too, and reads back the same
	def := pbFields(cursorToolDef(tools[0]))
	b, _ := json.Marshal(pbAny(def[1].data))
	if string(b) != `{"required":["p"],"type":"object"}` {
		t.Fatal(string(b))
	}
}

func TestServeCursor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var reply []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent.v1.AgentService/Run" || r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("x-cursor-client-type") != "cli" {
			http.Error(w, `{"code":"unauthenticated","message":"no"}`, 401)
			return
		}
		f, err := readConnectFrame(bufio.NewReader(r.Body))
		if err != nil || len(pbFields(f.data)) == 0 {
			http.Error(w, `{"code":"invalid_argument","message":"no run"}`, 400)
			return
		}
		// the Run's body stays open: the reply is flushed, not held until
		// the body is read to its end
		cursorDuplex(w)
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Write(reply)
		w.(http.Flusher).Flush()
	}))
	defer up.Close()
	tok, ver, agent, api := cursorToken, cursorVersion, cursorAgent, cursorAPI
	cursorToken = func() (string, error) { return "tok", nil }
	cursorVersion = func() string { return "cli-test" }
	cursorAgent, cursorAPI = up.URL, up.URL // no server config there: the global API
	defer func() { cursorToken, cursorVersion, cursorAgent, cursorAPI = tok, ver, agent, api }()
	forgetCursorEndpoint(t)

	serve := func(from provider.Protocol, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		var u Usage
		New().serveCursor(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), from, "auto", []byte(body), &u)
		return w
	}
	reply = bytes.Join([][]byte{
		cursorUpdate(1, pb{}.str(1, "Hello")),
		cursorUpdate(27, pb{}.varint(1, 1)),
		cursorCallFrame(1, "toolu_1", "read", "x"),
	}, nil)
	w := serve(provider.Anthropic, `{"model":"x","max_tokens":10,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	var msg struct {
		Content []struct {
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	json.Unmarshal(w.Body.Bytes(), &msg)
	if w.Code != 200 || len(msg.Content) != 2 || msg.Content[0].Text != "Hello" || msg.Content[1].ID != "toolu_1" ||
		string(msg.Content[1].Input) != `{"city":"x"}` || msg.StopReason != "tool_use" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	reply = cursorEnd(`{"error":{"code":"resource_exhausted","message":"out of credits"}}`)
	if w = serve(provider.Chat, `{"model":"x","messages":[{"role":"user","content":"hi"}]}`); w.Code != 429 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func forgetCursorEndpoint(t *testing.T) {
	t.Helper()
	reset := func() {
		cursorEndpoint.Lock()
		cursorEndpoint.key, cursorEndpoint.url, cursorEndpoint.listed = "", "", false
		cursorEndpoint.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// cursorConfigServer is Cursor's server config, naming agent as the agent
// API; it counts how often it is asked.
func cursorConfigServer(t *testing.T, agent *string, asked *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aiserver.v1.ServerConfigService/GetServerConfig" || r.Header.Get("Authorization") == "" ||
			r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"code":"not_found"}`, 404)
			return
		}
		*asked++
		if *agent == "" {
			http.Error(w, `{"code":"internal"}`, 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"configVersion": "1",
			"agentUrlConfig": map[string]any{"agentUrl": *agent, "agentnUrl": *agent + "/n"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The agent API is the one the server config names, asked for once a
// token; the global one when the config can't be had.
func TestCursorAgentURL(t *testing.T) {
	forgetCursorEndpoint(t)
	agent, asked := "https://agent.us.example", 0
	srv := cursorConfigServer(t, &agent, &asked)
	api, global := cursorAPI, cursorAgent
	cursorAPI, cursorAgent = srv.URL, "https://global.example"
	defer func() { cursorAPI, cursorAgent = api, global }()
	s, ctx := New(), context.Background()

	if u := s.cursorAgentURL(ctx, "tok-a", false); u != "https://agent.us.example" || asked != 1 {
		t.Fatal(u, asked) // privacy mode: agentUrl, not agentnUrl
	}
	if u := s.cursorAgentURL(ctx, "tok-a", false); u != "https://agent.us.example" || asked != 1 {
		t.Fatal("not kept:", u, asked)
	}
	agent = "https://agent.eu.example"
	if u := s.cursorAgentURL(ctx, "tok-b", false); u != "https://agent.eu.example" || asked != 2 {
		t.Fatal("another account, the same endpoint:", u, asked)
	}
	agent = "https://agent.ap.example"
	if u := s.cursorAgentURL(ctx, "tok-b", true); u != "https://agent.ap.example" || asked != 3 {
		t.Fatal("fresh wasn't asked:", u, asked)
	}
	agent = ""
	if u := s.cursorAgentURL(ctx, "tok-c", false); u != "https://global.example" || asked != 4 {
		t.Fatal("no config, no global:", u, asked)
	}
	if u := s.cursorAgentURL(ctx, "tok-c", false); u != "https://global.example" || asked != 4 {
		t.Fatal("a failure is asked again at once:", u, asked)
	}
}

// A region the team isn't served in is asked of the config once more, and
// the Run tried again where it now says; still turned away, the error says
// so, not to sign in.
func TestServeCursorRegion(t *testing.T) {
	forgetCursorEndpoint(t)
	regionErr := `{"code":"unauthenticated","message":"Error","details":[{"debug":{"details":{"title":"Unauthorized request.","detail":"This region is not yet available for your team"}}}]}`
	var ran []string
	run := func(name string, ok bool) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = append(ran, name)
			readConnectFrame(bufio.NewReader(r.Body))
			cursorDuplex(w)
			w.Header().Set("Content-Type", "application/connect+proto")
			if !ok { // a stream's error comes at its end
				w.Write(cursorEnd(`{"error":` + regionErr + `}`))
			} else {
				w.Write(cursorUpdate(1, pb{}.str(1, "Hello")))
				w.Write(cursorUpdate(14, pb{}.varint(1, 3).varint(2, 1)))
			}
			w.(http.Flusher).Flush()
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	global, us := run("global", false), run("us", true)
	agent, asked := global.URL, 0
	srv := cursorConfigServer(t, &agent, &asked)
	tok, ver, api, glob := cursorToken, cursorVersion, cursorAPI, cursorAgent
	cursorToken = func() (string, error) { return "tok", nil }
	cursorVersion = func() string { return "cli-test" }
	cursorAPI, cursorAgent = srv.URL, global.URL
	defer func() { cursorToken, cursorVersion, cursorAPI, cursorAgent = tok, ver, api, glob }()

	serve := func() *httptest.ResponseRecorder {
		body := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
		w := httptest.NewRecorder()
		var u Usage
		New().serveCursor(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), provider.Chat, "auto", []byte(body), &u)
		return w
	}
	// the config kept from before names global; asked again, it says us
	s := New()
	s.cursorAgentURL(context.Background(), "tok", false)
	agent = us.URL
	if w := serve(); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") || strings.Join(ran, ",") != "global,us" || asked != 2 {
		t.Fatalf("%d %s %v %d", w.Code, w.Body, ran, asked)
	}
	// the config still says global: no second Run, and plain words
	forgetCursorEndpoint(t)
	agent, ran = global.URL, nil
	w := serve()
	if w.Code != 403 || strings.Join(ran, ",") != "global" || !strings.Contains(w.Body.String(), "region is not yet available") ||
		strings.Contains(strings.ToLower(w.Body.String()), "sign in") || strings.Contains(w.Body.String(), "Devin") {
		t.Fatalf("%d %s %v", w.Code, w.Body, ran)
	}
}

func TestCursorFailureWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		code   int
		has    string
		not    []string
	}{
		{401, `{"code":"unauthenticated","message":"Error","details":[{"debug":{"details":{"title":"Unauthorized request.","detail":"This region is not yet available for your team"}}}]}`,
			403, "This region is not yet available for your team", []string{"sign in", "Devin"}},
		{401, `{"code":"unauthenticated","message":"token expired"}`, 401, "token expired — sign in to Cursor again in magpie", []string{"Devin"}},
		{401, `Unauthorized`, 401, "sign in to Cursor again", []string{"Devin"}},
		{403, `{"code":"permission_denied","message":"Your team doesn't allow this model"}`, 403, "Your team doesn't allow this model", []string{"sign in"}},
		{200, `{"error":{"code":"resource_exhausted","message":"out of credits"}}`, 429, "usage limit reached: out of credits", nil},
	} {
		status, msg := cursorFailure(c.status, []byte(c.body))
		if status != c.code || !strings.Contains(msg, c.has) {
			t.Errorf("%s: %d %q", c.body, status, msg)
		}
		for _, n := range c.not {
			if strings.Contains(strings.ToLower(msg), strings.ToLower(n)) {
				t.Errorf("%s: %q says %q", c.body, msg, n)
			}
		}
	}
	if status, msg := cursorFailure(200, []byte(`{}`)); status != 200 || msg != "" {
		t.Fatal("a stream that ended well:", status, msg)
	}
}

// A model magpie offers goes to Cursor as the id its effort picks; one of
// Cursor's own ids at an effort goes at the effort asked for, where Cursor
// has that one, else as it is.
func TestCursorModelID(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var raw []catalog.Model
	for _, l := range strings.Split(strings.TrimSpace(`
grok-4.7-low - Grok 4.7 Low
grok-4.7-low-fast - Grok 4.7 Low Fast
grok-4.7-medium - Grok 4.7 Medium
grok-4.7-medium-fast - Grok 4.7 Medium Fast
grok-4.7-high - Grok 4.7 High
grok-4.7-high-fast - Grok 4.7 High Fast
grok-4.7-xhigh - Grok 4.7 Extra High
grok-4.7-xhigh-fast - Grok 4.7 Extra High Fast
gpt-5.2-low - GPT-5.2 Low
gpt-5.2 - GPT-5.2
gpt-5.2-high - GPT-5.2 High
gpt-5.2-xhigh - GPT-5.2 Extra High
claude-4.6-opus-high - Claude Opus 4.6 1M
claude-4.6-opus-max - Claude Opus 4.6 1M Max
claude-4.6-opus-high-thinking - Claude Opus 4.6 1M Thinking
claude-4.6-opus-max-thinking - Claude Opus 4.6 1M Max Thinking
gpt-5.5-medium - GPT-5.5 1M
gpt-5.5-extra-high - GPT-5.5 1M Extra High
claude-4.5-sonnet - Claude Sonnet 4.5`), "\n") {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("cursor", "", raw); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		model, effort string
		fast          bool
		want          string
	}{
		{"grok-4.7", "", false, "grok-4.7-medium"}, // every name says its effort: medium
		{"grok-4.7", "high", false, "grok-4.7-high"},
		{"grok-4.7", "max", false, "grok-4.7-xhigh"},
		{"grok-4.7", "high", true, "grok-4.7-high-fast"},
		{"grok-4.7-fast", "low", false, "grok-4.7-low-fast"},
		{"gpt-5.2", "medium", false, "gpt-5.2"}, // between low and high: the unnamed one
		{"gpt-5.2", "max", false, "gpt-5.2-xhigh"},
		{"gpt-5.2", "low", true, "gpt-5.2-low"}, // no fast one
		{"claude-4.6-opus", "medium", false, "claude-4.6-opus-high"},
		{"claude-4.6-opus-thinking", "xhigh", false, "claude-4.6-opus-max-thinking"},
		{"gpt-5.5", "xhigh", false, "gpt-5.5-extra-high"},
		{"grok-4.7-low-fast", "high", false, "grok-4.7-high-fast"}, // Cursor's own id: the effort asked for wins
		{"grok-4.7-low", "high", false, "grok-4.7-high"},
		{"grok-4.7-low", "", false, "grok-4.7-low"},
		{"grok-4.7-low", "", true, "grok-4.7-low-fast"},
		{"grok-4.7-low", "minimal", false, "grok-4.7-low"}, // Cursor has none at minimal
		{"gpt-5.2-high", "low", true, "gpt-5.2-low"},       // no fast one
		{"claude-4.6-opus-high-thinking", "max", false, "claude-4.6-opus-max-thinking"},
		{"claude-4.5-sonnet", "high", false, "claude-4.5-sonnet"},
		{"auto", "", false, "auto"},
	} {
		if got := cursorModelID(c.model, c.effort, c.fast); got != c.want {
			t.Errorf("%s at %q fast %v: %s, want %s", c.model, c.effort, c.fast, got, c.want)
		}
	}
}
