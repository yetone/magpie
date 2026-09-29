package imagemcp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var png = []byte("\x89PNG\r\n\x1a\n fake")

// gw is a gateway that draws one image per n, and keeps what it was asked.
type gw struct {
	mu   sync.Mutex
	path []string
	body []string
	ua   []string
}

func (g *gw) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.path, g.body, g.ua = append(g.path, r.URL.Path), append(g.body, string(b)), append(g.ua, r.Header.Get("User-Agent"))
	g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer magpie" {
		w.WriteHeader(401)
		return
	}
	var req struct {
		N int `json:"n"`
	}
	json.Unmarshal(b, &req)
	var data []map[string]string
	for range max(req.N, 1) {
		data = append(data, map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png), "mime_type": "image/png"})
	}
	json.NewEncoder(w).Encode(map[string]any{"model": "art/gpt-image-1", "data": data})
}

// client talks to a server over pipes, answering roots/list with root.
type client struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	root string
}

func start(t *testing.T, g *gw, root string) *client {
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &server{gateway: up.URL, client: http.DefaultClient, enc: json.NewEncoder(outW), agent: "magpie-image/1", pending: map[string]chan rpc{}}
	go s.serve(inR)
	t.Cleanup(func() { inW.Close(); outW.Close() })
	c := &client{t: t, in: inW, out: bufio.NewScanner(outR), root: root}
	c.out.Buffer(make([]byte, 64<<10), 16<<20)
	return c
}

// call sends a request and returns its result, answering roots/list on the way.
func (c *client) call(method string, params any) map[string]any {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	go c.in.Write(append(b, '\n'))
	done := time.After(10 * time.Second)
	for {
		lines := make(chan string, 1)
		go func() {
			if c.out.Scan() {
				lines <- c.out.Text()
			}
		}()
		var line string
		select {
		case line = <-lines:
		case <-done:
			c.t.Fatalf("%s: no answer", method)
		}
		var m map[string]any
		json.Unmarshal([]byte(line), &m)
		if m["method"] == "roots/list" {
			ans, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"roots": []any{map[string]any{"uri": "file://" + c.root, "name": "p"}}}})
			go c.in.Write(append(ans, '\n'))
			continue
		}
		if m["error"] != nil {
			c.t.Fatalf("%s: %v", method, m["error"])
		}
		r, _ := m["result"].(map[string]any)
		return r
	}
}

func text(r map[string]any) string {
	c, _ := r["content"].([]any)
	if len(c) == 0 {
		return ""
	}
	m, _ := c[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func TestGenerateSavesInTheProject(t *testing.T) {
	g := &gw{}
	project := t.TempDir()
	c := start(t, g, project)
	c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"roots": map[string]any{}}, "clientInfo": map[string]any{"name": "claude-code", "version": "2.1.0"}})
	tools := c.call("tools/list", map[string]any{})
	if b, _ := json.Marshal(tools); !strings.Contains(string(b), `"generate_image"`) {
		t.Fatalf("tools %s", b)
	}
	r := c.call("tools/call", map[string]any{"name": "generate_image", "arguments": map[string]any{"prompt": "A Magpie, on a branch!", "n": 2}})
	out := text(r)
	if r["isError"] == true || strings.Count(out, filepath.Join(project, Folder)) != 2 {
		t.Fatalf("%v", r)
	}
	files, _ := filepath.Glob(filepath.Join(project, Folder, "a-magpie-on-a-branch-*.png"))
	if len(files) != 2 {
		t.Fatalf("saved %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != string(png) {
		t.Fatalf("file %q", b)
	}
	if g.ua[0] != "claude-code/2.1.0 magpie-image/1" || g.path[0] != "/v1/images/generations" {
		t.Fatalf("asked %v as %v", g.path, g.ua)
	}
	// a path, and a reference image: an edit, the file sent as a data URL
	os.WriteFile(filepath.Join(project, "in.png"), png, 0o644)
	r = c.call("tools/call", map[string]any{"name": "generate_image", "arguments": map[string]any{"prompt": "bluer", "path": "assets/hero.png", "reference_images": []string{"in.png"}}})
	if r["isError"] == true || !strings.Contains(text(r), filepath.Join(project, "assets", "hero.png")) {
		t.Fatalf("%v", r)
	}
	if g.path[1] != "/v1/images/edits" || !strings.Contains(g.body[1], "data:image/png;base64,") {
		t.Fatalf("asked %v %s", g.path, g.body[1])
	}
	// the same path again is not written over
	r = c.call("tools/call", map[string]any{"name": "generate_image", "arguments": map[string]any{"prompt": "bluer", "path": "assets/hero.png"}})
	if !strings.Contains(text(r), filepath.Join(project, "assets", "hero-2.png")) {
		t.Fatalf("%v", r)
	}
}

func TestGatewayRefusalIsAToolError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"no model draws: pick one in Settings → Images → Image generation"}}`)
	}))
	defer up.Close()
	s := &server{gateway: up.URL, client: http.DefaultClient, agent: "x", pending: map[string]chan rpc{}}
	_, err := s.generate(json.RawMessage(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "Image generation") {
		t.Fatalf("err = %v", err)
	}
}

func TestTarget(t *testing.T) {
	dir := t.TempDir()
	p, _ := target(dir, "out/", "Hello", ".png", 0, 1)
	if filepath.Dir(p) != filepath.Join(dir, "out") || !strings.HasPrefix(filepath.Base(p), "hello-") {
		t.Fatal(p)
	}
	p, _ = target(dir, "a.webp", "x", ".webp", 1, 3)
	if p != filepath.Join(dir, "a-2.webp") {
		t.Fatal(p)
	}
	// a JPEG asked for as a PNG is saved as what it is
	for path, want := range map[string]string{"hero.png": "hero.jpg", "hero.jpeg": "hero.jpeg", "hero.JPG": "hero.JPG", "hero.v2": "hero.v2.jpg"} {
		if p, _ = target(dir, path, "x", ".jpg", 0, 1); p != filepath.Join(dir, want) {
			t.Errorf("%s: %s, want %s", path, p, want)
		}
	}
}
