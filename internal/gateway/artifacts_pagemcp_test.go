package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pageMCPCall posts one call to the artifact's mcp route from this
// computer, as the shell does: the status, the answer and the error, if
// it is one. hdr are RemoteAddr or Host to set over the shell's.
func pageMCPCall(h http.Handler, ref string, body map[string]any, hdr ...string) (int, map[string]any, map[string]any) {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/mcp", bytes.NewReader(b))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "text/plain")
	for i := 0; i+1 < len(hdr); i += 2 {
		switch hdr[i] {
		case "RemoteAddr":
			r.RemoteAddr = hdr[i+1]
		case "Host":
			r.Host = hdr[i+1]
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	e, _ := out["error"].(map[string]any)
	return w.Code, out, e
}

func annotated(name string, ann string) map[string]json.RawMessage {
	s := hubSpec(name)
	if ann != "" {
		s["annotations"] = json.RawMessage(ann)
	}
	return s
}

// mcpPage is a page that declares a manifest of github's list_issues and
// create_issue, a host: server, and Linear, which the Library hasn't, and
// a Library with github, "my fs" and another server.
func mcpPage(t *testing.T) (http.Handler, *fakeHub, string, string) {
	t.Helper()
	h := artifacts(t)
	f := &fakeHub{on: true,
		servers: map[string][]HubServer{"": {{Name: "github"}, {Name: "my fs"}, {Name: "other"}}},
		tools: map[string][]HubTool{"": {
			{Server: "github", Tool: "list_issues", Spec: annotated("list_issues", `{"readOnlyHint":true,"title":"List"}`)},
			{Server: "github", Tool: "create_issue", Spec: annotated("create_issue", `{"readOnlyHint":false,"destructiveHint":false}`)},
			{Server: "github", Tool: "delete_repo", Spec: hubSpec("delete_repo")},
			{Server: "my fs", Tool: "read_file", Spec: hubSpec("read_file")},
			{Server: "other", Tool: "x", Spec: hubSpec("x")},
		}},
	}
	withHub(t, f)
	id := publishFor(t, h, map[string]any{"mcp": map[string]any{"servers": []any{
		map[string]any{"server": "github", "tools": []string{"list_issues", "create_issue"}},
		map[string]any{"server": "host:my_fs", "tools": []string{"read_file"}},
		map[string]any{"server": "Linear", "tools": []string{"list_issues"}},
	}}})
	return h, f, id, shellTokenOf(t, h, id)
}

// The shell lists the manifest's servers that are in the Library, each
// with the manifest's tools it lists and the hints it declared, and none
// of the Library's others.
func TestArtifactMCPLists(t *testing.T) {
	h, _, id, tok := mcpPage(t)
	code, out, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "list"})
	if code != 200 || e != nil {
		t.Fatalf("%d %v", code, e)
	}
	got, _ := json.Marshal(out["servers"])
	// the answer decoded and written again: each object's keys sorted
	want := `[{"authStatus":"connected","kind":"connector","server":"github","tools":[{"annotations":{"readOnlyHint":true},"description":"Does list_issues.","name":"list_issues"},{"annotations":{"destructiveHint":false,"readOnlyHint":false},"description":"Does create_issue.","name":"create_issue"}]},{"authStatus":"connected","kind":"connector","server":"host:my_fs","tools":[{"description":"Does read_file.","name":"read_file"}]}]`
	if string(got) != want {
		t.Fatalf("listed\n%s\nwant\n%s", got, want)
	}
	// and the manifest whole, Linear too, for the shell to ask about
	man, _ := json.Marshal(out["manifest"])
	if string(man) != `[{"server":"github","tools":["list_issues","create_issue"]},{"server":"host:my_fs","tools":["read_file"]},{"server":"Linear","tools":["list_issues"]}]` {
		t.Fatalf("manifest %s", man)
	}
	code, out, _ = pageMCPCall(h, id, map[string]any{"t": tok, "op": "describe", "server": "github", "tool": "list_issues"})
	if code != 200 || out["inputSchema"] == nil || out["name"] != "list_issues" {
		t.Fatalf("describe: %d %v", code, out)
	}
}

