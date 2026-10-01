package imagemcp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
			ans, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"roots": []any{map[string]any{"uri": fileURI(c.root), "name": "p"}}}})
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

// mp4 is the start of a real MP4: the gateway's content has to be a video.
var mp4 = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), []byte("-the rest of the video")...)

// vgw is a gateway that makes videos: a request starts one, it is in
// progress for `polls` asks and then done, or failed, or never ready.
type vgw struct {
	mu      sync.Mutex
	path    []string
	body    []string
	polls   int
	asked   int
	ending  string // completed (default), failed, stuck
	content []byte
}

func (g *vgw) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.path, g.body = append(g.path, r.Method+" "+r.URL.Path), append(g.body, string(b))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/videos":
		if strings.Contains(string(b), "REFUSED") {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Duration must be between 1 and 15 seconds"}}`)
			return
		}
		io.WriteString(w, `{"id":"video_grok.r1.1","object":"video","status":"queued","model":"grok/grok-imagine-video"}`)
	case r.URL.Path == "/v1/videos/video_grok.r1.1":
		g.asked++
		switch {
		case g.ending == "stuck" || g.asked <= g.polls:
			io.WriteString(w, `{"id":"video_grok.r1.1","status":"in_progress","progress":40}`)
		case g.ending == "failed":
			io.WriteString(w, `{"id":"video_grok.r1.1","status":"failed","error":{"code":"invalid_argument","message":"image_url must either be a base64-encoded image or a URL."}}`)
		default:
			io.WriteString(w, `{"id":"video_grok.r1.1","status":"completed","progress":100,"seconds":"6","model":"grok/grok-imagine-video"}`)
		}
	case r.URL.Path == "/v1/videos/video_grok.r1.1/content":
		w.Header().Set("Content-Type", "video/mp4")
		if g.content != nil {
			w.Write(g.content)
		} else {
			w.Write(mp4)
		}
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"not here"}}`)
	}
}

func startVideo(t *testing.T, g *vgw, root string) *client {
	t.Helper()
	was := videoPollEvery
	videoPollEvery = time.Millisecond
	t.Cleanup(func() { videoPollEvery = was })
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &server{gateway: up.URL, client: http.DefaultClient, enc: json.NewEncoder(outW), agent: "magpie-image/1", pending: map[string]chan rpc{}}
	go s.serve(inR)
	t.Cleanup(func() { inW.Close(); outW.Close() })
	c := &client{t: t, in: inW, out: bufio.NewScanner(outR), root: root}
	c.out.Buffer(make([]byte, 64<<10), 16<<20)
	c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"roots": map[string]any{}}, "clientInfo": map[string]any{"name": "claude-code", "version": "2.1.0"}})
	return c
}

func film(c *client, args map[string]any) map[string]any {
	return c.call("tools/call", map[string]any{"name": "generate_video", "arguments": args})
}

func TestBothToolsAreListed(t *testing.T) {
	c := startVideo(t, &vgw{}, t.TempDir())
	b, _ := json.Marshal(c.call("tools/list", map[string]any{}))
	if !strings.Contains(string(b), `"generate_image"`) || !strings.Contains(string(b), `"generate_video"`) {
		t.Fatalf("tools %s", b)
	}
}

func TestGenerateVideoWaitsAndSavesTheMP4(t *testing.T) {
	g := &vgw{polls: 2}
	project := t.TempDir()
	c := startVideo(t, g, project)
	r := film(c, map[string]any{"prompt": "A kite sways, gently!", "seconds": 6, "size": "16:9"})
	out := text(r)
	if r["isError"] == true || !strings.Contains(out, "6-second video with grok/grok-imagine-video") || !strings.Contains(out, filepath.Join(project, VideoFolder)) {
		t.Fatalf("%v", r)
	}
	files, _ := filepath.Glob(filepath.Join(project, VideoFolder, "a-kite-sways-gently-*.mp4"))
	if len(files) != 1 {
		t.Fatalf("saved %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != string(mp4) {
		t.Fatalf("file %q", b)
	}
	if g.path[0] != "POST /v1/videos" || !strings.Contains(g.body[0], `"seconds":"6"`) || !strings.Contains(g.body[0], `"size":"16:9"`) || !strings.Contains(g.body[0], `"prompt":"A kite sways, gently!"`) {
		t.Fatalf("asked %v %v", g.path, g.body)
	}
	// started, asked after until done (2 in progress + 1 done), then fetched
	if g.asked != 3 || g.path[len(g.path)-1] != "GET /v1/videos/video_grok.r1.1/content" {
		t.Fatalf("polled %d times: %v", g.asked, g.path)
	}
}

func TestGenerateVideoFromAnImage(t *testing.T) {
	g := &vgw{}
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "first.png"), png, 0o644)
	c := startVideo(t, g, project)
	r := film(c, map[string]any{"prompt": "it moves", "image": "first.png", "reference_images": []string{"https://example.com/a.png", "first.png"}, "path": "clips/intro.mp4"})
	if r["isError"] == true || !strings.Contains(text(r), filepath.Join(project, "clips", "intro.mp4")) {
		t.Fatalf("%v", r)
	}
	var sent struct {
		In   map[string]string   `json:"input_reference"`
		Refs []map[string]string `json:"reference_images"`
	}
	json.Unmarshal([]byte(g.body[0]), &sent)
	if !strings.HasPrefix(sent.In["image_url"], "data:image/png;base64,") || len(sent.Refs) != 2 || sent.Refs[0]["image_url"] != "https://example.com/a.png" {
		t.Fatalf("asked %s", g.body[0])
	}
	// the same path again is not written over; a path with no extension is a folder, as for images; another video extension becomes .mp4
	r = film(c, map[string]any{"prompt": "it moves", "path": "clips/intro.mp4"})
	r2 := film(c, map[string]any{"prompt": "it moves", "path": "clips/outro"})
	r3 := film(c, map[string]any{"prompt": "it moves", "path": "clips/end.mov"})
	if !strings.Contains(text(r), filepath.Join(project, "clips", "intro-2.mp4")) || !strings.Contains(text(r2), filepath.Join(project, "clips", "outro")+string(filepath.Separator)+"it-moves-") || !strings.Contains(text(r3), filepath.Join(project, "clips", "end.mp4")) {
		t.Fatalf("%q %q %q", text(r), text(r2), text(r3))
	}
}

func TestGenerateVideoSaysWhyItFailed(t *testing.T) {
	project := t.TempDir()
	c := startVideo(t, &vgw{ending: "failed", polls: 1}, project)
	r := film(c, map[string]any{"prompt": "x"})
	if r["isError"] != true || !strings.Contains(text(r), "the video failed: image_url must either be a base64-encoded image or a URL.") {
		t.Fatalf("%v", r)
	}
	if files, _ := filepath.Glob(filepath.Join(project, VideoFolder, "*")); len(files) != 0 {
		t.Fatalf("a failed video was saved: %v", files)
	}
	c = startVideo(t, &vgw{}, project)
	r = film(c, map[string]any{"prompt": "REFUSED", "seconds": 99})
	if r["isError"] != true || !strings.Contains(text(r), "no video: Duration must be between 1 and 15 seconds") {
		t.Fatalf("%v", r)
	}
}

func TestGenerateVideoGivesUpWaiting(t *testing.T) {
	was := videoWait
	videoWait = 30 * time.Millisecond
	defer func() { videoWait = was }()
	c := startVideo(t, &vgw{ending: "stuck"}, t.TempDir())
	r := film(c, map[string]any{"prompt": "x"})
	if r["isError"] != true || !strings.Contains(text(r), "video_grok.r1.1") || !strings.Contains(text(r), "wasn't ready") {
		t.Fatalf("%v", r)
	}
}

func TestGenerateVideoRefusesWhatIsNotAVideo(t *testing.T) {
	c := startVideo(t, &vgw{content: []byte("<html>not a video</html>")}, t.TempDir())
	r := film(c, map[string]any{"prompt": "x"})
	if r["isError"] != true || !strings.Contains(text(r), "isn't a video") {
		t.Fatalf("%v", r)
	}
}

func TestGenerateVideoChecksItsArguments(t *testing.T) {
	c := startVideo(t, &vgw{}, t.TempDir())
	for name, args := range map[string]map[string]any{
		"no prompt":            {"prompt": "  "},
		"fractional seconds":   {"prompt": "x", "seconds": 2.5},
		"zero seconds":         {"prompt": "x", "seconds": 0.0},
		"seconds not a number": {"prompt": "x", "seconds": "a few"},
		"a missing image":      {"prompt": "x", "image": "nope.png"},
	} {
		if r := film(c, args); r["isError"] != true {
			t.Errorf("%s: %v", name, r)
		}
	}
	// seconds may come as a string
	if r := film(c, map[string]any{"prompt": "x", "seconds": "6"}); r["isError"] == true {
		t.Errorf("seconds \"6\": %v", r)
	}
}

// fileURI is p as a client names a root: file:///C:/p on Windows.
func fileURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// A root or a reference given as a file:// URI is the file it names, a
// drive's or a share's on Windows.
func TestFilePath(t *testing.T) {
	cases := map[string]string{"file:///tmp/a%20b.png": "/tmp/a b.png"}
	if runtime.GOOS == "windows" {
		cases = map[string]string{
			"file:///C:/Users/me/a%20b":    `C:\Users\me\a b`,
			"file:///c%3A/Users/me/p":      `c:\Users\me\p`,
			"file://server/share/p/i.png":  `\\server\share\p\i.png`,
			"file://localhost/D:/work/one": `D:\work\one`,
		}
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := filePath(u); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
