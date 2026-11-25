package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// sampleUpstream is an Anthropic provider with a Haiku, a Sonnet and an
// Opus that answers each round with reply(the request's body): the SSE
// it streams, or a status and a body when the int isn't 200. It records
// every request.
type sampleUpstream struct {
	mu    sync.Mutex
	asked []map[string]any
	keys  []string
	uas   []string
}

func (u *sampleUpstream) bodies() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.asked...)
}

func newSampleUpstream(t *testing.T, reply func(body map[string]any) (int, string)) *sampleUpstream {
	t.Helper()
	u := &sampleUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		u.mu.Lock()
		u.asked = append(u.asked, m)
		u.keys = append(u.keys, r.Header.Get("x-api-key"))
		u.uas = append(u.uas, r.Header.Get("User-Agent"))
		u.mu.Unlock()
		code, body := reply(m)
		if code != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "sk-fake", Anthropic: srv.URL, Models: []string{"claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5"}}); err != nil {
		t.Fatal(err)
	}
	return u
}

// sampleSSE is a streamed answer of text pieces that stops for stop.
func sampleSSE(stop string, pieces ...string) string {
	lines := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}
	for _, p := range pieces {
		b, _ := json.Marshal(p)
		lines = append(lines, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(b)+`}}`)
	}
	lines = append(lines,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"`+stop+`"},"usage":{"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	return sse(lines...)
}

// pageSample is one round as runtime.js sends it: its status, its NDJSON
// lines, or its error.
func pageSample(h http.Handler, ref string, call map[string]any, hdr ...string) (int, []map[string]any, map[string]any) {
	b, _ := json.Marshal(call)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/sample", strings.NewReader(string(b)))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
			continue
		}
		if hdr[i] == "RemoteAddr" {
			r.RemoteAddr = hdr[i+1]
			continue
		}
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, nil, out
	}
	var lines []map[string]any
	sc := bufio.NewScanner(w.Body)
	for sc.Scan() {
		var l map[string]any
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	return w.Code, lines, nil
}

// sampleText is the text the lines carry, and their end.
func sampleText(lines []map[string]any) (string, map[string]any, map[string]any) {
	var b strings.Builder
	var end, fail map[string]any
	for _, l := range lines {
		if s, ok := l["text"].(string); ok {
			b.WriteString(s)
		}
		if e, ok := l["end"].(map[string]any); ok {
			end = e
		}
		if e, ok := l["error"].(map[string]any); ok {
			fail = e
		}
	}
	return b.String(), end, fail
}

func pageAsks(s string) []map[string]any {
	return []map[string]any{{"role": "user", "content": s}}
}

