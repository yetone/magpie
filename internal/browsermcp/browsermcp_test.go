package browsermcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// Each line goes to the gateway's browser server with this process's
// session, and its answer comes back a line; a notification is answered
// nothing, and a gateway that can't be reached is said so, by id.
func TestBridgePassesLinesWithItsSession(t *testing.T) {
	var mu sync.Mutex
	var sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gateway.BrowserMCPPath || r.Header.Get("Authorization") != "Bearer "+gateway.Token {
			http.Error(w, "wrong", 400)
			return
		}
		mu.Lock()
		sessions = append(sessions, r.Header.Get(gateway.BrowserSessionHeader))
		mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.RawMessage
			Method string
		}
		json.Unmarshal(b, &m)
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"method": m.Method}})
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n\n" +
		`{"jsonrpc":"2.0","id":"b","method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := serve(in, &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(out.String(), `"method":"initialize"`) || !strings.Contains(out.String(), `"id":"b"`) {
		t.Fatalf("out: %q", out.String())
	}
	if len(sessions) != 3 || sessions[0] == "" || sessions[0] != sessions[1] || sessions[1] != sessions[2] {
		t.Fatalf("sessions: %q", sessions)
	}

	out.Reset()
	srv.Close()
	if err := serve(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n"), &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":7`) || !strings.Contains(out.String(), "magpie isn't running") {
		t.Fatalf("gateway gone: %q", out.String())
	}
}

// `magpie mcp search` passes each line to the gateway's web search server,
// with no browser session, and a gateway that can't be reached names
// Magpie Web Search.
func TestSearchBridgePassesLines(t *testing.T) {
	var mu sync.Mutex
	var paths, sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+gateway.Token {
			http.Error(w, "wrong", 400)
			return
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		sessions = append(sessions, r.Header.Get(gateway.BrowserSessionHeader))
		mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.RawMessage
			Method string
		}
		json.Unmarshal(b, &m)
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"method": m.Method}})
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"magpie"}}}` + "\n")
	var out bytes.Buffer
	if err := serveSearch(in, &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 || !strings.Contains(out.String(), `"method":"tools/call"`) {
		t.Fatalf("out: %q", out.String())
	}
	if len(paths) != 3 || paths[0] != gateway.SearchMCPPath || paths[2] != gateway.SearchMCPPath || strings.Join(sessions, "") != "" {
		t.Fatalf("paths %q, sessions %q", paths, sessions)
	}

	out.Reset()
	srv.Close()
	if err := serveSearch(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n"), &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":7`) || !strings.Contains(out.String(), "Magpie Web Search can't be reached") {
		t.Fatalf("gateway gone: %q", out.String())
	}
}

// magpie mcp vision relays each line to the gateway's image recognition
// server, a relative image path made the agent's folder's on the way
// (the gateway can't know where the agent runs), and says so by id when
// the gateway can't be reached (matrix0487 on Discord).
func TestVisionBridgePassesLines(t *testing.T) {
	var mu sync.Mutex
	var paths, images []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+gateway.Token {
			http.Error(w, "wrong", 400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.RawMessage
			Method string
			Params struct {
				Arguments struct{ Image string }
			}
		}
		json.Unmarshal(b, &m)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		images = append(images, m.Params.Arguments.Image)
		mu.Unlock()
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"method": m.Method}})
	}))
	defer srv.Close()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"analyze_image","arguments":{"image":"shots/a.png","prompt":"what"}}}` + "\n")
	var out bytes.Buffer
	if err := serveVision(in, &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 || !strings.Contains(out.String(), `"id":2`) {
		t.Fatalf("out: %q", out.String())
	}
	// the bridge passes messages side by side, so they reach the gateway
	// in any order: the one image named is the call's
	named := slices.DeleteFunc(slices.Clone(images), func(v string) bool { return v == "" })
	if len(paths) != 3 || slices.ContainsFunc(paths, func(p string) bool { return p != gateway.VisionMCPPath }) || len(named) != 1 || named[0] != filepath.Join(wd, "shots", "a.png") {
		t.Fatalf("paths %q, images %q", paths, images)
	}

	out.Reset()
	srv.Close()
	if err := serveVision(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n"), &out, srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":7`) || !strings.Contains(out.String(), "Magpie Image Recognition can't be reached") {
		t.Fatalf("gateway gone: %q", out.String())
	}
	if err := RunVision([]string{"extra"}); err == nil {
		t.Fatal("an argument was taken")
	}
}

