package gateway

import (
	"context"
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

// An agent on another computer (#7890: magpie in Docker, the agent in
// another container) reaches the hub with a key at an address of its
// own; Magpie Artifacts' call is told that address, MAGPIE_PUBLIC_URL
// first, and a call from this computer is told none.
func TestHubTellsArtifactsTheAddressAnAgentAwayCalled(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	f := &fakeHub{on: true, tools: map[string][]HubTool{"": {{Server: "magpie-artifacts", Tool: "publish_artifact", Spec: hubSpec("publish_artifact")}}}}
	var mu sync.Mutex
	var bases []string
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		mu.Lock()
		bases = append(bases, HubCallerOf(ctx).Base)
		mu.Unlock()
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	}
	withHub(t, f)
	h := lanGuard(New().Handler())
	_, secrets := newCaller(t, "Agent")
	call := toolsCall("magpie-artifacts"+hubSep+"publish_artifact", `{"html":"<p>"}`)
	post := func(addr, host string, hdr ...string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, HubMCPAll, strings.NewReader(call))
		r.RemoteAddr, r.Host = addr, host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if m := readMCP(t, w); m.Error != nil || m.Result.IsError {
			t.Fatalf("%s at %s: %+v", addr, host, m)
		}
	}
	key := []string{"Authorization", "Bearer " + secrets[0]}
	post("172.18.0.5:40000", "magpie:3425", key...)
	t.Setenv("MAGPIE_PUBLIC_URL", "https://nas.example:8443")
	post("172.18.0.5:40000", "magpie:3425", key...)
	post(here, "127.0.0.1:3425")
	mu.Lock()
	defer mu.Unlock()
	want := []string{"http://magpie:3425", "https://nas.example:8443", ""}
	if strings.Join(bases, ",") != strings.Join(want, ",") {
		t.Fatalf("bases %q, want %q", bases, want)
	}
}