// A call of the manifest runs on the Library server it names, a host:
// name on the server whose name reads so, with the input as the page
// gave it; the result comes back whole with the tool's hints, a tool's
// failure as tool_error with the whole result, and a server that can't
// run it as server_unavailable, retryable.
func TestArtifactMCPCalls(t *testing.T) {
	h, f, id, tok := mcpPage(t)
	code, out, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "list_issues", "input": map[string]any{"q": "open"}})
	res, _ := json.Marshal(out["result"])
	if code != 200 || e != nil || string(res) != `{"content":[{"text":"ran github list_issues","type":"text"}]}` || out["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatalf("%d %v %s", code, out, res)
	}
	if code, _, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "host:my_fs", "tool": "read_file"}); code != 200 || e != nil {
		t.Fatalf("host: %d %v", code, e)
	}
	if ran := f.ran(); len(ran) != 2 || ran[0] != ` github list_issues {"q":"open"}` || ran[1] != ` my fs read_file {}` {
		t.Fatalf("ran %q", ran)
	}

	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		return json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"repo not found"}]}`), nil
	}
	code, _, e = pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "create_issue", "input": map[string]any{}})
	if code != 200 || e["code"] != "tool_error" || e["message"] != "repo not found" || e["server"] != "github" || e["result"] == nil {
		t.Fatalf("tool error: %d %v", code, e)
	}
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		return nil, errors.New("it isn't running")
	}
	code, _, e = pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "list_issues"})
	if code != 502 || e["code"] != "server_unavailable" || e["retryable"] != true || !strings.Contains(e["message"].(string), "it isn't running") {
		t.Fatalf("down: %d %v", code, e)
	}
}

// Nothing outside the manifest is run, nor from anyone but the shell on
// this computer, nor for a version without mcp, a watch of a tool that
// changes things, an input that isn't an object, or more calls a minute
// than a page makes; with the hub off, nothing is listed or run.
func TestArtifactMCPRefuses(t *testing.T) {
	h, f, id, tok := mcpPage(t)
	plain := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	pageTok := tokenRe.FindStringSubmatch(pageGet(h, ArtifactPagePath+id+"/", "Sec-Fetch-Dest", "iframe").Body.String())
	if pageTok == nil {
		t.Fatal("no page token")
	}
	call := func(server, tool string) map[string]any {
		return map[string]any{"t": tok, "op": "call", "server": server, "tool": tool}
	}
	with := func(m map[string]any, k string, v any) map[string]any { m[k] = v; return m }
	for _, c := range []struct {
		why  string
		ref  string
		body map[string]any
		hdr  []string
		code int
		ec   string
	}{
		{"a tool outside the manifest", id, call("github", "delete_repo"), nil, 403, "not_in_manifest"},
		{"a server outside the manifest", id, call("other", "x"), nil, 403, "not_in_manifest"},
		{"a server the Library hasn't", id, call("Linear", "list_issues"), nil, 404, "server_not_connected"},
		{"watching a write", id, with(call("github", "create_issue"), "watch", true), nil, 400, "bad_request"},
		{"an input that isn't an object", id, with(call("github", "list_issues"), "input", []int{1}), nil, 400, "bad_request"},
		{"an op that isn't one", id, map[string]any{"t": tok, "op": "drop"}, nil, 400, "bad_request"},
		{"the page's own token", id, with(call("github", "list_issues"), "t", pageTok[1]), nil, 403, "not_granted"},
		{"no token", id, with(call("github", "list_issues"), "t", ""), nil, 403, "not_granted"},
		{"another artifact's shell", plain, call("github", "list_issues"), nil, 403, "not_granted"},
		{"a version without mcp", plain, with(call("github", "list_issues"), "t", shellTokenOf(t, h, plain)), nil, 403, "not_granted"},
		{"a version that isn't there", id, with(call("github", "list_issues"), "version", 9), nil, 403, "not_granted"},
		{"another computer", id, call("github", "list_issues"), []string{"RemoteAddr", "192.168.1.9:50001"}, 403, ""},
		{"a rebound name", id, call("github", "list_issues"), []string{"Host", "evil.example:3425"}, 403, ""},
	} {
		code, _, e := pageMCPCall(h, c.ref, c.body, c.hdr...)
		if code != c.code || c.ec != "" && (e == nil || e["code"] != c.ec) {
			t.Errorf("%s: %d %v", c.why, code, e)
		}
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("ran %q", ran)
	}

	was := artifactMCPCalls
	artifactMCPCalls = newMinuteLimit(2)
	t.Cleanup(func() { artifactMCPCalls = was })
	for i := 1; i <= 3; i++ {
		code, _, e := pageMCPCall(h, id, call("github", "list_issues"))
		if i <= 2 && code != 200 || i == 3 && (code != 429 || e["code"] != "rate_limited" || e["retryable"] != true || e["retryAfterMs"] == nil) {
			t.Fatalf("call %d: %d %v", i, code, e)
		}
	}
	if ran := f.ran(); len(ran) != 2 {
		t.Fatalf("ran %d calls, want 2", len(ran))
	}

	f.mu.Lock()
	f.on = false
	f.mu.Unlock()
	code, out, _ := pageMCPCall(h, id, map[string]any{"t": tok, "op": "list"})
	if code != 200 || len(out["servers"].([]any)) != 0 || !strings.Contains(out["off"].(string), "Library") {
		t.Fatalf("off, listed: %d %v", code, out)
	}
	if code, _, e := pageMCPCall(h, id, call("github", "list_issues")); code != 404 || e["code"] != "server_not_connected" {
		t.Fatalf("off, called: %d %v", code, e)
	}
	if ran := f.ran(); len(ran) != 2 {
		t.Fatalf("off, ran %d calls", len(ran))
	}
}

// A server that doesn't answer in time is server_unavailable, retryable,
// and says so.
func TestArtifactMCPTimesOut(t *testing.T) {
	h, f, id, tok := mcpPage(t)
	was := hubCallWait
	hubCallWait = 50 * time.Millisecond
	t.Cleanup(func() { hubCallWait = was })
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	code, _, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "list_issues"})
	if code != 502 || e["code"] != "server_unavailable" || e["retryable"] != true || !strings.Contains(e["message"].(string), "didn't answer") {
		t.Fatalf("%d %v", code, e)
	}
}

// A page's call of a server with a call timeout of its own in the Library
// waits that long, as an agent's does, and running out of it names it.
func TestArtifactMCPOwnWait(t *testing.T) {
	h, f, id, tok := mcpPage(t)
	f.mu.Lock()
	f.servers[""][0].Wait = time.Hour
	f.mu.Unlock()
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) < 50*time.Minute {
			return nil, errors.New("not given its hour")
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	}
	if code, _, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "list_issues"}); code != 200 {
		t.Fatalf("a server given an hour: %d %v", code, e)
	}
	f.mu.Lock()
	f.servers[""][0].Wait = 100 * time.Millisecond
	f.mu.Unlock()
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	code, _, e := pageMCPCall(h, id, map[string]any{"t": tok, "op": "call", "server": "github", "tool": "list_issues"})
	if code != 502 || !strings.Contains(e["message"].(string), "didn't answer in 100ms, the call timeout it has in magpie's Library") {
		t.Fatalf("past its own timeout: %d %v", code, e)
	}
}