// A page that declares sample asks a model: the answer streams back as
// it comes, from the tier's model, under a system line that names no
// product and no vendor, and an answer is replayed to the same call.
func TestArtifactPageSample(t *testing.T) {
	h := artifacts(t)
	u := newSampleUpstream(t, func(map[string]any) (int, string) { return 200, sampleSSE("end_turn", "Hel", "lo") })
	a := selfPage(t, `{"sample": {}}`, nil)
	tok := ptok(t, a.ID)

	code, lines, out := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("Say hello")})
	text, end, fail := sampleText(lines)
	if code != 200 || text != "Hello" || end == nil || fail != nil || end["stop"] != "end_turn" || end["tier"] != "default" || end["truncated"] != false {
		t.Fatalf("a call: %d %v %v", code, lines, out)
	}
	if len(lines) != 3 || lines[0]["text"] != "Hel" {
		t.Errorf("not streamed piece by piece: %v", lines)
	}
	asked := u.bodies()
	if len(asked) != 1 || asked[0]["model"] != "claude-sonnet-4-5" || asked[0]["stream"] != true {
		t.Fatalf("upstream: %v", asked)
	}
	sys, _ := asked[0]["system"].(string)
	if sys == "" {
		// a system line in blocks, as routing may send it
		b, _ := json.Marshal(asked[0]["system"])
		sys = string(b)
	}
	for _, word := range []string{"magpie", "Magpie", "Claude", "Anthropic", "artifact"} {
		if strings.Contains(sys, word) {
			t.Errorf("the system line names %s: %s", word, sys)
		}
	}
	if k := u.keys[0]; k != "sk-fake" {
		t.Errorf("upstream key %q", k)
	}

	// the same call again is replayed; with cache off, or refresh, it isn't
	code, lines, _ = pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("Say hello")})
	if text, end, _ := sampleText(lines); code != 200 || text != "Hello" || end["cached"] != true || len(u.bodies()) != 1 {
		t.Fatalf("a replay: %d %v, %d asked", code, lines, len(u.bodies()))
	}
	pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("Say hello"), "cache": map[string]any{"off": true}})
	pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("Say hello"), "cache": map[string]any{"refresh": true}})
	if n := len(u.bodies()); n != 3 {
		t.Fatalf("cache off and refresh asked %d times in all, want 3", n)
	}
	// another page's same call isn't this one's
	b := selfPage(t, `{"sample": {}}`, nil)
	pageSample(h, b.ID, map[string]any{"t": ptok(t, b.ID), "messages": pageAsks("Say hello")})
	if n := len(u.bodies()); n != 4 {
		t.Fatalf("another page got this one's answer: %d asked", n)
	}

	// the tiers
	for tier, model := range map[string]string{"quick": "claude-haiku-4-5", "complex": "claude-opus-4-5", "inherit": "claude-sonnet-4-5"} {
		code, lines, out := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("tier " + tier), "modelTier": tier})
		asked := u.bodies()
		if _, end, _ := sampleText(lines); code != 200 || asked[len(asked)-1]["model"] != model || end["model"] != nil {
			t.Errorf("%s: %d %v %v, asked %v", tier, code, lines, out, asked[len(asked)-1]["model"])
		}
	}
	// a model named, which says it answered
	code, lines, _ = pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("named"), "model": "claude-opus-4-5"})
	asked = u.bodies()
	if _, end, _ := sampleText(lines); code != 200 || asked[len(asked)-1]["model"] != "claude-opus-4-5" || end["model"] != "claude-opus-4-5" || end["tier"] != "complex" {
		t.Errorf("a named model: %d %v", code, lines)
	}
	// a json call says so to the model
	pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("as json"), "json": true})
	asked = u.bodies()
	if sys, _ := json.Marshal(asked[len(asked)-1]["system"]); !strings.Contains(string(sys), "JSON") {
		t.Errorf("a json call's system line: %s", sys)
	}
}

// A round is the artifacts' own, not the browser's that opened the page:
// it counts under SampleAgent in Usage, and the browser's User-Agent
// goes no further than magpie.
func TestArtifactPageSampleIsArtifacts(t *testing.T) {
	h := artifacts(t)
	u := newSampleUpstream(t, func(map[string]any) (int, string) { return 200, sampleSSE("end_turn", "ok") })
	a := selfPage(t, `{"sample": {}}`, nil)
	const browser = "Mozilla/5.0 (Macintosh) PageBrowser/1.0"
	if code, lines, out := pageSample(h, a.ID, map[string]any{"t": ptok(t, a.ID), "messages": pageAsks("hi")}, "User-Agent", browser); code != 200 || len(lines) == 0 {
		t.Fatalf("a call: %d %v %v", code, lines, out)
	}
	if ua := u.uas[0]; strings.Contains(ua, "PageBrowser") || strings.Contains(ua, "Mozilla") {
		t.Errorf("the browser's User-Agent went upstream: %q", ua)
	}
	var agents []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(agents) == 0; time.Sleep(10 * time.Millisecond) {
		for _, r := range usage.Load(time.Time{}) {
			if r.Provider == "fake" {
				agents = append(agents, r.Agent)
			}
		}
	}
	if len(agents) != 1 || agents[0] != SampleAgent {
		t.Fatalf("Usage's agents: %v, want [%s]", agents, SampleAgent)
	}
}

