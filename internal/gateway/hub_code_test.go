package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"runtime/metrics"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codeHub is a hub whose agent "opencode" has github's tools, the
// results call gives.
func codeHub(t *testing.T, call func(ctx context.Context, server, tool string) (json.RawMessage, error)) (*fakeHub, func(code string) mcpReply) {
	t.Helper()
	fresh(t)
	spec := func(n string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"name": json.RawMessage(`"` + n + `"`), "description": json.RawMessage(`"` + n + `"`),
			"inputSchema": json.RawMessage(`{"type":"object"}`)}
	}
	f := &fakeHub{on: true, call: call, tools: map[string][]HubTool{"opencode": {
		{Server: "github", Tool: "search_issues", Spec: spec("search_issues")},
		{Server: "github", Tool: "get_issue", Spec: spec("get_issue")},
		{Server: "web-fetch", Tool: "fetch", Spec: spec("fetch")},
	}}, servers: map[string][]HubServer{"opencode": {
		{Name: "github", Known: true, Tools: []HubToolNote{{Name: "search_issues"}, {Name: "get_issue"}}},
		{Name: "web-fetch", Known: true, Tools: []HubToolNote{{Name: "fetch"}}},
	}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	return f, func(code string) mcpReply {
		args, _ := json.Marshal(map[string]string{"code": code})
		return readMCP(t, hubPost(h, "opencode", here, toolsCall(hubCodeTool, string(args))))
	}
}

func codeText(m mcpReply) string {
	var b strings.Builder
	for _, c := range m.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// run_code is listed beside the tools, said in the instructions, and runs
// a script that calls several tools, in a loop and at once, and gives
// back only what it returns and logs: a tool's JSON text as an object, a
// tool's structuredContent as itself, its arguments as the script gave
// them. Each call is a request-log row of its own.
func TestHubCodeRunsTools(t *testing.T) {
	var at, most atomic.Int32
	f, run := codeHub(t, func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		n := at.Add(1)
		defer at.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		switch tool {
		case "search_issues":
			return json.RawMessage(`{"content":[{"type":"text","text":"[{\"number\":1,\"title\":\"a\",\"body\":\"long\"},{\"number\":2,\"title\":\"b\",\"body\":\"long\"}]"}]}`), nil
		case "get_issue":
			return json.RawMessage(`{"content":[{"type":"text","text":"ignored"}],"structuredContent":{"state":"open","comments":3}}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"plain words"}]}`), nil
	})
	h := lanGuard(New().Handler())
	if got := listedNames(t, readMCP(t, hubPost(h, "opencode", here, toolsList))); !slices.Contains(got, hubCodeTool) || len(got) != 4 {
		t.Fatalf("listed %v", got)
	}
	if say := initInstructions(t, hubPost(h, "opencode", here, hubInit).Body.String()); !strings.Contains(say, hubCodeTool) {
		t.Fatalf("instructions: %s", say)
	}

	got := run(`
const found = await tools.github__search_issues({q: "bug", n: 2});
console.log("found", found.length);
const states = await Promise.all(found.map(i => tools["github__get_issue"]({number: i.number})));
const page = await tools.web_fetch__fetch({url: "https://x"});
return {issues: found.map((i, k) => ({n: i.number, state: states[k].state})), page};`)
	if got.Result.IsError {
		t.Fatalf("run: %s", codeText(got))
	}
	text := codeText(got)
	var out struct {
		Issues []struct {
			N     int
			State string
		}
		Page string
	}
	i := strings.Index(text, "{")
	if !strings.HasPrefix(text, "found 2\n") || i < 0 || json.Unmarshal([]byte(text[i:]), &out) != nil ||
		len(out.Issues) != 2 || out.Issues[1].N != 2 || out.Issues[1].State != "open" || out.Page != "plain words" || strings.Contains(text, "long") {
		t.Fatalf("gave %q", text)
	}
	if most.Load() < 2 {
		t.Fatalf("Promise.all ran its calls one at a time (%d at most)", most.Load())
	}
	ran := f.ran()
	if len(ran) != 4 || ran[0] != `opencode github search_issues {"q":"bug","n":2}` || !slices.Contains(ran, `opencode web-fetch fetch {"url":"https://x"}`) {
		t.Fatalf("ran %q", ran)
	}
	rows := mcpRows(t)
	if n := len(slices.DeleteFunc(slices.Clone(rows), func(r usage.Record) bool { return r.Requested != "github__get_issue" })); n != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if !slices.ContainsFunc(rows, func(r usage.Record) bool { return r.Requested == hubCodeTool }) {
		t.Fatalf("no row of run_code's own: %+v", rows)
	}
}

// A tool's error is thrown, and the script can catch it; one it doesn't
// catch, or its own throw, a syntax error and a name no tool has are an
// error result that says what happened, with what was logged before.
func TestHubCodeErrors(t *testing.T) {
	_, run := codeHub(t, func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		if tool == "get_issue" {
			return json.RawMessage(`{"content":[{"type":"text","text":"Not Found"}],"isError":true}`), nil
		}
		if tool == "fetch" {
			return nil, errors.New("server went away")
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	})
	if got := run(`try { await tools.github__get_issue({number: 9}) } catch (e) { return "caught: " + e.message }`); got.Result.IsError || codeText(got) != "caught: Not Found" {
		t.Fatalf("caught: %+v", got)
	}
	for code, want := range map[string]string{
		`console.log("before"); await tools.github__get_issue({});`:     "before\nThe code threw: ",
		`await tools.web_fetch__fetch({})`:                              "server went away",
		`throw new Error("mine")`:                                       "mine",
		`return (`:                                                      "doesn't parse",
		`return await tools.gh__search_issues({})`:                      "github__search_issues",
		`return await tools.nothing__here({})`:                          "no tool is called nothing__here",
		`tools.github__search_issues({}); return new Promise(() => {})`: "never settled",
	} {
		got := run(code)
		if !got.Result.IsError || !strings.Contains(codeText(got), want) {
			t.Fatalf("%s gave %+v, want %q", code, got, want)
		}
	}
	if got := run(`return await tools.github__get_issue({})`); !strings.Contains(codeText(got), "Not Found") {
		t.Fatalf("the tool's own words: %s", codeText(got))
	}
	if got := run(``); !got.Result.IsError {
		t.Fatalf("no code: %+v", got)
	}
}

// A script that never ends is stopped at its time, a tool call still
// running is cancelled with it, and one that calls more than hubCodeCalls
// tools is refused past them; what it gives back is cut.
func TestHubCodeLimits(t *testing.T) {
	was := hubCodeWait
	hubCodeWait = 300 * time.Millisecond
	t.Cleanup(func() { hubCodeWait = was })
	var cancelled atomic.Bool
	_, run := codeHub(t, func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		if tool == "fetch" {
			<-ctx.Done()
			cancelled.Store(true)
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	})
	start := time.Now()
	if got := run(`while (true) {}`); !got.Result.IsError || !strings.Contains(codeText(got), "didn't finish") {
		t.Fatalf("a loop: %+v", got)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("a loop ran %s", took)
	}
	if got := run(`return await tools.web_fetch__fetch({})`); !got.Result.IsError || !strings.Contains(codeText(got), "didn't finish") {
		t.Fatalf("a call past the time: %+v", got)
	}
	if !cancelled.Load() {
		t.Fatal("the call kept running")
	}
	hubCodeWait = 20 * time.Second
	got := run(`let n = 0, refused = "";
for (let i = 0; i < 105; i++) { try { await tools.github__search_issues({}); n++ } catch (e) { refused = e.message } }
return n + " " + refused`)
	if got.Result.IsError || !strings.HasPrefix(codeText(got), "100 ") || !strings.Contains(codeText(got), "more than 100") {
		t.Fatalf("many calls: %+v", got)
	}
	if got := run(`function f() { return f() } f()`); !got.Result.IsError {
		t.Fatalf("deep recursion: %+v", got)
	}
	big := codeText(run(`return "x".repeat(100000)`))
	if len(big) > hubCodeMax+200 || !strings.Contains(big, "cut") {
		t.Fatalf("a big result is %d long", len(big))
	}
	for _, code := range []string{`return typeof require`, `return typeof fetch`, `return typeof setTimeout`, `return typeof process`} {
		if got := codeText(run(code)); got != "undefined" {
			t.Fatalf("%s: %s", code, got)
		}
	}
}

// A script reaches only the tools its agent is given: a server switched
// off for it isn't there to call.
func TestHubCodeKeepsToTheAgentsTools(t *testing.T) {
	f, run := codeHub(t, nil)
	f.mu.Lock()
	f.tools["claude"] = []HubTool{{Server: "secret", Tool: "read", Spec: hubSpec("read")}}
	f.mu.Unlock()
	if got := run(`return await tools.secret__read({})`); !got.Result.IsError {
		t.Fatalf("another agent's tool: %+v", got)
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("ran %q", ran)
	}
}

// Turned off in settings, run_code is neither listed nor run, and the
// instructions don't name it.
func TestHubCodeOff(t *testing.T) {
	_, run := codeHub(t, nil)
	s := settings.Load()
	s.RouterNoCode = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s := settings.Load()
		s.RouterNoCode = false
		settings.Save(s)
	})
	h := lanGuard(New().Handler())
	if got := listedNames(t, readMCP(t, hubPost(h, "opencode", here, toolsList))); slices.Contains(got, hubCodeTool) {
		t.Fatalf("listed %v", got)
	}
	if say := initInstructions(t, hubPost(h, "opencode", here, hubInit).Body.String()); strings.Contains(say, hubCodeTool) {
		t.Fatalf("instructions: %s", say)
	}
	if got := run(`return 1`); got.Error == nil {
		t.Fatalf("ran: %+v", got)
	}
}