// awayCall is a tools/call of the artifacts server as the hub makes it
// for an agent away, at base: through `magpie mcp artifacts` in magpie's
// own folder (MemoryCwdHeader), on loopback.
func awayCall(t *testing.T, h http.Handler, base, cwd, tool string, args any) (string, bool) {
	t.Helper()
	a, _ := json.Marshal(args)
	meta := ""
	if base != "" {
		meta = `,"_meta":{"magpie/base":` + jsonString(base) + `}`
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + string(a) + meta + `}}`
	m := readMCP(t, mcpPostTo(h, ArtifactsMCPPath, "127.0.0.1:50000", body, MemoryCwdHeader, cwd))
	if m.Error != nil || len(m.Result.Content) != 1 {
		t.Fatalf("%s: %+v", tool, m)
	}
	return m.Result.Content[0].Text, m.Result.IsError
}

// An agent away is given its links at the address it called magpie at,
// and told, when a file it names isn't on magpie's computer, to pass the
// page as html; a file magpie does see (a folder mounted into both) is
// read as before. An agent here is answered as before.
func TestArtifactsAnswerAnAgentAway(t *testing.T) {
	h := artifacts(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	home := t.TempDir() // magpie's own folder, /config/home in its image
	const base = "http://magpie:3425"

	said, isErr := awayCall(t, h, base, home, artifactPublishTool, map[string]any{"html": "<title>Away</title><p>hi"})
	if isErr || !strings.Contains(said, base+ArtifactPagePath) || strings.Contains(said, URL()) || strings.Contains(said, "private to this computer") {
		t.Fatalf("publish away: %s", said)
	}
	if !strings.Contains(said, "MAGPIE_PUBLIC_URL") {
		t.Errorf("a link at the Host called doesn't say how to give another: %s", said)
	}
	id := publishedID(t, said)
	link := base + ArtifactPagePath + id + "/"
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{artifactReadTool, map[string]any{"id": id}},
		{artifactListTool, map[string]any{}},
	} {
		if said, isErr := awayCall(t, h, base, home, c.tool, c.args); isErr || !strings.Contains(said, link) {
			t.Errorf("%s away: %s", c.tool, said)
		}
	}
	if said, isErr := awayCall(t, h, base, home, artifactOpenTool, map[string]any{"id": id}); !isErr || !strings.Contains(said, link) {
		t.Errorf("open away: %v %s", isErr, said)
	}

	// the agent's own files: relative to its folder, or absolute there
	os.WriteFile(filepath.Join(home, "report.html"), []byte("<p>magpie's own"), 0o600)
	for _, p := range []string{"report.html", "/workspace/out/report.html", filepath.Join(home, "missing.html")} {
		said, isErr := awayCall(t, h, base, home, artifactPublishTool, map[string]any{"file_path": p})
		if !isErr || !strings.Contains(said, "html") || !strings.Contains(said, "another computer or in another container") || !strings.Contains(said, "mounted") {
			t.Errorf("%s away: %v %s", p, isErr, said)
		}
	}
	// a supporting file by path, too
	if said, isErr := awayCall(t, h, base, home, artifactPublishTool, map[string]any{"html": "<p>", "files": map[string]any{"a.js": "/workspace/a.js"}}); !isErr || !strings.Contains(said, "another computer or in another container") {
		t.Errorf("a file beside it away: %v %s", isErr, said)
	}
	// a folder mounted into both: the path magpie sees it at
	shared := filepath.Join(t.TempDir(), "shared.html")
	os.WriteFile(shared, []byte("<title>Shared</title><p>mounted"), 0o600)
	if said, isErr := awayCall(t, h, base, home, artifactPublishTool, map[string]any{"file_path": shared}); isErr || !strings.Contains(said, base+ArtifactPagePath) {
		t.Errorf("a mounted file away: %s", said)
	}

	// MAGPIE_PUBLIC_URL, the hub's first choice, needs no word on it
	t.Setenv("MAGPIE_PUBLIC_URL", "https://nas.example:8443")
	if said, _ := awayCall(t, h, "https://nas.example:8443", home, artifactPublishTool, map[string]any{"html": "<p>"}); !strings.Contains(said, "https://nas.example:8443"+ArtifactPagePath) || strings.Contains(said, "MAGPIE_PUBLIC_URL") {
		t.Errorf("publish at the public address: %s", said)
	}

	if said, isErr := awayCall(t, h, base, home, artifactDeleteTool, map[string]any{"id": id}); isErr || !strings.Contains(said, link) {
		t.Errorf("delete away: %s", said)
	}

	// what isn't an address with a host alone is no base
	for _, bad := range []string{"javascript:alert(1)", "http://x/a/", "http://u:p@x", "ftp://x", "http://x?a=1", "x:3425"} {
		if said, _ := awayCall(t, h, bad, home, artifactPublishTool, map[string]any{"html": "<p>"}); !strings.Contains(said, URL()+ArtifactPagePath) {
			t.Errorf("base %q: %s", bad, said)
		}
	}

	// an agent here: this computer's address, its folder, the stat's own error
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if said, _ := awayCall(t, h, "", home, artifactPublishTool, map[string]any{"file_path": "report.html"}); !strings.Contains(said, URL()+ArtifactPagePath) || !strings.Contains(said, "private to this computer") {
		t.Errorf("publish here: %s", said)
	}
	if said, isErr := awayCall(t, h, "", home, artifactPublishTool, map[string]any{"file_path": "/workspace/out/report.html"}); !isErr || strings.Contains(said, "another computer") {
		t.Errorf("a missing file here: %v %s", isErr, said)
	}
}

// The page an agent away was given opens in a browser on another
// computer while magpie is on the network, without a key, read-only: no
// token and no capability in it, and its store and assets stay this
// computer's. Off the network, and at a name rebound to loopback, it
// doesn't open.
func TestArtifactPageOpensAwayReadOnly(t *testing.T) {
	h := artifacts(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<title>T</title><p>hi"), Files: map[string][]byte{"d.json": []byte("[]")},
		Capabilities: artifact.Capabilities{"db": json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	page := ArtifactPagePath + a.ID + "/"
	get := func(method, path string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr, r.Host = "192.0.2.7:4000", "magpie:3425"
		r.Header.Set("User-Agent", "Mozilla/5.0")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get(http.MethodGet, page); w.Code < 400 {
		t.Fatalf("magpie off the network served another computer: %d", w.Code)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	if !remoteKeyed() {
		t.Fatal("MAGPIE_ADDR 0.0.0.0 isn't on the network")
	}
	w := get(http.MethodGet, page)
	body := w.Body.String()
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != 200 || !strings.Contains(body, "<p>hi") || !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("away: %d %q %s", w.Code, csp, body)
	}
	tok, _ := artifact.PageToken(a.ID)
	if tok == "" || strings.Contains(body, tok) || !strings.Contains(body, `"shared":true`) {
		t.Fatalf("the page away carries a token or the full runtime: %s", body)
	}
	// a tab there, as a browser asks for one, reads the page alone: no
	// viewer shell, whose token writes comments and sends to the agent
	shell, _ := artifact.ShellToken(a.ID)
	if d := get(http.MethodGet, page, "Sec-Fetch-Dest", "document"); d.Code != 200 || !strings.Contains(d.Body.String(), "<p>hi") ||
		!strings.Contains(d.Body.String(), `"shared":true`) || !strings.HasPrefix(d.Header().Get("Content-Security-Policy"), "sandbox ") ||
		shell == "" || strings.Contains(d.Body.String(), shell) || strings.Contains(d.Body.String(), tok) {
		t.Fatalf("a tab away: %d %s", d.Code, d.Body)
	}
	if f := get(http.MethodGet, page+"d.json", "Origin", "null", "Sec-Fetch-Mode", "cors"); f.Code != 200 || f.Body.String() != "[]" {
		t.Errorf("its file away: %d %s", f.Code, f.Body)
	}
	// its store and assets: not without a key, nor with the page's token
	for _, p := range []string{page + artifactAPIDir + "events", page + artifactAPIDir + "shell", page + artifactAPIDir + "live", page + artifactAPIDir + "app.webmanifest", artifactBlobPath + "x"} {
		if w := get(http.MethodGet, p, "Origin", "null", "Sec-Fetch-Mode", "cors"); w.Code < 400 {
			t.Errorf("GET %s away: %d", p, w.Code)
		}
	}
	for _, p := range []string{"db", "publish", "meta", "upload", "assets", "comments", "sample", "tts", "dictation", "mcp", "files"} {
		r := httptest.NewRequest(http.MethodPost, page+artifactAPIDir+p, strings.NewReader(`{}`))
		r.RemoteAddr, r.Host = "192.0.2.7:4000", "magpie:3425"
		r.Header.Set("Origin", "null")
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Errorf("POST %s away: %d", p, w.Code)
		}
	}
	// nothing else of the gateway's opens by the exemption
	for _, p := range []string{"/v1/models", "/api/state", ArtifactPagePath} {
		if w := get(http.MethodGet, p); w.Code < 400 && w.Code != http.StatusNotFound {
			t.Errorf("GET %s away: %d", p, w.Code)
		}
	}
	// here, at a name rebound to loopback: still not
	r := httptest.NewRequest(http.MethodGet, page, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "rebound.example:3425"
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() < 400 {
		t.Error("a rebound name opened a page")
	}
	// the browser here still runs it with its runtime
	if w := pageGet(h, page); w.Code != 200 || !strings.Contains(w.Body.String(), tok) {
		t.Errorf("here: %d", w.Code)
	}
}