// Only analyze_image's relative path is made the folder's: an absolute
// path, the home's, a URL, a drive, another tool and another method go as
// written, byte for byte.
func TestImageFromLeavesTheRest(t *testing.T) {
	wd := filepath.Join(string(filepath.Separator)+"work", "proj")
	call := func(name, image string) string {
		return `{"jsonrpc":"2.0","id":"x-1","method":"tools/call","params":{"name":"` + name + `","arguments":{"image":"` + image + `"}}}`
	}
	abs := filepath.Join(wd, "abs.png")
	absJSON, _ := json.Marshal(abs)
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":"x-1","method":"tools/call","params":{"name":"analyze_image","arguments":{"image":` + string(absJSON) + `}}}`,
		call("analyze_image", "~/a.png"),
		call("analyze_image", "https://example.com/a.png"),
		call("analyze_image", "data:image/png;base64,aGVsbG8="),
		call("analyze_image", "file:///a.png"),
		call("analyze_image", "C:/shots/a.png"),
		call("generate_image", "a.png"),
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"image":"a.png"}}`,
		`not json`,
	} {
		if got := string(imageFrom([]byte(line), wd)); got != line {
			t.Errorf("%s\n became %s", line, got)
		}
	}
	got := imageFrom([]byte(call("analyze_image", "a.png")), wd)
	var m struct {
		ID     string
		Params struct {
			Arguments struct{ Image string }
		}
	}
	if err := json.Unmarshal(got, &m); err != nil || m.ID != "x-1" || m.Params.Arguments.Image != filepath.Join(wd, "a.png") {
		t.Fatalf("relative: %s", got)
	}
}

// Behind magpie's MCP router this one process serves every agent, so a
// call that names its caller's session in _meta goes on in that session,
// each caller in a tab of its own; every other message, and a call with
// no such session, goes in this process's own.
func TestBridgeTakesTheRoutersCaller(t *testing.T) {
	own := "stdio-abc"
	for _, c := range []struct{ body, want string }{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_tabs","_meta":{"magpie/session":"hub-claude-1"}}}`, "hub:hub-claude-1"},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_tabs","_meta":{"magpie/session":" "}}}`, own},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_tabs"}}`, own},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"magpie/session":"hub-claude-1"}}}`, own},
		{`not json`, own},
	} {
		if got := callerSession([]byte(c.body), own); got != c.want {
			t.Errorf("%s: %q, want %q", c.body, got, c.want)
		}
	}
}

// magpie mcp memory relays each line to the gateway's memory server with
// the folder the agent started it in, and, from initialize on, the client
// name the agent gave, so the gateway knows the project and the agent.
func TestMemoryBridgeSendsFolderAndClient(t *testing.T) {
	var mu sync.Mutex
	type seen struct{ path, method, cwd, client string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.RawMessage
			Method string
		}
		json.Unmarshal(b, &m)
		mu.Lock()
		got = append(got, seen{r.URL.Path, m.Method, r.Header.Get(gateway.MemoryCwdHeader), r.Header.Get(gateway.MemoryClientHeader)})
		mu.Unlock()
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code","version":"2"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"x"}}}` + "\n")
	var out bytes.Buffer
	if err := serveMemory(in, &out, srv.URL, "/work/repo"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, g := range got {
		if g.path != gateway.MemoryMCPPath || g.cwd != "/work/repo" || g.client != "claude-code" {
			t.Fatalf("%s sent %+v", g.method, g)
		}
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; n != 2 {
		t.Fatalf("out: %q", out.String())
	}
}

func TestClientName(t *testing.T) {
	for in, want := range map[string]string{
		`{"method":"initialize","params":{"clientInfo":{"name":" codex-mcp-client "}}}`: "codex-mcp-client",
		`{"method":"tools/call","params":{"clientInfo":{"name":"codex"}}}`:              "",
		`{"method":"initialize","params":{"clientInfo":{"name":"a\nb"}}}`:               "",
		`not json`: "",
	} {
		if got := clientName([]byte(in)); got != want {
			t.Errorf("clientName(%s) = %q, want %q", in, got, want)
		}
	}
}