// A call with tools is rounds: the model's tool calls come back to the
// page with the blocks it sends back, its results go on to the next
// round, the last round may call no tool, and none of it is replayed.
func TestArtifactPageSampleTools(t *testing.T) {
	h := artifacts(t)
	u := newSampleUpstream(t, func(m map[string]any) (int, string) {
		msgs, _ := m["messages"].([]any)
		if len(msgs) == 1 {
			return 200, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking."}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather","input":{}}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Oslo\"}"}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":1}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
		}
		return 200, sampleSSE("end_turn", "It is 4°C.")
	})
	a := selfPage(t, `{"sample": {}}`, nil)
	tok := ptok(t, a.ID)
	tools := []map[string]any{{"name": "get_weather", "description": "The weather in a city", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}
	call := map[string]any{"t": tok, "messages": pageAsks("Weather in Oslo?"), "tools": tools}
	code, lines, out := pageSample(h, a.ID, call)
	text, end, _ := sampleText(lines)
	if code != 200 || text != "Looking." || end["stop"] != "tool_use" {
		t.Fatalf("round 1: %d %v %v", code, lines, out)
	}
	content, _ := end["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("the blocks: %v", end)
	}
	use := content[1].(map[string]any)
	if in, _ := use["input"].(map[string]any); use["type"] != "tool_use" || use["id"] != "tu_1" || in["city"] != "Oslo" {
		t.Fatalf("the tool call: %v", use)
	}
	asked := u.bodies()
	if tl, _ := asked[0]["tools"].([]any); len(tl) != 1 || tl[0].(map[string]any)["input_schema"] == nil || asked[0]["tool_choice"] != nil {
		t.Fatalf("round 1's tools: %v", asked[0])
	}

	// the next round, its last: the results go up, and no tool is called
	call["messages"] = []any{
		map[string]any{"role": "user", "content": "Weather in Oslo?"},
		map[string]any{"role": "assistant", "content": content},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": `{"c":4}`}}},
	}
	call["last"] = true
	code, lines, out = pageSample(h, a.ID, call)
	if text, end, _ := sampleText(lines); code != 200 || text != "It is 4°C." || end["stop"] != "end_turn" {
		t.Fatalf("round 2: %d %v %v", code, lines, out)
	}
	asked = u.bodies()
	last := asked[len(asked)-1]
	if tc, _ := last["tool_choice"].(map[string]any); tc["type"] != "none" {
		t.Errorf("the last round's tool_choice: %v", last["tool_choice"])
	}
	if msgs, _ := last["messages"].([]any); len(msgs) != 3 {
		t.Errorf("round 2's turns: %v", last["messages"])
	}
	// a call with tools isn't replayed
	pageSample(h, a.ID, call)
	if n := len(u.bodies()); n != 3 {
		t.Errorf("a call with tools was replayed: %d asked", n)
	}
}

