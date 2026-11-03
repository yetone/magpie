package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/browserext"
)

// Every action is one of the extension's tools, and the one schema names
// every argument the eighteen take, so none is lost in the merge.
func TestBrowserActionsCoverTheTools(t *testing.T) {
	src, err := browserext.File("background.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(browserActionOrder) != len(browserActions) {
		t.Fatalf("%d actions listed, %d known", len(browserActionOrder), len(browserActions))
	}
	used := map[string]bool{}
	for _, name := range browserActionOrder {
		a, ok := browserActions[name]
		if !ok {
			t.Fatalf("%s is listed but unknown", name)
		}
		if a.tool == browserRecordTool {
			continue // the gateway's own
		}
		if !strings.Contains(string(src), "  async "+a.tool+"(session, args) {") {
			t.Errorf("%s's %s isn't in background.js", name, a.tool)
		}
		used[a.tool] = true
	}
	props := browserOneTool()["inputSchema"].(map[string]any)["properties"].(map[string]any)
	desc := browserOneTool()["description"].(string)
	for _, tl := range browserTools() {
		m := tl.(map[string]any)
		name := m["name"].(string)
		if !used[name] {
			t.Errorf("no action carries out %s", name)
		}
		for arg := range m["inputSchema"].(map[string]any)["properties"].(map[string]any) {
			if arg == "action" || arg == "go" { // they are actions of their own
				continue
			}
			if props[arg] == nil {
				t.Errorf("%s's %s isn't in browser's schema", name, arg)
			}
		}
	}
	for _, name := range browserActionOrder {
		if !strings.Contains(desc, name) {
			t.Errorf("browser's description doesn't name %s", name)
		}
	}
}

// The point of one tool: its listing is a fraction of the eighteen's.
func TestBrowserToolListIsSmall(t *testing.T) {
	one, _ := json.Marshal([]any{browserOneTool()})
	all, _ := json.Marshal(browserTools())
	if len(one)*2 > len(all) {
		t.Fatalf("browser's listing is %d bytes, the eighteen's %d: no saving", len(one), len(all))
	}
	t.Logf("browser: %d bytes, the eighteen: %d", len(one), len(all))
}

// A browser call reaches the extension as its own tool with the arguments
// the action stands for; its answer names actions, not the eighteen; and
// screenshot: true adds a small picture of the window.
func TestBrowserToolCallsTheExtensionsTool(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	type got struct {
		tool string
		args map[string]any
	}
	calls := make(chan got, 8)
	big := image.NewRGBA(image.Rect(0, 0, 2880, 1800)) // a Retina window
	for i := range big.Pix {
		big.Pix[i] = 0xff
	}
	big.Set(10, 10, color.Black)
	var raw bytes.Buffer
	png.Encode(&raw, big)
	fakeExtension(t, srv.URL, func(_, _, tool string, args map[string]any) any {
		calls <- got{tool, args}
		if tool == "browser_screenshot" {
			return map[string]any{"content": []any{map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(raw.Bytes()), "mimeType": "image/png"}}}
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "Opened t1. Answer it with browser_dialog; call browser_tabs with action new."}}}
	})
	browserConnected(t, s)
	h := lanGuard(s.Handler())

	m := readImageReply(t, browserPost(h, "127.0.0.1:5000", imageToolCall("browser", map[string]any{"action": "tab_new", "url": "example.com"})))
	c := <-calls
	if c.tool != "browser_tabs" || c.args["action"] != "new" || c.args["url"] != "example.com" || m.Result.IsError {
		t.Fatalf("tab_new reached the extension as %+v: %+v", c, m)
	}
	if say := m.Result.Content[0].Text; strings.Contains(say, "browser_") || !strings.Contains(say, "the dialog action") || !strings.Contains(say, "the tab_new action") {
		t.Fatalf("the answer names the eighteen: %q", say)
	}
	if len(m.Result.Content) != 1 {
		t.Fatalf("a picture nobody asked for: %+v", m.Result.Content)
	}

	m = readImageReply(t, browserPost(h, "127.0.0.1:5000", imageToolCall("browser", map[string]any{"action": "back", "screenshot": true})))
	c = <-calls
	if c.tool != "browser_navigate" || c.args["go"] != "back" || c.args["screenshot"] != nil || c.args["action"] != nil {
		t.Fatalf("back reached the extension as %+v", c)
	}
	if c = <-calls; c.tool != "browser_screenshot" {
		t.Fatalf("then %+v, not a screenshot", c)
	}
	if len(m.Result.Content) != 2 || m.Result.Content[1].Type != "image" || m.Result.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("no picture with the answer: %+v", m.Result.Content)
	}
	b, _ := base64.StdEncoding.DecodeString(m.Result.Content[1].Data)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != browserShotWidth || cfg.Height != 640 {
		t.Fatalf("the picture is %+v (%v), want %dx640", cfg, err, browserShotWidth)
	}

	m = readImageReply(t, browserPost(h, "127.0.0.1:5000", imageToolCall("browser", map[string]any{"action": "format_disk"})))
	if !m.Result.IsError || !strings.HasPrefix(m.Result.Content[0].Text, "BAD_ARGS: action is one of tabs,") {
		t.Fatalf("an unknown action: %+v", m)
	}
	// a client that listed the eighteen before still calls them
	readImageReply(t, browserPost(h, "127.0.0.1:5000", imageToolCall("browser_snapshot", nil)))
	if c = <-calls; c.tool != "browser_snapshot" {
		t.Fatalf("browser_snapshot reached the extension as %+v", c)
	}
	select {
	case c = <-calls:
		t.Fatalf("an extra call %+v", c)
	default:
	}
}
