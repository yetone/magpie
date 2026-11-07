package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/browserext"
)

// The fixture page the real browser works in, served from this test.
const browserFixture = `<!doctype html>
<title>Fixture</title>
<h1>Magpie fixture</h1>
<input aria-label="Name" id="name">
<button onclick="out.textContent = 'clicked ' + document.getElementById('name').value">Go</button>
<select aria-label="Colour" onchange="out.textContent = 'colour ' + this.value"><option value="r">Red</option><option value="g">Green</option></select>
<button onmouseover="out.textContent = 'hovered'">Hover me</button>
<input type="file" aria-label="Upload" onchange="out.textContent = 'file ' + this.files[0].name">
<button id="src">Drag me</button> <button id="dst">Drop here</button>
<button onclick="alert('hello')">Alert</button>
<button onclick="out.textContent = confirm('sure?') ? 'confirmed' : 'declined'">Confirm</button>
<button onclick="console.error('boom from the page')">Log</button>
<button onclick="setTimeout(() => out.textContent = 'later', 400)">Later</button>
<a href="/two" target="_blank">Open two</a>
<div id="out" role="status"></div>
<div style="height: 4000px"></div>
<p>Bottom of the page</p>
<script>
  const out = document.getElementById('out');
  let down = null;
  document.addEventListener('mousedown', (e) => { down = e.target.id; });
  document.addEventListener('mouseup', (e) => { if (down === 'src' && e.target.id === 'dst') out.textContent = 'dropped'; down = null; });
</script>`

// A page with a frame from another site (SRC: 127.0.0.1 and localhost are
// two sites, so Chrome runs it in a process of its own) below the fold,
// and buttons that change nothing, and change the page twice a moment
// apart with no request.
const framesFixture = `<!doctype html>
<title>Frames</title>
<h1>Frames fixture</h1>
<button>Does nothing</button>
<button onclick="out.textContent = 'working'; setTimeout(() => out.textContent = 'done', 250)">Works a while</button>
<div id="out" role="status"></div>
<div style="height: 1500px"></div>
<iframe title="Checkout" src="SRC" style="width: 420px; height: 160px; margin-left: 30px"></iframe>
<script>const out = document.getElementById('out');</script>`

// what the frame from the other site shows
const innerFixture = `<!doctype html>
<title>Checkout</title>
<input aria-label="Card" id="card">
<button onclick="said.textContent = 'paid ' + card.value">Pay</button>
<div id="said" role="status"></div>
<script>const said = document.getElementById('said'), card = document.getElementById('card');</script>`

// Frames from another site and the kinds of failure, in a real Chromium:
// the frame's elements are in the snapshot with refs, typed into, clicked
// and measured where they are on the page, below the fold; an action
// answers once the page has stopped changing, not when its requests end;
// a click that changes nothing says NO_EFFECT; and each failure starts
// with its kind.
func TestBrowserFramesAndFailuresInARealBrowser(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "e2e-frames")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, innerFixture)
	}))
	t.Cleanup(other.Close)
	inner := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/inner"
	ref := func(page, role, name string) string {
		t.Helper()
		m := regexp.MustCompile(`- ` + role + ` "` + regexp.QuoteMeta(name) + `"(?: \[[a-z=, ]+\])? \[ref=(e\d+)\]`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no %s %q with a ref in:\n%s", role, name, page)
		}
		return m[1]
	}
	failed := func(tool string, args map[string]any, code string) string {
		t.Helper()
		m := call(tool, args)
		out := m.Result.Content[0].Text
		if !m.Result.IsError || !strings.HasPrefix(out, code+": ") {
			t.Fatalf("%s %v answered (error %v):\n%s\nwant it to fail %s", tool, args, m.Result.IsError, out, code)
		}
		return out
	}

	page := ok("browser_navigate", map[string]any{"url": site.URL + "/frames?src=" + url.QueryEscape(inner)}, "Frames fixture")
	// the frame loads after the page: read until it is there
	for end := time.Now().Add(10 * time.Second); !strings.Contains(page, `button "Pay"`) && time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		page = ok("browser_snapshot", nil)
	}
	if strings.Contains(page, "doesn't let Magpie Browser read") {
		t.Fatalf("the frame from the other site isn't read:\n%s", page)
	}
	ok("browser_type", map[string]any{"ref": ref(page, "textbox", "Card"), "text": "4242"}, "Typed into")
	pay := ref(page, "button", "Pay")
	ok("browser_click", map[string]any{"ref": pay}, "Clicked", "paid 4242")
	ok("browser_evaluate", map[string]any{"ref": pay, "script": "(el) => [el.ownerDocument.getElementById('said').textContent, location.host]"}, "paid 4242", strings.TrimPrefix(strings.TrimSuffix(inner, "/inner"), "http://"))
	png := call("browser_screenshot", map[string]any{"ref": pay, "png": true})
	if b, _ := base64.StdEncoding.DecodeString(png.Result.Content[0].Data); png.Result.IsError || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("the frame's button has no screenshot: %+v", png.Result.Content[len(png.Result.Content)-1])
	}
	ok("browser_snapshot", map[string]any{"ref": pay}, `button "Pay"`)

	// settled when the page stops changing: the second change comes
	// 250ms after the first, with no request between
	ok("browser_scroll", map[string]any{"direction": "up", "amount": 5000})
	page = ok("browser_snapshot", nil)
	if out := ok("browser_click", map[string]any{"ref": ref(page, "button", "Works a while")}, "done"); strings.Contains(out, "NO_EFFECT") {
		t.Fatalf("a click that changed the page says NO_EFFECT:\n%s", out)
	}
	ok("browser_click", map[string]any{"ref": ref(page, "button", "Does nothing")}, "NO_EFFECT")

	failed("browser_click", map[string]any{"ref": "nope"}, "BAD_ARGS")
	failed("browser_click", map[string]any{"ref": "e9999"}, "STALE_REF")
	failed("browser_click", nil, "BAD_ARGS")
	failed("browser_wait", map[string]any{"text": "never shown", "timeout": 1}, "WAIT_TIMEOUT")
	failed("browser_evaluate", map[string]any{"script": "(() => { throw new Error('nope') })()"}, "SCRIPT_ERROR")
	failed("browser_navigate", map[string]any{"url": site.URL + "/refused"}, "SITE_REFUSED")
	failed("browser_navigate", map[string]any{"url": "http://127.0.0.1:1/"}, "LOAD_FAILED")
}