// What a page is refused, by the code the contract gives.
func TestArtifactPageSampleRefused(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"sample": {}}`, nil)
	tok := ptok(t, a.ID)
	// no model yet
	if code, _, out := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("hi")}); code == 200 || errCode(out) != "sampling_disabled" {
		t.Errorf("no model: %d %v", code, out)
	}
	status, reply := 200, ""
	var mu sync.Mutex
	newSampleUpstream(t, func(map[string]any) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if status != 200 {
			return status, reply
		}
		return 200, sampleSSE("end_turn", "ok")
	})
	for _, c := range []struct {
		name string
		call map[string]any
		code string
	}{
		{"no turns", map[string]any{"t": tok}, "invalid_request"},
		{"ends on assistant", map[string]any{"t": tok, "messages": []any{map[string]any{"role": "user", "content": "a"}, map[string]any{"role": "assistant", "content": "b"}}}, "invalid_request"},
		{"a system turn", map[string]any{"t": tok, "messages": []any{map[string]any{"role": "system", "content": "a"}}}, "invalid_request"},
		{"an empty turn", map[string]any{"t": tok, "messages": pageAsks("  ")}, "invalid_request"},
		{"an image block from the page", map[string]any{"t": tok, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{}}}}}}, "invalid_request"},
		{"too much text", map[string]any{"t": tok, "messages": pageAsks(strings.Repeat("x", artifactSampleText+1))}, "prompt_too_large"},
		{"a bad tier", map[string]any{"t": tok, "messages": pageAsks("hi"), "modelTier": "huge"}, "invalid_request"},
		{"a bad tool name", map[string]any{"t": tok, "messages": pageAsks("hi"), "tools": []any{map[string]any{"name": "a b", "description": "x"}}}, "invalid_request"},
		{"a tool twice", map[string]any{"t": tok, "messages": pageAsks("hi"), "tools": []any{map[string]any{"name": "a", "description": "x"}, map[string]any{"name": "a", "description": "y"}}}, "invalid_request"},
		{"a tool with no description", map[string]any{"t": tok, "messages": pageAsks("hi"), "tools": []any{map[string]any{"name": "a"}}}, "invalid_request"},
		{"a schema not an object", map[string]any{"t": tok, "messages": pageAsks("hi"), "tools": []any{map[string]any{"name": "a", "description": "x", "inputSchema": map[string]any{"type": "string"}}}}, "invalid_request"},
		{"images undeclared", map[string]any{"t": tok, "messages": pageAsks("hi"), "images": []any{map[string]any{"type": "image/png", "data": "iVBORw0KGgo="}}}, "images_unavailable"},
	} {
		if code, _, out := pageSample(h, a.ID, c.call); code == 200 || errCode(out) != c.code {
			t.Errorf("%s: %d %v, want %s", c.name, code, out, c.code)
		}
	}
	// a wrong token, a page that doesn't declare sample, another computer,
	// a rebound name
	if code, _, out := pageSample(h, a.ID, map[string]any{"t": "nope", "messages": pageAsks("hi")}); code != 403 || errCode(out) != "not_granted" {
		t.Errorf("a wrong token: %d %v", code, out)
	}
	b := selfPage(t, `{"db": {}}`, nil)
	if code, _, out := pageSample(h, b.ID, map[string]any{"t": ptok(t, b.ID), "messages": pageAsks("hi")}); code != 403 || errCode(out) != "not_granted" {
		t.Errorf("undeclared: %d %v", code, out)
	}
	if code, lines, _ := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("hi")}, "Host", "evil.example:3425"); code == 200 || lines != nil {
		t.Errorf("a rebound name asked: %d", code)
	}
	if code, lines, _ := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("hi")}, "RemoteAddr", "192.168.1.20:50001", "Host", "192.168.1.10:3425"); code == 200 || lines != nil {
		t.Errorf("another computer asked: %d", code)
	}

	// a page asks at most artifactSampleMost times a minute
	p := selfPage(t, `{"sample": {}}`, nil)
	ptk := ptok(t, p.ID)
	for i := 1; i <= artifactSampleMost+1; i++ {
		code, _, out := pageSample(h, p.ID, map[string]any{"t": ptk, "messages": pageAsks("again"), "cache": map[string]any{"off": true}})
		if i <= artifactSampleMost && code != 200 || i > artifactSampleMost && (code != 429 || errCode(out) != "rate_limited") {
			t.Fatalf("call %d: %d %v", i, code, out)
		}
	}

	// the vendor's refusals, by their codes
	for _, c := range []struct {
		status        int
		body, code, r string
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 300000 tokens > 200000 maximum"}}`, "prompt_too_large", ""},
		{500, `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`, "upstream_error", ""},
		// last: routing may rest the key it was
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's usage limit"}}`, "rate_limited", "usage_limit"},
	} {
		mu.Lock()
		status, reply = c.status, c.body
		mu.Unlock()
		code, lines, out := pageSample(h, a.ID, map[string]any{"t": tok, "messages": pageAsks("vendor " + c.code), "cache": map[string]any{"off": true}})
		e, _ := out["error"].(map[string]any)
		if lines != nil {
			_, _, e = sampleText(lines)
		}
		if code == 200 && lines == nil || e == nil || e["code"] != c.code || c.r != "" && e["reason"] != c.r {
			t.Errorf("a %d: %d %v %v, want %s", c.status, code, out, lines, c.code)
		}
	}
}

