package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
)

// artifacts is a gateway whose artifacts are kept in a folder of the
// test's own.
func artifacts(t *testing.T) http.Handler {
	t.Helper()
	fresh(t)
	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.CloseDBs(); artifact.Dir = was })
	return lanGuard(New().Handler())
}

// artifactsCall is one tools/call of the artifacts server from an agent
// on this computer: the tool's text and whether it is an error.
func artifactsCall(t *testing.T, h http.Handler, tool string, args any, hdr ...string) (string, bool) {
	t.Helper()
	a, _ := json.Marshal(args)
	// the call says it works in no folder, as `magpie mcp artifacts`
	// started outside one does, so the gateway doesn't ask the client
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + string(a) + `,"_meta":{"magpie/roots":[]}}}`
	if len(hdr) > 0 {
		body = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + string(a) + `}}`
	}
	m := readMCP(t, mcpPostTo(h, ArtifactsMCPPath, "127.0.0.1:50000", body, hdr...))
	if m.Error != nil || len(m.Result.Content) != 1 {
		t.Fatalf("%s: %+v", tool, m)
	}
	return m.Result.Content[0].Text, m.Result.IsError
}

// pageGet is a GET of path by the browser on this computer.
func pageGet(h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "127.0.0.1:50001"
	r.Host = "127.0.0.1:3425"
	r.Header.Set("User-Agent", "Mozilla/5.0")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func publishedID(t *testing.T, said string) string {
	t.Helper()
	id, _, ok := ArtifactRef(said[strings.Index(said, ArtifactPagePath):])
	if !ok {
		t.Fatalf("no address in %q", said)
	}
	return id
}

// An agent publishes a page from a file in the folder it works in, with
// a script beside it, and updates it: every version is kept at its own
// address, the newest at the artifact's, and a publish built on an older
// version than the newest is refused.
func TestArtifactsPublishAndUpdate(t *testing.T) {
	h := artifacts(t)
	wd := t.TempDir()
	os.WriteFile(filepath.Join(wd, "report.html"), []byte(`<!doctype html><title>Sales report</title><script src="app.js"></script>`), 0o600)
	said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{
		"file_path": "report.html", "files": map[string]any{"app.js": map[string]any{"text": "console.log(1)"}},
	}, MemoryCwdHeader, wd, MemoryClientHeader, "codex-mcp-client")
	if isErr || !strings.Contains(said, `"Sales report"`) || !strings.Contains(said, URL()+ArtifactPagePath) {
		t.Fatalf("publish: %s", said)
	}
	id := publishedID(t, said)
	art, err := artifact.Get(id)
	if err != nil || art.Latest().Agent != "codex" {
		t.Fatalf("the version's agent: %+v %v", art, err)
	}

	w := pageGet(h, ArtifactPagePath+id+"/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Sales report") || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("page: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if js := pageGet(h, ArtifactPagePath+id+"/app.js", "Origin", "null", "Sec-Fetch-Mode", "cors"); js.Code != 200 || js.Body.String() != "console.log(1)" ||
		!strings.HasPrefix(js.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("its script, from the page's opaque origin: %d %v %s", js.Code, js.Header(), js.Body)
	}

	said, isErr = artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "<p>two", "version": 1})
	if isErr || !strings.Contains(said, "version 2") {
		t.Fatalf("update: %s", said)
	}
	if said, isErr = artifactsCall(t, h, artifactPublishTool, map[string]any{"id": ArtifactURL(id, 0), "html": "<p>mine", "version": 1}); !isErr || !strings.Contains(said, artifactReadTool) {
		t.Fatalf("a publish over a newer version: %s", said)
	}
	if w := pageGet(h, ArtifactPagePath+id+"/"); !strings.Contains(w.Body.String(), "<p>two") {
		t.Fatalf("newest: %s", w.Body)
	}
	if w := pageGet(h, ArtifactPagePath+id+"@1/"); !strings.Contains(w.Body.String(), "Sales report") || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("version 1: %v %s", w.Header(), w.Body)
	}
	if w := pageGet(h, ArtifactPagePath+id); w.Code != http.StatusFound || w.Header().Get("Location") != ArtifactPagePath+id+"/" {
		t.Fatalf("no slash: %d %v", w.Code, w.Header())
	}

	said, _ = artifactsCall(t, h, artifactReadTool, map[string]any{"id": id, "version": 1})
	if !strings.Contains(said, "version 1 of 2") || !strings.Contains(said, "files: app.js") || !strings.Contains(said, "<title>Sales report</title>") {
		t.Fatalf("read: %s", said)
	}
	if said, _ = artifactsCall(t, h, artifactReadTool, map[string]any{"id": id, "path": "app.js"}); !strings.Contains(said, "console.log(1)") {
		t.Fatalf("read a file: %s", said)
	}
	if said, _ = artifactsCall(t, h, artifactListTool, map[string]any{"query": "sales"}); !strings.Contains(said, id) || !strings.Contains(said, "version 2") {
		t.Fatalf("list: %s", said)
	}

	var opened string
	was := openArtifactURL
	openArtifactURL = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openArtifactURL = was })
	if artifactsCall(t, h, artifactOpenTool, map[string]any{"id": id}); opened != ArtifactURL(id, 0) {
		t.Fatalf("opened %q", opened)
	}

	if said, isErr = artifactsCall(t, h, artifactDeleteTool, map[string]any{"id": id}); isErr {
		t.Fatalf("delete: %s", said)
	}
	if w := pageGet(h, ArtifactPagePath+id+"/"); w.Code != 404 {
		t.Fatalf("a deleted artifact opened: %d", w.Code)
	}
}