// Magpie Browser in a real Chromium: the extension magpie writes, loaded
// as a user loads it, connects to the gateway, and every tool an agent
// calls is carried out in a real tab. Set MAGPIE_BROWSER_CHROME to a
// Chromium that takes --load-extension (Chrome for Testing, as Playwright
// has it) to run it.
func TestBrowserToolsInARealBrowser(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "e2e")
	ref := func(page, role, name string) string {
		t.Helper()
		m := regexp.MustCompile(`- ` + role + ` "` + regexp.QuoteMeta(name) + `"(?: \[[a-z=, ]+\])? \[ref=(e\d+)\]`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no %s %q with a ref in:\n%s", role, name, page)
		}
		return m[1]
	}
	shows := func(want string) {
		t.Helper()
		ok("browser_evaluate", map[string]any{"script": "() => document.getElementById('out').textContent"}, want)
	}

	page := ok("browser_tabs", map[string]any{"action": "new", "url": site.URL}, "Opened t", "Tab t", "Fixture", "Magpie fixture")
	ok("browser_tabs", nil, "* t", site.URL)
	page = ok("browser_snapshot", nil, `heading "Magpie fixture"`)
	ok("browser_snapshot", map[string]any{"interactive": true}, `button "Go"`)

	ok("browser_type", map[string]any{"ref": ref(page, "textbox", "Name"), "text": "Ada"}, "Typed into")
	ok("browser_click", map[string]any{"ref": ref(page, "button", "Go")}, "Clicked", "clicked Ada")
	ok("browser_press", map[string]any{"key": "Backspace", "ref": ref(page, "textbox", "Name")}, "Pressed Backspace")
	ok("browser_click", map[string]any{"ref": ref(page, "button", "Go")}, "clicked Ad")
	ok("browser_type", map[string]any{"ref": ref(page, "textbox", "Name"), "text": "Bo", "slowly": true, "clear": false})
	ok("browser_click", map[string]any{"ref": ref(page, "button", "Go")}, "clicked AdBo")

	ok("browser_select", map[string]any{"ref": ref(page, "combobox", "Colour"), "values": []string{"Green"}}, `Chose "Green"`)
	shows("colour g")
	ok("browser_hover", map[string]any{"ref": ref(page, "button", "Hover me")}, "Hovering")
	shows("hovered")
	ok("browser_drag", map[string]any{"from": ref(page, "button", "Drag me"), "to": ref(page, "button", "Drop here")}, "Dragged")
	shows("dropped")

	file := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(file, []byte("hi"), 0o600)
	ok("browser_upload", map[string]any{"ref": ref(page, `\w+`, "Upload"), "paths": []string{file}}, "Chose 1 file")
	shows("file notes.txt")

	ok("browser_click", map[string]any{"ref": ref(page, "button", "Later")})
	ok("browser_wait", map[string]any{"text": "later"}, "Done waiting")
	ok("browser_wait", map[string]any{"seconds": 0.2}, "Waited")

	ok("browser_click", map[string]any{"ref": ref(page, "button", "Alert")}, `alert dialog is open: "hello"`)
	ok("browser_dialog", nil, `Accepted the alert dialog ("hello")`)
	ok("browser_dialog", map[string]any{"accept": false}, "next one", "dismissed")
	ok("browser_click", map[string]any{"ref": ref(page, "button", "Confirm")}, "was dismissed as asked")
	shows("declined")

	ok("browser_click", map[string]any{"ref": ref(page, "button", "Log")})
	ok("browser_console", map[string]any{"level": "errors"}, "boom from the page")

	ok("browser_scroll", map[string]any{"direction": "down", "amount": 600}, "Scrolled down 600px")
	ok("browser_evaluate", map[string]any{"script": "window.scrollY"}, "600")
	// An async function is called and awaited however it is spaced, and
	// an expression that calls one is awaited, not called again.
	for _, script := range []string{
		"async()=>{await new Promise(r=>setTimeout(r,50));return 'awaited'}",
		"async () => { await null; return 'awaited' }",
		"async function(){ await null; return 'awaited' }",
		"(async()=>{await new Promise(r=>setTimeout(r,50));return 'awaited'})()",
		"Promise.resolve('awaited')",
	} {
		ok("browser_evaluate", map[string]any{"script": script}, `"awaited"`)
	}
	ok("browser_text", nil, "Magpie fixture", "Bottom of the page")

	shot := call("browser_screenshot", nil)
	if shot.Result.IsError || shot.Result.Content[0].Type != "image" || shot.Result.Content[0].MimeType != "image/jpeg" {
		t.Fatalf("screenshot: %+v", shot.Result)
	}
	if b, err := base64.StdEncoding.DecodeString(shot.Result.Content[0].Data); err != nil || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
		t.Fatalf("the screenshot isn't a JPEG: %v", err)
	}
	png := call("browser_screenshot", map[string]any{"ref": ref(page, "button", "Go"), "png": true})
	if b, _ := base64.StdEncoding.DecodeString(png.Result.Content[0].Data); png.Result.IsError || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("an element's screenshot isn't a PNG: %+v", png.Result.Content[len(png.Result.Content)-1])
	}

	ok("browser_navigate", map[string]any{"url": site.URL + "/two"}, "Opened", "Page two")
	ok("browser_navigate", map[string]any{"go": "back"}, "Went back", "Magpie fixture")
	page = ok("browser_navigate", map[string]any{"go": "reload"}, "Reloaded", "Magpie fixture")

	// a link that opens a tab: the session goes on in it
	two := ok("browser_click", map[string]any{"ref": ref(page, "link", "Open two")}, "opened a new tab", "Page two")
	id := regexp.MustCompile(`new tab, (t\d+)`).FindStringSubmatch(two)[1]
	ok("browser_tabs", map[string]any{"action": "close", "tab": id}, "Closed "+id)
	if m := call("browser_snapshot", map[string]any{"tab": id}); !m.Result.IsError {
		t.Fatalf("a closed tab still answers: %+v", m.Result)
	}
}