// TestArtifactPageSampleNextModel: a call whose model can't answer goes
// to the next one before a word is written: a dead key (401) passes every
// model of its provider by, a vendor down (500) only its model; what the
// request got wrong is never sent again, and when no model answers the
// page is told the first one's reason.
func TestArtifactPageSampleNextModel(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"sample": {}}`, nil)
	tok := ptok(t, a.ID)
	var mu sync.Mutex
	asked := map[string][]string{}
	fail := map[string]int{}
	fake := func(id string, models ...string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			model, _ := m["model"].(string)
			mu.Lock()
			asked[id] = append(asked[id], model)
			code := fail[id]
			if c, ok := fail[id+"/"+model]; ok {
				code = c
			}
			mu.Unlock()
			if code != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"type":"error","error":{"type":"x","message":"%s says %d"}}`, id, code)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sampleSSE("end_turn", "from "+id))
		}))
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "sk-" + id, Anthropic: srv.URL, Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	// Claude's models come first, so dead's are tried before good's
	fake("dead", "claude-sonnet-4-5", "claude-opus-4-5")
	fake("good", "glm-5")
	set := func(f map[string]int) {
		mu.Lock()
		defer mu.Unlock()
		fail, asked = f, map[string][]string{}
	}
	ask := func(what string, msgs []map[string]any) (int, string, map[string]any) {
		code, lines, out := pageSample(h, a.ID, map[string]any{"t": tok, "messages": msgs, "cache": map[string]any{"off": true}})
		text, _, e := sampleText(lines)
		if e == nil {
			e, _ = out["error"].(map[string]any)
		}
		return code, text, e
	}
	set(map[string]int{"dead": 401})
	if code, text, e := ask("a dead key", pageAsks("hi")); code != 200 || text != "from good" || e != nil {
		t.Errorf("a dead key: %d %q %v", code, text, e)
	}
	if got := asked["dead"]; len(got) != 1 {
		t.Errorf("a dead key's provider asked %v, want once", got)
	}
	// a model down: the next best is the tier's own (glm-5), and with that
	// down too the provider's other model, a tier up
	set(map[string]int{"dead/claude-sonnet-4-5": 500})
	if code, text, _ := ask("a model down", pageAsks("hi")); code != 200 || text != "from good" {
		t.Errorf("a model down: %d %q, want the next model", code, text)
	}
	set(map[string]int{"dead/claude-sonnet-4-5": 500, "good": 500})
	if code, text, _ := ask("two down", pageAsks("hi")); code != 200 || text != "from dead" {
		t.Errorf("two down: %d %q, want the provider's other model", code, text)
	}
	if got := asked["dead"]; len(got) == 0 || got[len(got)-1] != "claude-opus-4-5" || slices.Contains(got[:len(got)-1], "claude-opus-4-5") {
		t.Errorf("two down: asked %v", got)
	}
	set(map[string]int{"dead": 401, "good": 403})
	if code, _, e := ask("none answers", pageAsks("hi")); code != 401 || e == nil || !strings.Contains(fmt.Sprint(e["message"]), "dead says 401") {
		t.Errorf("none answers: %d %v, want the first model's reason", code, e)
	}
	set(map[string]int{"dead": 400})
	if code, _, _ := ask("a bad request", pageAsks("hi")); code != 400 || len(asked["good"]) != 0 {
		t.Errorf("a bad request: %d, and good asked %v: never sent again", code, asked["good"])
	}
}