// A relative path with no folder known, a folder, and a page given twice
// each say what to do instead of publishing.
func TestArtifactsPublishSaysWhy(t *testing.T) {
	h := artifacts(t)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"file_path": "x.html"}, "absolute path"},
		{map[string]any{"file_path": t.TempDir()}, "folder"},
		{map[string]any{"file_path": "/x.html", "html": "<p>"}, "not both"},
		{map[string]any{}, "file_path"},
		{map[string]any{"id": "nope", "html": "<p>"}, "isn't an artifact"},
		{map[string]any{"html": "<p>", "files": map[string]any{"../x": map[string]any{"text": "x"}}}, ".."},
	} {
		if said, isErr := artifactsCall(t, h, artifactPublishTool, c.args); !isErr || !strings.Contains(said, c.want) {
			t.Errorf("%v: %v %s", c.args, isErr, said)
		}
	}
}

// The server reads and writes files for the agent: a page, a browser and
// another computer are turned away.
func TestArtifactsMCPOnlyForAgentsHere(t *testing.T) {
	h := artifacts(t)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if m := readMCP(t, mcpPostTo(h, ArtifactsMCPPath, "127.0.0.1:50000", list)); len(m.Result.Tools) != 7 {
		t.Fatalf("tools: %+v", m.Result.Tools)
	}
	for _, c := range []struct {
		name, addr string
		hdr        []string
	}{
		{"a page", "127.0.0.1:50000", []string{"Origin", "http://127.0.0.1:3425"}},
		{"an artifact's page", "127.0.0.1:50000", []string{"Origin", "null"}},
		{"a browser", "127.0.0.1:50000", []string{"User-Agent", "Mozilla/5.0", "Sec-Fetch-Mode", "cors"}},
		{"another computer", "192.0.2.7:4000", nil},
	} {
		if w := mcpPostTo(h, ArtifactsMCPPath, c.addr, list, c.hdr...); w.Code == 200 {
			t.Errorf("%s was answered: %s", c.name, w.Body)
		}
	}
}