// realBrowser starts a Chromium with the extension magpie writes loaded
// in a profile of its own, pointed at a gateway of the test's, and waits
// for it to connect; site serves browserFixture (and /two).
func realBrowser(t *testing.T) (srv, site *httptest.Server) {
	t.Helper()
	srv, site, _ = realBrowserFrom(t, func(srv *httptest.Server) string {
		ext := t.TempDir()
		if err := browserext.Write(ext, srv.URL); err != nil {
			t.Fatal(err)
		}
		return ext
	})
	return srv, site
}

// realBrowserFrom is realBrowser with the extension loaded from the folder
// ext writes, and the gateway's Server too.
func realBrowserFrom(t *testing.T, ext func(*httptest.Server) string) (srv, site *httptest.Server, s *Server) {
	t.Helper()
	chrome := os.Getenv("MAGPIE_BROWSER_CHROME")
	if chrome == "" {
		t.Skip("MAGPIE_BROWSER_CHROME names no Chromium")
	}
	fresh(t)

	s = New()
	srv = httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/two":
			io.WriteString(w, "<!doctype html><title>Two</title><h1>Page two</h1>")
			return
		case "/frames":
			io.WriteString(w, strings.Replace(framesFixture, "SRC", html.EscapeString(r.URL.Query().Get("src")), 1))
			return
		case "/watch":
			// the user's own page: its title counts the times it was put
			// behind another tab
			io.WriteString(w, `<!doctype html><title>Watch 0</title><h1>The user's page</h1><script>let hid = 0; document.addEventListener("visibilitychange", () => { if (document.hidden) document.title = "Watch " + ++hid; });</script>`)
			return
		case "/focus":
			// a page that says in its title whether it thinks it has focus
			io.WriteString(w, `<!doctype html><title>Focus ?</title><h1>Focus</h1><script>setInterval(() => { document.title = "Focus " + (document.hasFocus() ? "on" : "off"); }, 200);</script>`)
			return
		case "/human":
			io.WriteString(w, humanFixture)
			return
		case "/refused":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<!doctype html><title>Denied</title><h1>Access denied</h1>")
			return
		}
		io.WriteString(w, browserFixture)
	}))
	t.Cleanup(site.Close)

	dir := ext(srv)
	profile := t.TempDir()
	head := "--headless=new"
	if os.Getenv("MAGPIE_BROWSER_HEADFUL") != "" {
		// a window on the screen: only then does Chrome put a tab behind
		// another one out of sight (headless, every tab is visible)
		head = "--window-size=1000,700"
	}
	cmd := exec.Command(chrome, head, "--user-data-dir="+profile,
		"--disable-extensions-except="+dir, "--load-extension="+dir,
		"--no-first-run", "--no-default-browser-check", "--disable-gpu", "--remote-debugging-port=0",
		// with the test's own HOME there is no keychain (macOS) or keyring
		// (Linux), and Chrome waits on it before the extension's storage
		// answers
		"--use-mock-keychain", "--password-store=basic",
		// a site's own name, not a developer's host: shop.example is the
		// test's site
		"--host-resolver-rules=MAP shop.example 127.0.0.1",
		// the user's own page, which the extension never attaches to
		site.URL+"/watch")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer cmd.Wait()
		defer cmd.Process.Kill()
		if t.Failed() {
			t.Logf("chromium said:\n%s", logs.String())
			// what it runs: is the extension's worker among them
			if port, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
				if resp, err := http.Get("http://127.0.0.1:" + strings.SplitN(string(port), "\n", 2)[0] + "/json/list"); err == nil {
					b, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					t.Logf("its targets: %s", b)
				}
			}
		}
	})
	for deadline := time.Now().Add(60 * time.Second); s.browser.current() == nil; {
		if time.Now().After(deadline) {
			t.Fatal("the extension never connected")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return srv, site, s
}

// browserCaller calls the browser's tools as the agent session session:
// call answers with the reply, ok with the text of one that should work.
func browserCaller(t *testing.T, srv *httptest.Server, session string) (call func(string, map[string]any) imageReply, ok func(string, map[string]any, ...string) string) {
	call = func(tool string, args map[string]any) imageReply {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+BrowserMCPPath, strings.NewReader(imageToolCall(tool, args)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(BrowserSessionHeader, session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var m imageReply
		if resp.StatusCode != 200 || json.Unmarshal(b, &m) != nil || m.Error != nil || len(m.Result.Content) == 0 {
			t.Fatalf("%s: %d %s", tool, resp.StatusCode, b)
		}
		return m
	}
	// ok calls a tool that should work and answers with its text
	ok = func(tool string, args map[string]any, want ...string) string {
		t.Helper()
		m := call(tool, args)
		var said []string
		for _, c := range m.Result.Content {
			said = append(said, c.Text)
		}
		out := strings.Join(said, "\n")
		if m.Result.IsError {
			t.Fatalf("%s %v failed: %s", tool, args, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Fatalf("%s %v doesn't say %q:\n%s", tool, args, w, out)
			}
		}
		return out
	}
	return call, ok
}

// Two agent sessions at once in a real Chromium (packinggoodsllc_07668 on
// Discord: two agents behind one router took one tab): each works in a
// new tab of its own; each opens its tabs in a group of its own
// ("Magpie", "Magpie 2"), and each sees the other's tab marked ~.
func TestBrowserSessionsApartInARealBrowser(t *testing.T) {
	srv, site := realBrowser(t)
	_, a := browserCaller(t, srv, "agent-a")
	_, b := browserCaller(t, srv, "agent-b")
	tabOf := func(page string) string {
		t.Helper()
		m := regexp.MustCompile(`Tab (t\d+):`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no tab in:\n%s", page)
		}
		return m[1]
	}
	ta := tabOf(a("browser_navigate", map[string]any{"url": site.URL}, "Magpie fixture"))
	tb := tabOf(b("browser_navigate", map[string]any{"url": site.URL + "/two"}, "Page two"))
	if ta == tb {
		t.Fatalf("both sessions work in %s", ta)
	}
	if got := tabOf(a("browser_snapshot", nil)); got != ta {
		t.Fatalf("a went from %s to %s", ta, got)
	}
	a("browser_evaluate", map[string]any{"script": "document.title"}, "Fixture")
	b("browser_evaluate", map[string]any{"script": "document.title"}, "Two")
	if list := a("browser_tabs", nil); !regexp.MustCompile(`(?m)^\* `+ta+` `).MatchString(list) || !regexp.MustCompile(`(?m)^~ `+tb+` `).MatchString(list) {
		t.Fatalf("a's list doesn't mark its tab %s * and b's %s ~:\n%s", ta, tb, list)
	}

	// a tab each opens goes in its own group, and calls go on in it
	a("browser_tabs", map[string]any{"action": "new", "url": site.URL})
	b("browser_tabs", map[string]any{"action": "new", "url": site.URL + "/two"})
	// the group of the tab a list marks mark with the page title
	groupOf := func(list, mark, title string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(mark) + ` t\d+ \[(Magpie(?: \d+)?)\] "` + title + `"`).FindStringSubmatch(list)
		if m == nil {
			t.Fatalf("no %s tab %q in a Magpie group:\n%s", mark, title, list)
		}
		return m[1]
	}
	list := a("browser_tabs", nil)
	ga, gb := groupOf(list, "*", "Fixture"), groupOf(list, "~", "Two")
	if ga == gb {
		t.Fatalf("both sessions' tabs are in %s:\n%s", ga, list)
	}
	list = b("browser_tabs", nil)
	if groupOf(list, "*", "Two") != gb || groupOf(list, "~", "Fixture") != ga {
		t.Fatalf("b's list doesn't have its tab in %s and a's in %s:\n%s", gb, ga, list)
	}
}

// The one browser tool in a real Chromium: actions open, act and go back,
// and screenshot: true brings a picture of the window no wider than
// browserShotWidth.
func TestBrowserToolActionsInARealBrowser(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "e2e-one")
	ok("browser", map[string]any{"action": "tab_new", "url": site.URL + "/frames?src=about:blank"}, "Opened t", "Frames fixture")
	m := call("browser", map[string]any{"action": "snapshot", "interactive": true, "screenshot": true})
	if m.Result.IsError || len(m.Result.Content) != 2 || m.Result.Content[1].Type != "image" {
		t.Fatalf("snapshot with screenshot: %+v", m.Result.Content)
	}
	b, _ := base64.StdEncoding.DecodeString(m.Result.Content[1].Data)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width == 0 || cfg.Width > browserShotWidth {
		t.Fatalf("the picture: %+v %v", cfg, err)
	}
	page := m.Result.Content[0].Text
	r := regexp.MustCompile(`button "Works a while" \[ref=(e\d+)\]`).FindStringSubmatch(page)
	if r == nil {
		t.Fatalf("no button in:\n%s", page)
	}
	ok("browser", map[string]any{"action": "click", "ref": r[1]}, "done")
	ok("browser", map[string]any{"action": "navigate", "url": site.URL + "/two"})
	if out := ok("browser", map[string]any{"action": "back"}, "Frames fixture"); strings.Contains(out, "browser_") {
		t.Fatalf("an answer names the eighteen:\n%s", out)
	}
	ok("browser", map[string]any{"action": "tab_close"}, "Closed t")
}

// Agents leave the user's tab alone (the owner, 11-03: Magpie Browser
// "老是会代替掉用户正在浏览的网页"; 11-04: "你到底彻底修复了没啊"): a call
// that names no tab works in a new one, behind the tab the user is looking
// at, in the session's Magpie group; a link that opens a tab, tab_new,
// screenshots and a recording all happen behind it too. The user's page
// counts each time it is put behind another tab, so a tab brought forward
// for a moment and given back fails as well as one left in front.
func TestBrowserLeavesTheUsersTabAlone(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "quiet")
	users := regexp.MustCompile(`(?m)^\+ (t\d+) "Watch 0" .*/watch$`).FindStringSubmatch(ok("browser_tabs", nil))
	if users == nil {
		t.Fatal("no shown user's page to start with")
	}
	shown := func(when string) {
		t.Helper()
		list := ok("browser_tabs", nil)
		if !regexp.MustCompile(`(?m)^\+ ` + users[1] + ` "Watch 0" .*/watch$`).MatchString(list) {
			t.Fatalf("%s, the user's tab %s isn't shown, unchanged and never put behind (Watch N counts the times):\n%s", when, users[1], list)
		}
	}
	shown("to start with")
	if os.Getenv("MAGPIE_BROWSER_HEADFUL") == "" {
		// headless, every tab is visible, so a tab brought forward for a
		// moment and given back can't be told
		t.Log("headless: only where the user's tab ends up is checked; MAGPIE_BROWSER_HEADFUL=1 catches a tab brought forward for a moment too")
	}
	page := ok("browser_navigate", map[string]any{"url": site.URL}, "Magpie fixture")
	if strings.Contains(page, users[1]+":") {
		t.Fatalf("navigated the user's tab:\n%s", page)
	}
	shown("after a navigate")
	if list := ok("browser_tabs", nil); !regexp.MustCompile(`(?m)^\* t\d+ \[Magpie\] "Fixture"`).MatchString(list) {
		t.Fatalf("the agent's tab isn't in its Magpie group:\n%s", list)
	}
	link := regexp.MustCompile(`- link "Open two"(?: \[[a-z=, ]+\])? \[ref=(e\d+)\]`).FindStringSubmatch(page)
	if link == nil {
		t.Fatalf("no link Open two in:\n%s", page)
	}
	ok("browser_click", map[string]any{"ref": link[1]}, "opened a new tab", "Page two")
	shown("after a link opened a tab")
	if list := ok("browser_tabs", nil); !regexp.MustCompile(`(?m)^\* t\d+ \[Magpie\] "Two"`).MatchString(list) {
		t.Fatalf("the tab the link opened isn't the agent's, in its group:\n%s", list)
	}
	ok("browser_tabs", map[string]any{"action": "new", "url": site.URL + "/two"}, "Page two")
	shown("after tab new")
	if m := call("browser_screenshot", nil); m.Result.IsError || m.Result.Content[0].Type != "image" {
		t.Fatalf("screenshot: %+v", m.Result.Content)
	}
	shown("after a screenshot")
	ok("browser", map[string]any{"action": "record_start"}, "Recording")
	ok("browser", map[string]any{"action": "tab_new", "url": site.URL}, "Opened t")
	ok("browser", map[string]any{"action": "navigate", "url": site.URL + "/two", "screenshot": true}, "Page two")
	ok("browser", map[string]any{"action": "record_stop"}, "Recorded 2 frames")
	shown("after a recording")
}

