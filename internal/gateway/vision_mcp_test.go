package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
)

// visionPost sends one JSON-RPC body to the image recognition MCP server
// through h, from addr, with hdr as name, value pairs.
func visionPost(h http.Handler, addr, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, VisionMCPPath, strings.NewReader(body))
	r.RemoteAddr = addr
	r.Host = "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// analyzeCall is a tools/call of analyze_image with image and, when it
// isn't "", prompt.
func analyzeCall(image, prompt string) string {
	args := map[string]string{"image": image}
	if prompt != "" {
		args["prompt"] = prompt
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "analyze_image", "arguments": args}})
	return string(b)
}

// pngFile is a file whose bytes read as a PNG, in a test's folder.
func pngFile(t *testing.T) (string, string) {
	t.Helper()
	b := append([]byte("\x89PNG\r\n\x1a\n"), "pixels"...)
	p := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, base64.StdEncoding.EncodeToString(b)
}

// magpie's image recognition is an MCP server of its own beside the web
// search's (JonahFu on X): an agent lists one analyze_image tool and calls
// it with a file on this computer, and the model that describes images
// answers — with a full description, or what the prompt asks.
func TestVisionMCPAnalyzesImage(t *testing.T) {
	s, u := eyed(t)
	h := lanGuard(s.Handler())
	const here = "127.0.0.1:50000"

	init := readMCP(t, visionPost(h, here, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`))
	if init.Error != nil || init.Result.ProtocolVersion != "2025-06-18" || init.Result.ServerInfo.Name != "magpie-vision" {
		t.Fatalf("initialize: %+v", init)
	}
	list := readMCP(t, visionPost(h, here, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "analyze_image" || !slices.Equal(list.Result.Tools[0].InputSchema.Required, []string{"image"}) {
		t.Fatalf("tools/list: %+v", list)
	}

	file, b64 := pngFile(t)
	m := readMCP(t, visionPost(h, here, analyzeCall(file, ""), "User-Agent", "claude-code/2.1.0"))
	if m.Result.IsError || len(m.Result.Content) != 1 || m.Result.Content[0].Text != "A red square with the word HELLO in it." {
		t.Fatalf("a file: %+v", m)
	}
	sent := u.sent("eye")
	if len(sent) != 1 || !strings.Contains(sent[0], "data:image/png;base64,"+b64) || !strings.Contains(sent[0], "Describe this image.") {
		t.Fatalf("the describer was sent %q", sent)
	}
	// asked something of it: the prompt goes with the image
	if m := readMCP(t, visionPost(h, here, analyzeCall("data:image/png;base64,aGVsbG8=", "What does the dialog say?"))); m.Result.IsError {
		t.Fatalf("a data URL with a prompt: %+v", m)
	}
	sent = u.sent("eye")
	if len(sent) != 2 || !strings.Contains(sent[1], "What does the dialog say?") || !strings.Contains(sent[1], "aGVsbG8=") || !strings.Contains(sent[1], "answer what it asks") {
		t.Fatalf("asked %q", sent[1:])
	}
	var routed *Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Kind == "vision" && r.For != nil && r.For.Agent == "claude-code" {
			routed = &r
		}
	}
	if routed == nil || !routed.For.MCP {
		t.Fatalf("route %+v", routed)
	}

	// what isn't an image it can read is the tool's error, the describer
	// asked nothing
	notImage := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notImage, []byte("hello"), 0o644)
	for _, bad := range []string{"", "shot.png", notImage, filepath.Dir(notImage), filepath.Join(t.TempDir(), "gone.png"), "data:text/plain;base64,aGk="} {
		if m := readMCP(t, visionPost(h, here, analyzeCall(bad, ""))); !m.Result.IsError {
			t.Fatalf("%q: %+v", bad, m)
		}
	}
	if n := len(u.sent("eye")); n != 2 {
		t.Fatalf("the describer was asked %d times", n)
	}
	if m := readMCP(t, visionPost(h, here, analyzeCall(filepath.Join(t.TempDir(), "x"), ""))); !strings.Contains(m.Result.Content[0].Text, "couldn't read") {
		t.Fatalf("a missing file: %+v", m)
	}
}

// Image recognition off, or no model that sees: the tool's error says
// where to pick one.
func TestVisionMCPWithNoSeer(t *testing.T) {
	_, _ = eyed(t)
	noVision(t)
	m := readMCP(t, visionPost(lanGuard(New().Handler()), "127.0.0.1:50000", analyzeCall("data:image/png;base64,aGVsbG8=", "")))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "Settings → Models → Image recognition") {
		t.Fatalf("%+v", m)
	}
	fresh(t)
	m = readMCP(t, visionPost(lanGuard(New().Handler()), "127.0.0.1:50000", analyzeCall("data:image/png;base64,aGVsbG8=", "")))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "no model that sees images") {
		t.Fatalf("%+v", m)
	}
}

// The server is a gateway route like the others: another computer reaches
// it only while magpie is shared and with a gateway key, a web page is
// turned away, and a file path is read only for an agent on this computer —
// a shared magpie's files aren't its callers'.
func TestVisionMCPGuarded(t *testing.T) {
	s, u := eyed(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	h := lanGuard(s.Handler())
	file, _ := pngFile(t)
	const there = "192.0.2.7:4000"
	if w := visionPost(h, there, analyzeCall(file, "")); w.Code != http.StatusForbidden {
		t.Fatalf("not shared: %d %s", w.Code, w.Body.String())
	}
	_, secrets := newCaller(t, "Laptop")
	if w := visionPost(h, there, analyzeCall(file, "")); w.Code != http.StatusUnauthorized {
		t.Fatalf("shared, no key: %d %s", w.Code, w.Body.String())
	}
	if w := visionPost(h, "127.0.0.1:50000", analyzeCall(file, ""), "Origin", "http://localhost:5173"); w.Code != http.StatusForbidden {
		t.Fatalf("a page on this computer: %d %s", w.Code, w.Body.String())
	}
	m := readMCP(t, visionPost(h, there, analyzeCall(file, ""), "Authorization", "Bearer "+secrets[0]))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "only for an agent on the computer magpie runs on") {
		t.Fatalf("another computer's file path: %+v", m)
	}
	// through a tunnel to this computer, too
	if m := readMCP(t, visionPost(h, "127.0.0.1:50000", analyzeCall(file, ""), "Authorization", "Bearer "+secrets[0], "X-Forwarded-For", "203.0.113.9")); !m.Result.IsError {
		t.Fatalf("a proxied file path: %+v", m)
	}
	if n := len(u.sent("eye")); n != 0 {
		t.Fatalf("the describer was asked %d times", n)
	}
	m = readMCP(t, visionPost(h, there, analyzeCall("data:image/png;base64,aGVsbG8=", ""), "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "HELLO") {
		t.Fatalf("shared, a data URL with its key: %+v", m)
	}
}

// A gateway key past its limit looks at no more images through the server.
func TestVisionMCPHeldToKeyLimit(t *testing.T) {
	s, u := eyed(t)
	budget.Forget()
	pinLimitClock(t)
	calls := limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	h := lanGuard(s.Handler())
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	m := readMCP(t, visionPost(h, "127.0.0.1:5000", analyzeCall("data:image/png;base64,aGVsbG8=", ""), "Authorization", "Bearer "+secrets[0]))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, `"Capped"`) {
		t.Fatalf("a spent key: %+v", m)
	}
	if n := len(u.sent("eye")); n != 0 {
		t.Fatalf("a spent key's image was looked at %d times", n)
	}
	// without a key, from this computer, it looks
	if m := readMCP(t, visionPost(h, "127.0.0.1:5000", analyzeCall("data:image/png;base64,aGVsbG8=", ""))); m.Result.IsError {
		t.Fatalf("keyless: %+v", m)
	}
	// plugins/budget off: a key held to a limit looks at nothing, however
	// much it has left
	withoutLimits(t)
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 100000})
	looked := len(u.sent("eye"))
	m = readMCP(t, visionPost(h, "127.0.0.1:5000", analyzeCall("data:image/png;base64,aGVsbG8=", ""), "Authorization", "Bearer "+secrets[0]))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "Key limits built-in") {
		t.Fatalf("off: %+v", m)
	}
	if n := len(u.sent("eye")); n != looked {
		t.Fatalf("off: a limited key's image was looked at")
	}
}