// An agent that defers MCP tools behind a search shows the server as the
// start of its instructions only (Codex 0.172 cuts it near 250
// characters), so what makes an agent publish a page it writes, and the
// tool it searches for, are in that start.
func TestArtifactsInstructionsLeadWithTheTrigger(t *testing.T) {
	h := artifacts(t)
	var m struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	w := mcpPostTo(h, ArtifactsMCPPath, "127.0.0.1:50000", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.172.0"}}}`)
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	head := m.Result.Instructions[:min(200, len(m.Result.Instructions))]
	for _, want := range []string{artifactPublishTool, "HTML", "tool", "game", "app", "HTML file"} {
		if !strings.Contains(head, want) {
			t.Errorf("the first 200 characters don't say %q: %q", want, head)
		}
	}
	for _, not := range []string{"magpie", "Magpie", "Codex", "Claude Code"} {
		if strings.Contains(m.Result.Instructions, not) {
			t.Errorf("the instructions name %q", not)
		}
	}
}

// A page runs in a sandbox with an opaque origin: it reads its own files
// and nothing else of the gateway's, and only the browser on this
// computer, at this computer's own name, opens it.
func TestArtifactPageSandboxed(t *testing.T) {
	h := artifacts(t)
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<p>hi"), Files: map[string][]byte{"d.json": []byte("[]")}})
	if err != nil {
		t.Fatal(err)
	}
	page := ArtifactPagePath + a.ID + "/"
	w := pageGet(h, page)
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != 200 || !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "allow-scripts") {
		t.Fatalf("%d, CSP %q", w.Code, csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("no nosniff: %v", w.Header())
	}
	if f := pageGet(h, page+"d.json", "Origin", "null"); f.Code != 200 || f.Header().Get("Content-Security-Policy") != csp {
		t.Errorf("a file: %d %v", f.Code, f.Header())
	}
	// the page's origin reads nothing else here
	for _, p := range []string{"/v1/models", "/api/state", "/v1/magpie/quotas"} {
		if w := pageGet(h, p, "Origin", "null", "Sec-Fetch-Mode", "cors"); w.Code < 400 {
			t.Errorf("Origin null read %s: %d", p, w.Code)
		}
	}
	// nor writes through the exemption
	r := httptest.NewRequest(http.MethodPost, page, strings.NewReader("{}"))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() < 400 {
		t.Errorf("a POST from Origin null under %s was taken", ArtifactPagePath)
	}
	// another site's page, another computer, a name rebound to 127.0.0.1
	if w := pageGet(h, page+"d.json", "Origin", "https://evil.example", "Sec-Fetch-Mode", "cors"); w.Code < 400 {
		t.Errorf("another site read a file: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, page, nil)
	r.RemoteAddr, r.Host = "192.0.2.7:4000", "127.0.0.1:3425"
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() < 400 {
		t.Error("another computer opened a page")
	}
	r = httptest.NewRequest(http.MethodGet, page, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "rebound.example:3425"
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() < 400 {
		t.Error("a rebound name opened a page")
	}
	// a path that climbs out
	for _, p := range []string{page + "../" + a.ID + "/meta.json", page + "..%2fmeta.json", ArtifactPagePath + "nope/"} {
		if w := pageGet(h, p); w.Code == 200 {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
}

func TestArtifactRef(t *testing.T) {
	id := strings.Repeat("a", 26)
	for in, want := range map[string]int{
		id: 0, id + "@3": 3, "http://127.0.0.1:3425/a/" + id + "/": 0, "http://127.0.0.1:3425/a/" + id + "@2/x.js": 2,
	} {
		got, n, ok := ArtifactRef(in)
		if !ok || got != id || n != want {
			t.Errorf("%s: %s %d %v", in, got, n, ok)
		}
	}
	for _, in := range []string{"", "nope", id + "@0", strings.Repeat("a", 27), strings.Repeat("A", 26)} {
		if _, _, ok := ArtifactRef(in); ok {
			t.Errorf("%q read as an artifact", in)
		}
	}
}

// shareSite is usemagpie.ai's /api/shares for one share, as
// site/shares.js answers: a create, then its updates and its delete.
func shareSite(t *testing.T) (puts func() int, up func() bool) {
	t.Helper()
	var mu sync.Mutex
	n, live := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		const id = "abcdefghijklmnop"
		if r.Method != http.MethodPost && (!live || r.Header.Get("Authorization") != "Bearer tok") {
			http.Error(w, `{"error":"no such shared page: it was taken down"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			live = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"` + id + `","url":"https://usemagpie.ai/a/` + id + `","token":"tok","version":1}`))
		case http.MethodGet:
			// its assets (none)
			w.Write([]byte(`{"blobs":[]}`))
		case http.MethodPut:
			n++
			w.Write([]byte(`{"id":"` + id + `","url":"https://usemagpie.ai/a/` + id + `","version":2}`))
		case http.MethodDelete:
			live = false
			w.Write([]byte(`{"deleted":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_SHARE_SITE", srv.URL)
	return func() int { mu.Lock(); defer mu.Unlock(); return n },
		func() bool { mu.Lock(); defer mu.Unlock(); return live }
}

// Once the user shares an artifact, an agent's next version shows at its
// shared link, and the agent says so; list and read name the link, and a
// delete says it took the link down too. An agent can't share one.
func TestArtifactsSharedFollowsPublish(t *testing.T) {
	h := artifacts(t)
	puts, up := shareSite(t)
	said, _ := artifactsCall(t, h, artifactPublishTool, map[string]any{"html": "<title>Plan</title>one"})
	id := publishedID(t, said)
	if !strings.Contains(said, "private to this computer") || puts() != 0 || up() {
		t.Fatalf("an unshared publish: %s", said)
	}
	if _, err := artifact.ShareLatest(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "two"})
	if isErr || puts() != 1 || !strings.Contains(said, "https://usemagpie.ai/a/abcdefghijklmnop") || strings.Contains(said, "private") {
		t.Fatalf("a shared publish (%d updates): %s", puts(), said)
	}
	if sh, _ := artifact.Shared(id); sh == nil || sh.Version != 2 {
		t.Fatalf("shared %+v", sh)
	}
	for _, tool := range []string{artifactListTool, artifactReadTool} {
		if said, _ := artifactsCall(t, h, tool, map[string]any{"id": id}); !strings.Contains(said, "shared at https://usemagpie.ai/a/abcdefghijklmnop (version 2)") {
			t.Fatalf("%s: %s", tool, said)
		}
	}
	if said, isErr := artifactsCall(t, h, artifactDeleteTool, map[string]any{"id": id}); isErr || up() || !strings.Contains(said, "its shared link") {
		t.Fatalf("delete (link up %v): %s", up(), said)
	}
	for _, tool := range artifactsMCPTools() {
		if b, _ := json.Marshal(tool.(map[string]any)["inputSchema"]); strings.Contains(string(b), "share") {
			t.Fatalf("an agent can share: %s", b)
		}
	}
}

// The guide is a tool of its own, loaded with the server, read-only: it
// gives the capabilities a list or a declaration names, and publish's
// capabilities point an agent to it.
func TestArtifactsGuideTool(t *testing.T) {
	h := artifacts(t)
	var guide, publish map[string]any
	for _, x := range artifactsMCPTools() {
		switch m := x.(map[string]any); m["name"] {
		case artifactGuideTool:
			guide = m
		case artifactPublishTool:
			publish = m
		}
	}
	if guide == nil || guide["annotations"].(map[string]any)["readOnlyHint"] != true || guide["_meta"].(map[string]any)["anthropic/alwaysLoad"] != true {
		t.Fatalf("the guide tool: %v", guide)
	}
	if b, _ := json.Marshal(publish["inputSchema"]); !strings.Contains(string(b), artifactGuideTool) || !strings.Contains(string(b), "view, interact, admin, owner, self") {
		t.Fatalf("publish's capabilities don't name the guide or the levels: %s", b)
	}
	for _, args := range []map[string]any{{"capabilities": []string{"db"}}, {"capabilities": map[string]any{"db": map[string]any{}}}} {
		said, isErr := artifactsCall(t, h, artifactGuideTool, args)
		if isErr || !strings.Contains(said, "db.collection(") || strings.Contains(said, "\nsample: ") {
			t.Fatalf("guide %v: %s", args, said)
		}
	}
	if said, isErr := artifactsCall(t, h, artifactGuideTool, map[string]any{}); isErr || !strings.Contains(said, "\nsample: ") || !strings.Contains(said, "\nassets: ") {
		t.Fatalf("the whole guide: %s", said)
	}
	if said, isErr := artifactsCall(t, h, artifactGuideTool, map[string]any{"capabilities": []string{"speech"}}); !isErr || !strings.Contains(said, "speech") {
		t.Fatalf("speech: %v %s", isErr, said)
	}
}

// #7665: with magpie web at MAGPIE_PUBLIC_URL (SetArtifactsAt), what an
// agent is told and lists names the page there, where the user's browser
// on another computer is signed in; open_artifact still opens this
// computer's browser at this computer's address, and the gateway's own
// /a/ still opens to that browser alone.
func TestArtifactLinksAtWebAddress(t *testing.T) {
	h := artifacts(t)
	SetArtifactsAt("https://magpie.example.com/")
	t.Cleanup(func() { SetArtifactsAt("") })
	said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"html": "<title>Plan</title><p>hi"})
	if isErr {
		t.Fatal(said)
	}
	id := publishedID(t, said)
	at := "https://magpie.example.com/a/" + id + "/"
	if !strings.Contains(said, "Published \"Plan\": "+at+"\n") || strings.Contains(said, URL()) || !strings.Contains(said, "signed in to magpie web") {
		t.Fatalf("publish: %s", said)
	}
	for tool, args := range map[string]map[string]any{artifactReadTool: {"id": id}, artifactListTool: {}} {
		if said, isErr := artifactsCall(t, h, tool, args); isErr || !strings.Contains(said, at) || strings.Contains(said, URL()) {
			t.Errorf("%s: %s", tool, said)
		}
	}
	if u := ArtifactURL(id, 2); u != "https://magpie.example.com/a/"+id+"@2/" {
		t.Errorf("a version: %s", u)
	}
	var opened string
	was := openArtifactURL
	openArtifactURL = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openArtifactURL = was })
	if said, isErr := artifactsCall(t, h, artifactOpenTool, map[string]any{"id": id}); isErr || opened != URL()+"/a/"+id+"/" {
		t.Errorf("open: %q %s", opened, said)
	}
	r := httptest.NewRequest(http.MethodGet, "/a/"+id+"/", nil)
	r.RemoteAddr, r.Host = "192.0.2.7:4000", "magpie.example.com"
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() < 400 {
		t.Error("the gateway opened a page to another computer")
	}

	// an agent away (#7890) is given magpie web's address too, not the
	// one it called magpie at, which the user's browser isn't signed in to
	for tool, args := range map[string]map[string]any{artifactPublishTool: {"id": id, "html": "<p>away"}, artifactReadTool: {"id": id}, artifactListTool: {}} {
		said, isErr := awayCall(t, h, "http://magpie:3425", t.TempDir(), tool, args)
		if isErr || !strings.Contains(said, at) || strings.Contains(said, "http://magpie:3425") {
			t.Errorf("%s away: %s", tool, said)
		}
		if tool == artifactPublishTool && !strings.Contains(said, "signed in to magpie web") {
			t.Errorf("publish away: %s", said)
		}
	}

	// on the network, the gateway opens the address without a key
	// (artifactPageAsked), as the publish says
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	if said, _ := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "<p>net"}); !strings.Contains(said, at) || !strings.Contains(said, "without a key") || strings.Contains(said, "signed in") {
		t.Errorf("publish on the network: %s", said)
	}
	t.Setenv("MAGPIE_ADDR", "")

	// without it, this computer's address, as before
	SetArtifactsAt("")
	if said, _ := artifactsCall(t, h, artifactReadTool, map[string]any{"id": id}); !strings.Contains(said, URL()+"/a/"+id+"/") {
		t.Errorf("read without a web address: %s", said)
	}
	if said, _ := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "<p>two"}); !strings.Contains(said, "private to this computer") {
		t.Errorf("publish without a web address: %s", said)
	}
}