// A tab is let go about 20s after the last call in it ends, as Claude in
// Chrome does: a page behind the user's never goes on for minutes with
// its focus emulated (Discord took that for a bot).
func TestBrowserLetsATabGoAfterItsCalls(t *testing.T) {
	if os.Getenv("MAGPIE_BROWSER_HEADFUL") == "" {
		t.Skip("headless, every tab has focus: MAGPIE_BROWSER_HEADFUL=1 runs it")
	}
	srv, site := realBrowser(t)
	_, ok := browserCaller(t, srv, "letgo")
	ok("browser_navigate", map[string]any{"url": site.URL + "/focus"}, "Focus")
	title := func() string {
		m := regexp.MustCompile(`(?m)^. t\d+ \[Magpie\] "(Focus [^"]*)"`).FindStringSubmatch(ok("browser_tabs", nil))
		if m == nil {
			t.Fatal("no Focus tab in the list")
		}
		return m[1]
	}
	// a timer behind the user's tab runs once a second
	until := func(want string, within time.Duration) (string, time.Duration) {
		start := time.Now()
		for {
			got := title()
			if got == want || time.Since(start) > within {
				return got, time.Since(start)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if got, _ := until("Focus on", 5*time.Second); got != "Focus on" {
		t.Fatalf("while the agent works in it, the tab behind the user's has %q, not focus", got)
	}
	start := time.Now()
	for title() != "Focus off" {
		// tabs lists, and touches no tab: it doesn't hold this one
		if time.Since(start) > 45*time.Second {
			t.Fatalf("the tab still has emulated focus %s after the last call in it", time.Since(start).Round(time.Second))
		}
		time.Sleep(time.Second)
	}
	if d := time.Since(start); d < 15*time.Second {
		t.Fatalf("let go after %s, before the 20s a next call may come in", d.Round(time.Second))
	}
	// a call in it takes it again
	ok("browser_snapshot", nil, "Focus")
	if got, _ := until("Focus on", 5*time.Second); got != "Focus on" {
		t.Fatalf("a call after it was let go left %q", got)
	}
}

// An older extension, loaded from the folder magpie writes, has the folder
// written with this magpie's when it connects, for the browser to read
// when it starts again.
func TestBrowserOlderExtensionsFolderWritten(t *testing.T) {
	var man string
	realBrowserFrom(t, func(srv *httptest.Server) string {
		t.Setenv("MAGPIE_ADDR", srv.Listener.Addr().String())
		dir := browserext.Dir()
		if err := browserext.Write(dir, srv.URL); err != nil {
			t.Fatal(err)
		}
		man = filepath.Join(dir, "manifest.json")
		b, _ := os.ReadFile(man)
		old := bytes.Replace(b, []byte(`"version": "`+browserext.Version()+`"`), []byte(`"version": "0.0.1"`), 1)
		if bytes.Equal(old, b) {
			t.Fatalf("no version in %s", b)
		}
		os.WriteFile(man, old, 0o644)
		return dir
	})
	for deadline := time.Now().Add(10 * time.Second); ; {
		if b, _ := os.ReadFile(man); bytes.Contains(b, []byte(`"version": "`+browserext.Version()+`"`)) {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(man)
			t.Fatalf("the folder still has:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A shop's page that watches for an automated browser the ways bot checks
// do: its own getComputedStyle,
// querySelectorAll, elementFromPoint and focus counting the calls a script
// in its world makes, and every mouse, key, composition and wheel event.
const humanFixture = `<!doctype html>
<title>Shop</title>
<h1>Shop</h1>
<button id="buy" style="width: 121px; height: 41px">Buy</button>
<button onclick="console.log('bought')">Log</button>
<input aria-label="Search" id="search">
<textarea aria-label="Message" id="msg"></textarea>
<div contenteditable="true" role="textbox" aria-label="Chat" id="chat" style="min-height: 2em; border: 1px solid"></div>
<div style="height: 4000px"></div>
<script>
  const seen = { calls: [], moves: 0, movesBeforeClick: -1, click: null, keys: [], shift: 0, composed: [], wheels: 0 };
  window.seen = seen;
  const spy = (o, k, name) => { const f = o[k]; o[k] = function (...a) { seen.calls.push(name); return f.apply(this, a); }; };
  spy(window, "getComputedStyle", "getComputedStyle");
  spy(Document.prototype, "querySelectorAll", "querySelectorAll");
  spy(Element.prototype, "querySelectorAll", "querySelectorAll");
  spy(Document.prototype, "elementFromPoint", "elementFromPoint");
  spy(HTMLElement.prototype, "focus", "focus");
  addEventListener("mousemove", () => seen.moves++, true);
  buy.addEventListener("click", (ev) => {
    const r = buy.getBoundingClientRect();
    seen.movesBeforeClick = seen.moves;
    seen.click = { trusted: ev.isTrusted, dx: ev.clientX - (r.left + r.width / 2), dy: ev.clientY - (r.top + r.height / 2) };
  });
  // a mouse button held down presses at 0.5, as a real mouse's does
  buy.addEventListener("pointerdown", (ev) => { seen.pressure = ev.pressure; });
  addEventListener("keydown", (ev) => { seen.keys.push([ev.key, performance.now(), ev.isTrusted]); if (ev.key === "Shift") { seen.shift++; if (seen.shiftAt === undefined || ev.location !== 1) seen.shiftAt = ev.location; } }, true);
  addEventListener("compositionend", (ev) => seen.composed.push(ev.data), true);
  // a chat box sends on Enter, as Discord's does
  chat.addEventListener("keydown", (ev) => { if (ev.key === "Enter" && !ev.shiftKey) { ev.preventDefault(); seen.sent = chat.innerText; } });
  addEventListener("wheel", () => seen.wheels++, { capture: true, passive: true });
</script>`

// The extension drives a site the way a person does, so the bot checks
// sites like Discord run don't find it (the owner, 11-05: Discord flagged
// the account under Magpie Browser, never under Claude's extension): CDP's
// Runtime domain, which a page can find by what the console does with its
// values, stays off until console is asked for on a site's own host; its reads and helpers run in a world of its own, so the page's
// own functions never see a call; a click comes after the mouse has moved
// there, trusted and off the exact middle; text is typed a key at a time
// at uneven gaps, capitals with Shift, Chinese through an IME's
// composition; a scroll is several wheel notches; and naming the user's
// own tab opens its page in a tab of the agent's, leaving theirs alone.
func TestBrowserActsLikeAPerson(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "person")
	shop := strings.Replace(site.URL, "127.0.0.1", "shop.example", 1) + "/human"
	page := ok("browser_tabs", map[string]any{"action": "new", "url": shop}, "Opened t", "Shop")
	ref := func(role, name string) string {
		t.Helper()
		m := regexp.MustCompile(`- ` + role + ` "` + name + `"(?: \[[a-z=, ]+\])? \[ref=(e\d+)\]`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no %s %q with a ref in:\n%s", role, name, page)
		}
		return m[1]
	}
	page = ok("browser_snapshot", nil, "Shop")
	ok("browser_click", map[string]any{"ref": ref("button", "Buy")}, "Clicked")
	ok("browser_type", map[string]any{"ref": ref("textbox", "Search"), "text": "Hello, World"}, "Typed into")
	ok("browser_type", map[string]any{"ref": ref("textbox", "Message"), "text": "你好 ok"}, "Typed into")
	ok("browser_type", map[string]any{"ref": ref("textbox", "Chat"), "text": "早上好\nline two", "submit": true}, "Typed into")
	ok("browser_scroll", map[string]any{"direction": "down", "amount": 600}, "Scrolled")
	ok("browser_wait", map[string]any{"text": "Shop"})
	ok("browser_text", nil, "Shop")

	var seen struct {
		Calls            []string
		Moves            int
		MovesBeforeClick int
		Click            *struct {
			Trusted bool
			Dx, Dy  float64
		}
		Keys     [][]any
		Shift    int
		Composed []string
		Wheels   int
		Sent     string
		Pressure *float64
		ShiftAt  *int
	}
	out := ok("browser_evaluate", map[string]any{"script": "() => seen"})
	if i := strings.Index(out, "{"); i < 0 || json.Unmarshal([]byte(out[i:strings.LastIndex(out, "}")+1]), &seen) != nil {
		t.Fatalf("the page's record: %s", out)
	}
	if len(seen.Calls) != 0 {
		t.Errorf("the page saw the extension call its own functions: %v", seen.Calls)
	}
	if seen.Click == nil || !seen.Click.Trusted {
		t.Errorf("no trusted click on Buy: %+v", seen.Click)
	} else if math.Abs(seen.Click.Dx) <= 0.5 && math.Abs(seen.Click.Dy) <= 0.5 {
		t.Errorf("the click landed on the middle of Buy (%v, %v)", seen.Click.Dx, seen.Click.Dy)
	}
	if seen.Pressure == nil {
		t.Errorf("no pressure seen on Buy's pointerdown")
	} else if *seen.Pressure != 0.5 {
		t.Errorf("the button went down at pressure %v: a mouse's is 0.5", *seen.Pressure)
	}
	if seen.MovesBeforeClick < 4 {
		t.Errorf("the mouse moved %d times before the click, not along a path", seen.MovesBeforeClick)
	}
	var typed, gaps []float64
	for _, k := range seen.Keys {
		if k[0] != "Shift" && k[0] != "Backspace" && k[0] != "Process" {
			typed = append(typed, k[1].(float64))
		}
		if k[2] != true {
			t.Errorf("an untrusted keydown: %v", k)
		}
	}
	if len(typed) < len("Hello, World")+len(" ok") {
		t.Errorf("%d keydowns for %d typed characters: text went in without keys", len(typed), len("Hello, World")+len(" ok"))
	}
	for i := 1; i < len(typed) && i < 12; i++ {
		gaps = append(gaps, typed[i]-typed[i-1])
	}
	lo, hi := 1e9, 0.0
	for _, g := range gaps {
		lo, hi = min(lo, g), max(hi, g)
	}
	if len(gaps) < 5 || hi-lo < 20 {
		t.Errorf("the gaps between keys are even (%v): a person's are not", gaps)
	}
	if seen.Shift < 2 {
		t.Errorf("Shift was pressed %d times for H and W", seen.Shift)
	}
	if seen.ShiftAt == nil {
		t.Errorf("no Shift keydown seen")
	} else if *seen.ShiftAt != 1 {
		t.Errorf("Shift came from keyboard location %v, not the left Shift (1)", *seen.ShiftAt)
	}
	if strings.Join(seen.Composed, "") != "你好早上好" {
		t.Errorf("Chinese didn't come through an IME's composition: %q", seen.Composed)
	}
	if seen.Sent != "早上好\nline two" {
		t.Errorf("the chat box sent %q: a newline must not send it early, submit must", seen.Sent)
	}
	if seen.Wheels < 3 {
		t.Errorf("a 600px scroll came as %d wheel events", seen.Wheels)
	}
	ok("browser_evaluate", map[string]any{"script": "() => search.value + '|' + msg.value"}, "Hello, World|你好 ok")

	// the console is off on a site's own host until asked for, then read
	ok("browser_console", nil, "reads this tab's console from now on")
	ok("browser_click", map[string]any{"ref": ref("button", "Log")})
	ok("browser_console", nil, "bought")

	// the user's own tab, named, is opened in one of the agent's
	users := regexp.MustCompile(`(?m)^[+ ] (t\d+) "Watch 0" .*/watch$`).FindStringSubmatch(ok("browser_tabs", nil))
	if users == nil {
		t.Fatal("no user's page")
	}
	got := ok("browser_snapshot", map[string]any{"tab": users[1]}, "is the user's own tab", "The user's page")
	copied := regexp.MustCompile(`opened in (t\d+)`).FindStringSubmatch(got)
	if copied == nil || copied[1] == users[1] {
		t.Fatalf("no tab of the agent's for the user's %s:\n%s", users[1], got)
	}
	if again := ok("browser_snapshot", map[string]any{"tab": users[1]}); strings.Contains(again, "is the user's own tab") || !strings.Contains(again, copied[1]) {
		t.Fatalf("naming the user's tab again didn't go on in %s:\n%s", copied[1], again)
	}
	list := ok("browser_tabs", nil)
	if !regexp.MustCompile(`(?m)^\* ` + copied[1] + ` \[Magpie\] "Watch 0"`).MatchString(list) {
		t.Fatalf("the copy %s isn't the agent's, in its group:\n%s", copied[1], list)
	}
	if !regexp.MustCompile(`(?m)^[+ ] ` + users[1] + ` "Watch 0" .*/watch$`).MatchString(list) {
		t.Fatalf("the user's tab %s changed:\n%s", users[1], list)
	}
	if m := call("browser_tabs", map[string]any{"action": "close", "tab": users[1]}); !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "the user's own tab") {
		t.Fatalf("closing the user's tab: %+v", m.Result)
	}
}

// A long text goes in at a person's pace, not squeezed into what one call
// may wait: Discord can time a message from its first key to its sending,
// and 800 characters typed in 35s is no person's. The call answers when
// it has waited long enough, the typing goes on, and the tab's next call
// waits for it (Enter included).
func TestBrowserTypesALongTextAtAPersonsPace(t *testing.T) {
	srv, site := realBrowser(t)
	_, ok := browserCaller(t, srv, "typist")
	shop := strings.Replace(site.URL, "127.0.0.1", "shop.example", 1) + "/human"
	page := ok("browser_tabs", map[string]any{"action": "new", "url": shop}, "Shop")
	m := regexp.MustCompile(`- textbox "Chat" \[ref=(e\d+)\]`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no Chat box in:\n%s", page)
	}
	text := strings.TrimSpace(strings.Repeat("a long line of chat ", 16))
	start := time.Now()
	first := ok("browser_type", map[string]any{"ref": m[1], "text": text, "submit": true}, "characters so far", "then Enter", "the next call in this tab waits for it")
	if took := time.Since(start); took > 60*time.Second {
		t.Fatalf("browser_type answered after %v: past what a call may wait", took)
	}
	out := ok("browser_evaluate", map[string]any{"script": "() => ({sent: seen.sent, first: seen.keys[0][1], last: seen.keys[seen.keys.length - 1][1], n: seen.keys.length})"})
	var got struct {
		Sent        string
		First, Last float64
		N           int
	}
	if i := strings.Index(out, "{"); i < 0 || json.Unmarshal([]byte(out[i:strings.LastIndex(out, "}")+1]), &got) != nil {
		t.Fatalf("the page's record: %s", out)
	}
	if got.Sent != text {
		t.Fatalf("the chat box sent %q after %q, not the whole text", got.Sent, first)
	}
	if cps := float64(len(text)) / ((got.Last - got.First) / 1000); cps > 9 {
		t.Errorf("%d characters typed at %.1f a second: no person's pace", len(text), cps)
	}
}
