package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/sessions"
)

// commentCall is a comments call, as the shell or a page's runtime makes
// it: a simple POST.
func commentCall(h http.Handler, ref string, call map[string]any, hdr ...string) (int, map[string]any) {
	b, _ := json.Marshal(call)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/comments", strings.NewReader(string(b)))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
			continue
		}
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

var shellTokenRe = regexp.MustCompile(`"token":"([^"]*)"`)

// noAgents fakes every agent's command away, so no test starts one.
func noAgents(t *testing.T) {
	t.Helper()
	look, ses, run := sendLook, sendSessions, sendRun
	sendLook = func(string) string { return "" }
	sendSessions = func() []sessions.Session { return nil }
	sendRun = func(context.Context, string, string, string, ...string) ([]byte, []byte, error) {
		t.Error("an agent was started")
		return nil, nil, nil
	}
	t.Cleanup(func() { sendLook, sendSessions, sendRun = look, ses, run })
}

func publishFor(t *testing.T, h http.Handler, caps map[string]any) string {
	t.Helper()
	args := map[string]any{"html": "<!doctype html><html><head><title>Board</title></head><body><h1 id=top>Plan</h1><p>x"}
	if caps != nil {
		args["capabilities"] = caps
	}
	said, isErr := artifactsCall(t, h, artifactPublishTool, args)
	if isErr {
		t.Fatalf("publish: %s", said)
	}
	return publishedID(t, said)
}

// A tab opening a page gets the viewer shell, at magpie's own origin:
// its own nonce'd script, its own token, nothing framing it and no other
// origin reading it. The frame it opens, a script's fetch and a client
// with no Sec-Fetch headers get the page as before, its runtime told
// magpie's origin; and the two answers vary on the header.
func TestArtifactShell(t *testing.T) {
	noAgents(t)
	h := artifacts(t)
	id := publishFor(t, h, nil)
	page := ArtifactPagePath + id + "/"

	w := pageGet(h, page+"?tab=2", "Sec-Fetch-Dest", "document")
	body, hd := w.Body.String(), w.Header()
	csp := hd.Get("Content-Security-Policy")
	nonce := regexp.MustCompile(`script-src 'nonce-([^']+)'`).FindStringSubmatch(csp)
	if w.Code != 200 || nonce == nil || !strings.Contains(body, `<script nonce="`+nonce[1]+`">`) || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "sandbox") {
		t.Fatalf("the shell: %d %v %.300s", w.Code, hd, body)
	}
	if hd.Get("Access-Control-Allow-Origin") != "" || hd.Get("Cache-Control") != "no-store" || !strings.Contains(strings.Join(hd.Values("Vary"), ","), "Sec-Fetch-Dest") {
		t.Fatalf("the shell's headers: %v", hd)
	}
	if !strings.Contains(body, `<iframe id="page"`) || strings.Contains(body, "<h1") || !strings.Contains(body, `"src":"`+page+`?tab=2"`) || !strings.Contains(body, `"origin":"http://127.0.0.1:3425"`) {
		t.Fatalf("the shell frames the page: %.2000s", body)
	}
	m := shellTokenRe.FindStringSubmatch(body)
	if m == nil || !artifact.ShellTokenOK(id, m[1]) || artifact.PageTokenOK(id, m[1]) {
		t.Fatalf("the shell's token: %v", m)
	}

	for _, dest := range []string{"iframe", ""} {
		w := pageGet(h, page, "Sec-Fetch-Dest", dest)
		b := w.Body.String()
		if w.Code != 200 || !strings.Contains(b, "<h1 id=top>") || !strings.Contains(b, `"origin":"http://127.0.0.1:3425"`) || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox ") || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Sec-Fetch-Dest") {
			t.Fatalf("Sec-Fetch-Dest %q: %d %v %.300s", dest, w.Code, w.Header(), b)
		}
		if tok := tokenRe.FindStringSubmatch(b); tok == nil || artifact.ShellTokenOK(id, tok[1]) {
			t.Fatalf("Sec-Fetch-Dest %q holds the shell's token", dest)
		}
	}
	// a pinned version's shell says so; another file is never a shell
	if b := pageGet(h, ArtifactPagePath+id+"@1/", "Sec-Fetch-Dest", "document").Body.String(); !strings.Contains(b, `"pinned":true`) || !strings.Contains(b, `"version":1`) {
		t.Fatalf("pinned: %.400s", b)
	}
	if w := pageGet(h, page+"other.html", "Sec-Fetch-Dest", "document"); strings.Contains(w.Body.String(), `<iframe id="page"`) {
		t.Fatalf("a file got the shell")
	}
	// another computer, or a name rebound to this one, gets nothing
	r := httptest.NewRequest(http.MethodGet, page, nil)
	r.RemoteAddr, r.Host = "192.168.1.9:50001", "127.0.0.1:3425"
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatalf("another computer got the shell")
	}
	r = httptest.NewRequest(http.MethodGet, page, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "evil.example:3425"
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatalf("a rebound name got the shell")
	}
}