// A builtin is Go, past goja's interrupt: one that allocates gigabytes at
// once, or a regular expression that backtracks for ever, is ended with
// the script's process at its memory or its time, and takes neither
// magpie's memory nor the request with it.
func TestHubCodeBuiltinsEndWithTheirProcess(t *testing.T) {
	was := hubCodeWait
	hubCodeWait = 2 * time.Second
	t.Cleanup(func() { hubCodeWait = was })
	_, run := codeHub(t, nil)
	for _, c := range []struct{ code, want string }{
		{`return "x".repeat(3e9).length`, "memory"},
		{`return /^(a+)+$(?!x)/.test("a".repeat(40) + "b")`, "didn't finish"},
		{`const a = []; a.length = 4e9; return a.indexOf(1)`, "didn't finish"},
	} {
		code, want := c.code, c.want
		before := hubHeapNow()
		start := time.Now()
		got := make(chan mcpReply, 1)
		go func() { got <- run(code) }()
		select {
		case m := <-got:
			if !m.Result.IsError || !strings.Contains(codeText(m), want) {
				t.Fatalf("%s gave %q, want %q", code, codeText(m), want)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s still running after %s", code, time.Since(start))
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("%s took %s", code, took)
		}
		if grew := int64(hubHeapNow()) - int64(before); grew > 256<<20 {
			t.Fatalf("%s grew magpie's heap by %d MB", code, grew>>20)
		}
	}
}

func hubHeapNow() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}