// TestArtifactPageSampleImages: a page that declares {sample: {images:
// true}} sends an image, which reaches a model that takes images ahead of
// the page's words; one that doesn't declare it is refused the same
// image, and so is every page while no model takes images, and nothing
// is asked either time.
func TestArtifactPageSampleImages(t *testing.T) {
	h := artifacts(t)
	u := newSampleUpstream(t, func(map[string]any) (int, string) { return 200, sampleSSE("end_turn", "A dot.") })
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	ims := []any{map[string]any{"type": "image/png", "data": png}}
	a := selfPage(t, `{"sample": {"images": true}}`, nil)
	b := selfPage(t, `{"sample": {}}`, nil)
	ask := func(id string) (int, []map[string]any, map[string]any) {
		return pageSample(h, id, map[string]any{"t": ptok(t, id), "messages": pageAsks("What is this?"), "images": ims, "cache": map[string]any{"off": true}})
	}
	if code, _, out := ask(a.ID); code != 503 || errCode(out) != "images_unavailable" {
		t.Errorf("no model takes images: %d %v", code, out)
	}
	yes := true
	if err := provider.SetModelImage("fake/claude-sonnet-4-5", &yes); err != nil {
		t.Fatal(err)
	}
	code, lines, out := ask(a.ID)
	if text, _, _ := sampleText(lines); code != 200 || text != "A dot." {
		t.Fatalf("declared: %d %v %v", code, lines, out)
	}
	asked := u.bodies()
	if len(asked) != 1 {
		t.Fatalf("asked %d times", len(asked))
	}
	if m := asked[0]["model"]; m != "claude-sonnet-4-5" {
		t.Errorf("the model that takes images wasn't asked: %v", m)
	}
	msgs, _ := asked[0]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("the last turn: %v", last)
	}
	im, _ := blocks[0].(map[string]any)
	src, _ := im["source"].(map[string]any)
	if im["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != png {
		t.Errorf("the image block: %v", im)
	}
	if tx, _ := blocks[1].(map[string]any); tx["type"] != "text" || tx["text"] != "What is this?" {
		t.Errorf("the words after it: %v", tx)
	}
	if code, lines, out := ask(b.ID); code != 403 || errCode(out) != "images_unavailable" || lines != nil {
		t.Errorf("undeclared: %d %v", code, out)
	}
	if n := len(u.bodies()); n != 1 {
		t.Errorf("a refused image was asked about: %d asks", n)
	}
}

// The request a round is: turns of one role read as one, images with the
// last turn the page wrote, and only the blocks a round may hold.
func TestSampleRequest(t *testing.T) {
	raw := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	c := &sampleCall{Messages: []sampleTurn{{Role: "user", Content: raw("a")}, {Role: "user", Content: raw("b")}}, Images: []sampleImage{{Type: "image/png", Data: "iVBORw0KGgo="}}}
	req, e := sampleRequest(c, "m")
	if e != nil {
		t.Fatal(e)
	}
	msgs := req["messages"].([]map[string]any)
	if len(msgs) != 1 {
		t.Fatalf("two user turns not one: %v", msgs)
	}
	blocks := msgs[0]["content"].([]map[string]any)
	if len(blocks) != 3 || blocks[0]["type"] != "image" || blocks[1]["text"] != "a" || blocks[2]["text"] != "b" {
		t.Fatalf("the blocks: %v", blocks)
	}
	if sampleImagesOK([]sampleImage{{Type: "image/svg+xml", Data: "PHN2Zy8+"}}) == nil {
		t.Error("an svg was taken")
	}
	if sampleImagesOK([]sampleImage{{Type: "image/png", Data: "!!"}}) == nil {
		t.Error("bad base64 was taken")
	}
	bad := &sampleCall{Messages: []sampleTurn{{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"x","content":[{"type":"image"}]}]`)}}}
	if _, e := sampleRequest(bad, "m"); e == nil {
		t.Error("a tool result holding an image was taken")
	}
	for in, ok := range map[string]bool{`{"a":1}`: true, "Here:\n```json\n[1,2]\n```": true, `Sure! {"a": [1]} done`: true, "no json": false, "```\n{bad\n```": false} {
		if sampleJSONOK(in) != ok {
			t.Errorf("sampleJSONOK(%q) != %v", in, ok)
		}
	}
	for name, tier := range map[string]string{"claude-haiku-4-5": "quick", "claude-opus-4-5": "complex", "claude-sonnet-4-5": "default", "gemini-2.5-pro": "complex", "gpt-5-mini": "quick", "gemini-3-pro-preview": "complex", "o4-mini-high": "quick", "gemini-flash-lite": "quick", "deepseek-v4": "default", "promptly-1": "default"} {
		if got := sampleTierOf(name); got != tier {
			t.Errorf("sampleTierOf(%s) = %s, want %s", name, got, tier)
		}
	}
}