// The shell's token does everything; a page's own writes only, and only
// from a version declaring comments in full; it never lists, never sends
// to the agent itself, and a composer_only page writes nothing.
func TestArtifactCommentsAPI(t *testing.T) {
	noAgents(t)
	h := artifacts(t)
	id := publishFor(t, h, map[string]any{"comments": map[string]any{}})
	shell, _ := artifact.ShellToken(id)
	page := pageToken(t, h, ArtifactPagePath+id+"/")

	code, out := commentCall(h, id, map[string]any{"t": shell, "op": "create", "anchor": map[string]any{"path": "#top", "x": 0.2, "y": 0.5}, "label": "Plan", "text": "Bigger, please"})
	tid, _ := out["threadId"].(string)
	if code != 200 || tid == "" {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, out := commentCall(h, id, map[string]any{"t": shell, "op": "reply", "thread": tid, "text": "and bold"}); code != 200 || out["commentId"] == nil {
		t.Fatalf("reply: %d %v", code, out)
	}
	// the page asks whether the agent can take one; it writes nothing
	// itself: its writes go through the shell, which asks the viewer
	if code, out := commentCall(h, id, map[string]any{"t": page, "op": "can"}, "Origin", "null"); code != 200 || out["can"] != "no_session" {
		t.Fatalf("a page's can: %d %v", code, out)
	}
	code, out = commentCall(h, id, map[string]any{"t": shell, "op": "list"})
	ts, _ := out["threads"].([]any)
	if code != 200 || len(ts) != 1 || len(ts[0].(map[string]any)["comments"].([]any)) != 2 || out["agent"].(map[string]any)["can"] != "no_session" {
		t.Fatalf("list: %d %v", code, out)
	}
	for _, c := range []struct {
		why  string
		call map[string]any
		code string
	}{
		{"a page lists", map[string]any{"t": page, "op": "list"}, "not_granted"},
		{"a page stops a run", map[string]any{"t": page, "op": "stop", "thread": tid}, "not_granted"},
		{"a page sends", map[string]any{"t": page, "op": "send", "thread": tid}, "not_granted"},
		{"a page writes and sends", map[string]any{"t": page, "op": "reply", "thread": tid, "text": "go", "send": true}, "consent_required"},
		{"a page creates", map[string]any{"t": page, "op": "create", "anchor": map[string]any{"path": "#top", "x": 0.2, "y": 0.5}, "text": "hi"}, "consent_required"},
		{"a page replies", map[string]any{"t": page, "op": "reply", "thread": tid, "text": "and bold"}, "consent_required"},
		{"a page resolves", map[string]any{"t": page, "op": "resolve", "thread": tid, "resolved": true}, "consent_required"},
		{"a page deletes", map[string]any{"t": page, "op": "delete", "thread": tid}, "consent_required"},
		{"no token", map[string]any{"op": "list"}, "not_granted"},
		{"another's token", map[string]any{"t": "x" + shell[1:], "op": "list"}, "not_granted"},
		{"an empty comment", map[string]any{"t": shell, "op": "reply", "thread": tid, "text": "  "}, "invalid"},
		{"no such thread", map[string]any{"t": shell, "op": "reply", "thread": "t_nope", "text": "hi"}, "not_found"},
		{"send with no agent", map[string]any{"t": shell, "op": "create", "text": "hi", "send": true}, "claude_unavailable"},
	} {
		if _, out := commentCall(h, id, c.call, "Origin", "null"); errCode(out) != c.code {
			t.Errorf("%s: %v, want %s", c.why, out, c.code)
		}
	}
	if _, out := commentCall(h, id, map[string]any{"t": shell, "op": "resolve", "thread": tid, "resolved": true}); errCode(out) != "" {
		t.Fatalf("resolve: %v", out)
	}
	if th, _ := artifact.GetThread(id, tid); !th.Resolved {
		t.Fatalf("not resolved: %+v", th)
	}
	if ts, _ := artifact.Threads(id); len(ts) != 1 || len(ts[0].Comments) != 2 {
		t.Fatalf("a page's refused writes wrote: %+v", ts)
	}
	if _, out := commentCall(h, id, map[string]any{"t": shell, "op": "delete", "thread": tid}); errCode(out) != "" {
		t.Fatalf("delete: %v", out)
	}
	if ts, _ := artifact.Threads(id); len(ts) != 0 {
		t.Fatalf("left: %+v", ts)
	}
	// from elsewhere, nothing
	if code, _ := commentCall(h, id, map[string]any{"t": shell, "op": "list"}, "Host", "evil.example:3425"); code != 403 {
		t.Fatalf("a rebound name: %d", code)
	}

	// a page without comments, and a composer_only one, write nothing
	for _, caps := range []map[string]any{nil, {"comments": map[string]any{"composer_only": true}}} {
		other := publishFor(t, h, caps)
		tok := pageToken(t, h, ArtifactPagePath+other+"/")
		if code, out := commentCall(h, other, map[string]any{"t": tok, "op": "create", "text": "hi"}, "Origin", "null"); code != 403 || errCode(out) != "not_granted" {
			t.Fatalf("%v: %d %v", caps, code, out)
		}
		st, _ := artifact.ShellToken(other)
		if code, out := commentCall(h, other, map[string]any{"t": st, "op": "create", "text": "hi"}); code != 200 {
			t.Fatalf("%v, the shell: %d %v", caps, code, out)
		}
	}
}

// A comment sent to the agent resumes the session that published the
// page, in its folder, with a prompt that names no product; its answer
// is posted in the thread, and the run says it is done. Stop ends a run
// and leaves no trace of it; the shell's stream says the threads changed.
func TestArtifactCommentSend(t *testing.T) {
	h := artifacts(t)
	dir := t.TempDir()
	// the publishing session: Claude Code, in dir
	said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"html": "<!doctype html><title>Board</title><p>x"}, "User-Agent", "claude-cli/2.1.0 (external, cli)", MemoryCwdHeader, dir)
	if isErr {
		t.Fatalf("publish: %s", said)
	}
	id := publishedID(t, said)
	a, _ := artifact.Get(id)
	if v := publisher(a); v == nil || sendAgentOf(v.Agent) != "claude" || v.Cwd != dir {
		t.Fatalf("the publisher: %+v", a.Latest())
	}
	transcript := filepath.Join(t.TempDir(), "s1.jsonl")
	os.WriteFile(transcript, []byte(`{"tool":"publish","id":"`+id+`"}`+"\n"), 0o600)

	look, ses, run := sendLook, sendSessions, sendRun
	t.Cleanup(func() { sendLook, sendSessions, sendRun = look, ses, run })
	sendLook = func(ag string) string {
		if ag == "claude" {
			return "/fake/claude"
		}
		return ""
	}
	now := time.Now()
	sendSessions = func() []sessions.Session {
		return []sessions.Session{
			{Agent: "claude", ID: "other", Cwd: dir, Start: now.Add(-time.Hour), Last: now.Add(time.Hour), Path: filepath.Join(t.TempDir(), "none.jsonl")},
			{Agent: "claude", ID: "s1", Cwd: dir, Start: now.Add(-time.Hour), Last: now, Path: transcript},
		}
	}
	var mu sync.Mutex
	var got []string
	var gotDir, gotPrompt string
	hold := make(chan struct{})
	sendRun = func(ctx context.Context, d, stdin, name string, args ...string) ([]byte, []byte, error) {
		mu.Lock()
		got, gotDir, gotPrompt = append([]string{name}, args...), d, stdin
		mu.Unlock()
		if strings.Contains(stdin, "hold on") {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		return []byte(`{"type":"result","result":"Made the title bigger.","is_error":false}`), nil, nil
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	shell, _ := artifact.ShellToken(id)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+ArtifactPagePath+id+"/_api/shell?t="+shell, nil)
	req.Host = "127.0.0.1:3425"
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("the shell's stream: %v %v", err, resp)
	}
	defer resp.Body.Close()
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") {
				events <- l
			}
		}
	}()

	if _, out := commentCall(h, id, map[string]any{"t": shell, "op": "can"}); out["can"] != "available" {
		t.Fatalf("can: %v", out)
	}
	code, out := commentCall(h, id, map[string]any{"t": shell, "op": "create", "anchor": map[string]any{"path": "body>p:nth-of-type(1)", "x": 0.5, "y": 0.5}, "label": "x", "text": "Make the title bigger", "send": true})
	tid, _ := out["threadId"].(string)
	if code != 200 || tid == "" {
		t.Fatalf("create and send: %d %v", code, out)
	}
	var th artifact.Thread
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if th, _ = artifact.GetThread(id, tid); th.Run != nil && th.Run.State == "done" {
			break
		}
	}
	if th.Run == nil || th.Run.State != "done" || len(th.Comments) != 2 || th.Comments[1].Agent != "claude" || th.Comments[1].Text != "Made the title bigger." || !th.Comments[0].Sent {
		t.Fatalf("the thread after: %+v %+v", th, th.Run)
	}
	mu.Lock()
	args, d, prompt := strings.Join(got, " "), gotDir, gotPrompt
	mu.Unlock()
	if !strings.Contains(args, "/fake/claude -p ") || !strings.Contains(args, "--resume s1") || d != dir {
		t.Fatalf("ran %q in %s", args, d)
	}
	if !strings.Contains(prompt, "Make the title bigger") || !strings.Contains(prompt, id) || strings.Contains(strings.ToLower(prompt), "magpie") || strings.Contains(prompt, "Claude") {
		t.Fatalf("the prompt: %s", prompt)
	}
	select {
	case e := <-events:
		if e != `data: {"comments":true}` {
			t.Fatalf("the stream said %s", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream said nothing")
	}

	// Stop: the run ends, and the thread shows none
	_, out = commentCall(h, id, map[string]any{"t": shell, "op": "reply", "thread": tid, "text": "hold on", "send": true})
	if errCode(out) != "" {
		t.Fatalf("reply and send: %v", out)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		p := gotPrompt
		mu.Unlock()
		if strings.Contains(p, "hold on") {
			break
		}
	}
	if th, _ := artifact.GetThread(id, tid); th.Run == nil || th.Run.State != "working" {
		t.Fatalf("not working: %+v", th.Run)
	}
	if _, out := commentCall(h, id, map[string]any{"t": shell, "op": "stop", "thread": tid}); errCode(out) != "" {
		t.Fatalf("stop: %v", out)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if th, _ = artifact.GetThread(id, tid); th.Run == nil {
			break
		}
	}
	if th.Run != nil || len(th.Comments) != 3 {
		t.Fatalf("after stop: %+v %+v", th, th.Run)
	}
	close(hold)
}

// A publisher's name, as each route records it, is the agent a comment
// goes to; Claude's other apps and unknown clients are none of them.
func TestSendAgentOf(t *testing.T) {
	for name, want := range map[string]string{
		"claude": "claude", "claude-cli": "claude", "claude-code": "claude", "Claude-Code": "claude",
		"codex-mcp-client": "codex", "codex_cli_rs": "codex", "opencode": "opencode", "opencode-1.2": "opencode",
		"pi": "pi", "pi-coding-agent": "pi", "claude-desktop": "", "pipx": "", "magpie": "", "": "", "cursor": "",
	} {
		if got := sendAgentOf(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
