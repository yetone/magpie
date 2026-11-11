package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dayPlugin is pagePlugins' page asking for usage:day too.
func dayPlugin(t *testing.T) (on, off string) {
	t.Helper()
	on, off = pagePlugins(t)
	pkg := `{"name":"magpie-usage-glance","version":"0.1.0","magpie":{"page":{"title":"Usage glance","file":"page/index.html","icon":"page/icon.svg"},"permissions":["usage:day"]}}`
	if err := os.WriteFile(filepath.Join(on, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	return on, off
}

func pagePost(h http.Handler, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return do(h, r)
}

// A page reads a day only when its package asks for usage:day, and the
// day it reads names its logos as data: pictures, a page's only kind.
func TestPluginPageReadsADay(t *testing.T) {
	on, _ := dayPlugin(t)
	h := pageMux()
	w := pagePost(h, "/api/plugin-pages/read", map[string]any{"spec": on, "read": "usage:day", "params": map[string]any{}})
	var day map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &day) != nil {
		t.Fatalf("usage:day: %d %s", w.Code, w.Body)
	}
	for _, k := range []string{"date", "today", "agents", "days", "icons", "currency", "calls", "cost"} {
		if _, ok := day[k]; !ok {
			t.Errorf("usage:day has no %s: %s", k, w.Body)
		}
	}
	if w := pagePost(h, "/api/plugin-pages/read", map[string]any{"spec": on, "read": "usage:day", "params": map[string]any{"date": "2999-01-01"}}); w.Code == 200 {
		t.Errorf("a day after today: %d %s", w.Code, w.Body)
	}
	// a from makes it a span: a week's, a month's or all time's
	for _, from := range []string{"2000-01-01", "all"} {
		w := pagePost(h, "/api/plugin-pages/read", map[string]any{"spec": on, "read": "usage:day", "params": map[string]any{"from": from}})
		var span map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &span) != nil {
			t.Fatalf("usage:day from %s: %d %s", from, w.Code, w.Body)
		}
		if f, _ := span["from"].(string); f == "" || f > day["date"].(string) {
			t.Errorf("usage:day from %s has from %q: %s", from, f, w.Body)
		}
	}
	if w := pagePost(h, "/api/plugin-pages/read", map[string]any{"spec": on, "read": "usage:day", "params": map[string]any{"from": "2999-01-01"}}); w.Code == 200 {
		t.Errorf("a from after the day: %d %s", w.Code, w.Body)
	}
	if w := pagePost(h, "/api/plugin-pages/read", map[string]any{"spec": on, "read": "usage:summary"}); w.Code != http.StatusForbidden {
		t.Errorf("a read it no longer declares: %d", w.Code)
	}

	// a -color logo is drawn as it is, a plain one in the text's colour
	if ic, ok := pageIconOf("claude-color"); !ok || ic.Mono || !strings.HasPrefix(ic.Src, "data:image/svg+xml;base64,") {
		t.Errorf("claude's logo: %+v", ic)
	}
	if ic, ok := pageIconOf("openai"); !ok || !ic.Mono {
		t.Errorf("openai's logo: %+v", ic)
	}
	if ic, ok := pageIconOf("alma"); !ok || ic.Mono || !strings.HasPrefix(ic.Src, "data:image/png;base64,") {
		t.Errorf("alma's logo: %+v", ic)
	}
	for _, name := range []string{"", "generic", "../../app.js", "https://x.example/a.svg", `C:\a`, "no-such-logo"} {
		if ic, ok := pageIconOf(name); ok {
			t.Errorf("%q gave a logo %+v", name, ic)
		}
	}
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A page's picture is put on the clipboard or saved in Downloads, never
// over a file there; what isn't a PNG of a card's size, a page magpie
// doesn't show, an action of no name and a page that asks over and over
// are refused.
func TestPluginPageSharesAPicture(t *testing.T) {
	on, off := dayPlugin(t)
	dir := t.TempDir()
	var copied [][]byte
	heldCopy, heldDir := copyImage, pageImageDir
	copyImage = func(_ context.Context, b []byte) error { copied = append(copied, b); return nil }
	pageImageDir = func() string { return dir }
	t.Cleanup(func() { copyImage, pageImageDir = heldCopy, heldDir })
	pageImages.Lock()
	clear(pageImages.at)
	pageImages.Unlock()
	h := pageMux()
	pic := testPNG(t, 4, 3)
	next := func() {
		// past the page's half second
		pageImages.Lock()
		clear(pageImages.at)
		pageImages.Unlock()
	}

	if w := pagePost(h, "/api/plugin-pages/image", map[string]any{"spec": on, "action": "copy", "png": pic}); w.Code != 200 || len(copied) != 1 || !bytes.Equal(copied[0], pic) {
		t.Fatalf("copy: %d %s", w.Code, w.Body)
	}
	next()
	var saved struct{ Path string }
	w := pagePost(h, "/api/plugin-pages/image", map[string]any{"spec": on, "action": "save", "name": "magpie 2026-11-09", "png": pic})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || filepath.Base(saved.Path) != "magpie 2026-11-09.png" {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "magpie 2026-11-09.png")); err != nil || !bytes.Equal(b, pic) {
		t.Errorf("saved: %v", err)
	}
	next()
	w = pagePost(h, "/api/plugin-pages/image", map[string]any{"spec": on, "action": "save", "name": "magpie 2026-11-09.png", "png": pic})
	if w.Code != 200 || !strings.HasSuffix(w.Body.String(), `magpie 2026-11-09-2.png"}`+"\n") {
		t.Errorf("a second save: %d %s", w.Code, w.Body)
	}
	if w := pagePost(h, "/api/plugin-pages/image", map[string]any{"spec": on, "action": "copy", "png": pic}); w.Code != http.StatusTooManyRequests {
		t.Errorf("within half a second: %d", w.Code)
	}

	for name, body := range map[string]map[string]any{
		"another page":    {"spec": off, "action": "copy", "png": pic},
		"a made-up page":  {"spec": "/nowhere", "action": "copy", "png": pic},
		"no action":       {"spec": on, "action": "open", "png": pic},
		"not a PNG":       {"spec": on, "action": "copy", "png": []byte("<svg/>")},
		"nothing":         {"spec": on, "action": "copy"},
		"too wide a side": {"spec": on, "action": "copy", "png": testPNG(t, maxPageImageSide+1, 1)},
	} {
		next()
		if w := pagePost(h, "/api/plugin-pages/image", body); w.Code == 200 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if len(copied) != 1 {
		t.Errorf("copied %d times", len(copied))
	}
	next()
	copyImage = func(context.Context, []byte) error { return errNoImageClipboard }
	if w := pagePost(h, "/api/plugin-pages/image", map[string]any{"spec": on, "action": "copy", "png": pic}); w.Code == 200 || !strings.Contains(w.Body.String(), "xclip") {
		t.Errorf("a copy that failed: %d %s", w.Code, w.Body)
	}
	// a body past the largest picture is cut off, not read whole
	next()
	big := `{"spec":` + strings.Repeat(" ", maxPageImage*4/3+128<<10) + `""}`
	r := httptest.NewRequest("POST", "/api/plugin-pages/image", strings.NewReader(big))
	if w := do(h, r); w.Code == 200 {
		t.Errorf("an oversize body: %d", w.Code)
	}
	if !pageImageAllowed("y", time.Now()) || pageImageAllowed("y", time.Now()) || !pageImageAllowed("y", time.Now().Add(time.Second)) {
		t.Error("pageImageAllowed")
	}
}

func TestPageImageName(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "magpie",
		"  ":                     "magpie",
		"magpie 2026-11-09.png":  "magpie 2026-11-09",
		"../../etc/passwd":       "etc-passwd",
		`a:b*c?"d"<e>|f\g`:       "a-b-c--d--e--f-g",
		"x\ny\x00":               "x-y",
		"..":                     "magpie",
		strings.Repeat("长", 100): strings.Repeat("长", 80),
	} {
		if got := pageImageName(in); got != want {
			t.Errorf("pageImageName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each system's own tool puts the picture on its clipboard; the file's
// path is an argument, never a part of a script, and a Linux with no tool
// says what to install.
func TestClipImageCommand(t *testing.T) {
	const file = `/tmp/it's "x".png`
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	have := map[string]bool{}
	look := func(n string) (string, error) {
		if have[n] {
			return "/usr/bin/" + n, nil
		}
		return "", errors.New("none")
	}
	name, args, _, err := clipImageCommand("darwin", getenv, look, file)
	if err != nil || name != "osascript" || args[len(args)-1] != file || strings.Contains(strings.Join(args[:len(args)-1], " "), file) {
		t.Errorf("darwin: %s %q %v", name, args, err)
	}
	name, args, extra, err := clipImageCommand("windows", getenv, look, file)
	if err != nil || name != "powershell.exe" || !strings.Contains(strings.Join(args, " "), "-STA") || len(extra) != 1 || extra[0] != "MAGPIE_CLIP_PNG="+file || strings.Contains(strings.Join(args, " "), file) {
		t.Errorf("windows: %s %q %q %v", name, args, extra, err)
	}
	if _, _, _, err := clipImageCommand("linux", getenv, look, file); !errors.Is(err, errNoImageClipboard) {
		t.Errorf("linux with no tool: %v", err)
	}
	have["xclip"] = true
	name, args, _, err = clipImageCommand("linux", getenv, look, file)
	if err != nil || name != "/usr/bin/xclip" || args[len(args)-1] != file {
		t.Errorf("linux xclip: %s %q %v", name, args, err)
	}
	// Wayland's own tool first, but xclip when it has none
	env["WAYLAND_DISPLAY"] = "wayland-0"
	if name, _, _, _ := clipImageCommand("linux", getenv, look, file); name != "/usr/bin/xclip" {
		t.Errorf("wayland without wl-copy: %s", name)
	}
	have["wl-copy"] = true
	name, args, _, err = clipImageCommand("linux", getenv, look, file)
	if err != nil || name != "sh" || args[2] != "/usr/bin/wl-copy" || args[3] != file || strings.Contains(args[1], file) {
		t.Errorf("wayland: %s %q %v", name, args, err)
	}
}
