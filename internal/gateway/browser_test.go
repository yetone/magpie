package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/browserext"
)

func browserPost(h http.Handler, addr, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, BrowserMCPPath, strings.NewReader(body))
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

// fakeExtension connects to the gateway at base as Magpie Browser does,
// and answers each call with answer.
func fakeExtension(t *testing.T, base string, answer func(id, session, tool string, args map[string]any) any) {
	t.Helper()
	fakeExtensionAs(t, base, "Chrome/140", answer)
}

// fakeExtensionAs is fakeExtension in the browser whose User-Agent is agent.
func fakeExtensionAs(t *testing.T, base, agent string, answer func(id, session, tool string, args map[string]any) any) {
	t.Helper()
	fakeExtensionWith(t, base, "", nil, agent, answer)
}

// fakeExtensionWith is fakeExtensionAs given key (as the extension's popup
// is) and sending hdr.
func fakeExtensionWith(t *testing.T, base, key string, hdr http.Header, agent string, answer func(id, session, tool string, args map[string]any) any) {
	t.Helper()
	fakeExtensionVersion(t, base, key, hdr, agent, browserext.Version(), answer)
}

// fakeExtensionVersion is fakeExtensionWith saying it is version.
func fakeExtensionVersion(t *testing.T, base, key string, hdr http.Header, agent, version string, answer func(id, session, tool string, args map[string]any) any) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := http.Header{"Origin": {browserext.Origin}}
	for k, v := range hdr {
		h[k] = v
	}
	u := "ws" + strings.TrimPrefix(base, "http") + BrowserExtensionPath
	if key != "" {
		u += "?key=" + url.QueryEscape(key)
	}
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); c.CloseNow(); <-done })
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","version":"`+version+`","agent":"`+agent+`"}`)); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				ID, Session, Tool string
				Args              map[string]any
			}
			if json.Unmarshal(b, &m) != nil || m.ID == "" {
				continue
			}
			out, _ := json.Marshal(map[string]any{"id": m.ID, "result": answer(m.ID, m.Session, m.Tool, m.Args)})
			c.Write(ctx, websocket.MessageText, out)
		}
	}()
}

// browserConnected waits until the gateway has the extension, as it adds
// it on its hello.
func browserConnected(t *testing.T, s *Server) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); s.browser.current() == nil; {
		if time.Now().After(deadline) {
			t.Fatal("the extension never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An agent's call goes to the extension with its session, and the
// extension's MCP content comes back as the tool's result.
func TestBrowserMCPRelaysToTheExtension(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	type got struct {
		session, tool string
		args          map[string]any
	}
	calls := make(chan got, 4)
	fakeExtension(t, srv.URL, func(_, session, tool string, args map[string]any) any {
		calls <- got{session, tool, args}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "Page: Example"}}}
	})
	browserConnected(t, s)

	body := imageToolCall("browser_navigate", map[string]any{"url": "example.com"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+BrowserMCPPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(BrowserSessionHeader, "s1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var m imageReply
	if resp.StatusCode != 200 || json.Unmarshal(b, &m) != nil || m.Result.IsError || len(m.Result.Content) != 1 || m.Result.Content[0].Text != "Page: Example" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	c := <-calls
	if c.session != "s1" || c.tool != "browser_navigate" || c.args["url"] != "example.com" {
		t.Fatalf("the extension was handed %+v", c)
	}

	st, err := http.Get(srv.URL + BrowserStatusPath)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Connected          bool
		Extension, Browser string
	}
	json.NewDecoder(st.Body).Decode(&status)
	st.Body.Close()
	if !status.Connected || status.Extension != browserext.Version() || status.Browser != "Chrome/140" {
		t.Fatalf("status: %+v", status)
	}
}

// With no extension connected, the tools are still listed, and a call says
// how to set it up instead of waiting.
func TestBrowserMCPSaysWhenNoBrowser(t *testing.T) {
	fresh(t)
	h := lanGuard(New().Handler())
	w := browserPost(h, "127.0.0.1:5000", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "browser" {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body.String())
	}
	m := readImageReply(t, browserPost(h, "127.0.0.1:5000", imageToolCall("browser_snapshot", nil)))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "Magpie Browser isn't connected") {
		t.Fatalf("no browser: %+v", m)
	}
	w = browserPost(h, "127.0.0.1:5000", imageToolCall("browser_format_disk", nil))
	if !strings.Contains(w.Body.String(), "unknown tool") {
		t.Fatalf("an unknown tool: %s", w.Body.String())
	}
}

// The tools act as the user wherever their browser is signed in: never
// for another computer, shared and with its key or not, and never for a
// web page on this one.
func TestBrowserMCPGuarded(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	h := lanGuard(New().Handler())
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	const there = "192.0.2.7:4000"
	if w := browserPost(h, there, list); w.Code/100 == 2 {
		t.Fatalf("not shared: %d %s", w.Code, w.Body.String())
	}
	_, secrets := newCaller(t, "Laptop")
	// with a key it is answered, and its calls go to that key's browsers
	// only (TestBrowserIsItsKeys), of which there are none
	if w := browserPost(h, there, list, "Authorization", "Bearer "+secrets[0]); w.Code != http.StatusOK {
		t.Fatalf("shared, with a key: %d %s", w.Code, w.Body.String())
	}
	if w := browserPost(h, there, list, "Authorization", "Bearer mk-not-a-key"); w.Code/100 == 2 {
		t.Fatalf("shared, with a wrong key: %d %s", w.Code, w.Body.String())
	}
	if w := browserPost(h, "127.0.0.1:5000", list, "Origin", "http://localhost:5173"); w.Code != http.StatusForbidden {
		t.Fatalf("a page on this computer: %d %s", w.Code, w.Body.String())
	}
	for _, addr := range []string{there, "127.0.0.1:5000"} {
		r := httptest.NewRequest(http.MethodGet, BrowserStatusPath, nil)
		r.RemoteAddr = addr
		r.Host = "127.0.0.1:3425"
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		if addr != there {
			r.Header.Set("Origin", "http://localhost:5173")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status from %s: %d %s", addr, w.Code, w.Body.String())
		}
	}
}

// Only the extension itself may be the browser, on this computer or with
// a gateway key: a page that connects in its place would be handed every
// call, and could answer them.
func TestBrowserExtensionOnlyFromItsOrigin(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	h := lanGuard(New().Handler())
	_, secrets := newCaller(t, "Laptop")
	for _, c := range []struct{ addr, host, origin, key string }{
		{"127.0.0.1:5000", "127.0.0.1:3425", "chrome-extension://abcdefghijklmnopabcdefghijklmnop", ""},
		{"127.0.0.1:5000", "127.0.0.1:3425", "http://localhost:5173", ""},
		{"127.0.0.1:5000", "127.0.0.1:3425", "null", ""},
		{"127.0.0.1:5000", "127.0.0.1:3425", "", ""},
		{"127.0.0.1:5000", "rebound.example:3425", browserext.Origin, ""}, // DNS rebinding
		{"192.0.2.7:4000", "192.0.2.1:3425", browserext.Origin, ""},       // another computer, no key
		{"192.0.2.7:4000", "192.0.2.1:3425", browserext.Origin, "mk-not-a-key"},
		// a key is no use to a page
		{"192.0.2.7:4000", "192.0.2.1:3425", "http://192.0.2.1:3425", secrets[0]},
		{"192.0.2.7:4000", "192.0.2.1:3425", "chrome-extension://abcdefghijklmnopabcdefghijklmnop", secrets[0]},
		{"127.0.0.1:5000", "rebound.example:3425", "http://rebound.example:3425", secrets[0]},
	} {
		target := BrowserExtensionPath
		if c.key != "" {
			target += "?key=" + c.key
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.RemoteAddr, r.Host = c.addr, c.host
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		// refused, by the LAN guard (401 for a computer sending no key) or
		// by the handler (403): never switched to a WebSocket
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("%+v: %d %s", c, w.Code, w.Body.String())
		}
	}
}

// browser_* tool names the gateway lists are the ones the extension
// carries out.
func TestBrowserToolsAreTheExtensions(t *testing.T) {
	src, err := browserext.File("background.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range browserTools() {
		name := tl.(map[string]any)["name"].(string)
		if !strings.Contains(string(src), "  async "+name+"(session, args) {") {
			t.Errorf("%s isn't in background.js's TOOLS", name)
		}
	}
}

// With Magpie Browser in two browsers (packinggoodsllc_07668 on Discord:
// which one is used?), calls go to the last to connect until a session
// picks the other with browser_tabs use; another session stays where it
// was, the list of tabs says whose they are, and browsers lists both.
func TestBrowserMCPPicksAmongBrowsers(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	in := func(name string) func(_, _, _ string, _ map[string]any) any {
		return func(_, _, _ string, _ map[string]any) any {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "tabs of " + name}}}
		}
	}
	chrome := "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.7390.54 Safari/537.36"
	fakeExtensionAs(t, srv.URL, chrome, in("chrome"))
	browserConnected(t, s)
	fakeExtensionAs(t, srv.URL, chrome+" Edg/141.0.3537.57", in("edge"))
	for deadline := time.Now().Add(10 * time.Second); len(s.browser.all("")) < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the second extension never connected")
		}
	}
	tabs := func(session string, args map[string]any) (string, bool) {
		t.Helper()
		w := browserPost(lanGuard(s.Handler()), "127.0.0.1:5000", imageToolCall("browser_tabs", args), BrowserSessionHeader, session)
		m := readImageReply(t, w)
		var all []string
		for _, c := range m.Result.Content {
			all = append(all, c.Text)
		}
		return strings.Join(all, "\n"), m.Result.IsError
	}
	if got, _ := tabs("s1", map[string]any{}); !strings.Contains(got, "tabs of edge") || !strings.Contains(got, "b2's tabs (Edge 141); 2 browsers") {
		t.Fatalf("s1 before picking: %q", got)
	}
	if got, _ := tabs("s1", map[string]any{"action": "browsers"}); !strings.Contains(got, "  b1 Chrome 141, extension "+browserext.Version()) || !strings.Contains(got, "* b2 Edge 141, extension "+browserext.Version()) {
		t.Fatalf("browsers: %q", got)
	}
	if got, bad := tabs("s1", map[string]any{"action": "use", "browser": "Chrome"}); bad || !strings.Contains(got, "b1 (Chrome 141)") {
		t.Fatalf("use chrome: %q", got)
	}
	if got, _ := tabs("s1", map[string]any{"action": "list"}); !strings.Contains(got, "tabs of chrome") || !strings.Contains(got, "b1's tabs") {
		t.Fatalf("s1 after picking chrome: %q", got)
	}
	if got, _ := tabs("s2", map[string]any{}); !strings.Contains(got, "tabs of edge") {
		t.Fatalf("s2 was moved too: %q", got)
	}
	if got, bad := tabs("s2", map[string]any{"action": "use", "browser": "firefox"}); !bad || !strings.Contains(got, `no connected browser is "firefox"`) {
		t.Fatalf("use firefox: %q", got)
	}
	if got, bad := tabs("s2", map[string]any{"action": "use", "browser": "b1"}); bad || !strings.Contains(got, "b1") {
		t.Fatalf("use b1: %q", got)
	}
}

func TestBrowserName(t *testing.T) {
	for ua, want := range map[string]string{
		"Chrome/140": "Chrome 140",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.57": "Edge 141",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 OPR/124.0.0.0":                                   "Opera 124",
		"": "a Chromium browser",
	} {
		if got := browserName(ua); got != want {
			t.Errorf("browserName(%q) = %q, want %q", ua, got, want)
		}
	}
}

// A browser elsewhere connects with a gateway key, as one outside magpie's
// container does through its published port, and is that key's: only that
// key's callers reach it, never this computer's or another key's, and a
// key's caller never reaches this computer's browser, whatever session it
// names.
func TestBrowserIsItsKeys(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	s := New()
	// a request carrying testPeer comes from that address, as the host's
	// browser's does to a container (the bridge's 172.17.0.1, say)
	const testPeer = "X-Test-Peer"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get(testPeer); p != "" {
			r.RemoteAddr, r.Host = p, "192.0.2.1:3425"
			r.Header.Del(testPeer)
		}
		lanGuard(s.Handler()).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	keys, secrets := newCaller(t, "Host browser", "Other")
	const there = "172.17.0.1:40000"
	sessions := make(chan string, 16)
	in := func(name string) func(_, session, _ string, _ map[string]any) any {
		return func(_, session, _ string, _ map[string]any) any {
			sessions <- session
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "tabs of " + name}}}
		}
	}
	away := http.Header{testPeer: {there}}
	// no key, or one that isn't on, from there: never connected
	for _, key := range []string{"", "mk-not-a-key"} {
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + BrowserExtensionPath + "?key=" + key
		if c, resp, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {browserext.Origin}, testPeer: {there}}}); err == nil {
			c.CloseNow()
			t.Fatalf("connected from there with key %q", key)
		} else if resp == nil || resp.StatusCode/100 != 4 {
			t.Fatalf("key %q: %v %v", key, resp, err)
		}
	}
	fakeExtension(t, srv.URL, in("here"))
	browserConnected(t, s)
	fakeExtensionWith(t, srv.URL, secrets[0], away, "Chrome/141", in("the key's"))
	for deadline := time.Now().Add(10 * time.Second); len(s.browser.all(keyedSession(keys[0].ID, "x"))) < 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the keyed extension never connected")
		}
	}
	h := lanGuard(s.Handler())
	tabs := func(addr string, args map[string]any, hdr ...string) (string, bool) {
		t.Helper()
		m := readImageReply(t, browserPost(h, addr, imageToolCall("browser_tabs", args), hdr...))
		var all []string
		for _, c := range m.Result.Content {
			all = append(all, c.Text)
		}
		return strings.Join(all, "\n"), m.Result.IsError
	}
	mine := []string{"Authorization", "Bearer " + secrets[0], BrowserSessionHeader, "s1"}
	if got, bad := tabs(there, map[string]any{}, mine...); bad || got != "tabs of the key's" {
		t.Fatalf("the key's caller: %q", got)
	}
	if got := <-sessions; got != "key:"+keys[0].ID+"/s1" {
		t.Fatalf("the key's browser was handed session %q", got)
	}
	// naming this computer's session, or another key's, it still has its own
	for _, named := range []string{"hub:x", "key:" + keys[1].ID + "/s1", "key:/s1", "default"} {
		if got, bad := tabs(there, map[string]any{}, "Authorization", "Bearer "+secrets[0], BrowserSessionHeader, named); bad || got != "tabs of the key's" {
			t.Fatalf("the key's caller as %q: %q", named, got)
		}
		<-sessions
	}
	if got, _ := tabs(there, map[string]any{"action": "browsers"}, mine...); strings.Contains(got, "b1 ") || !strings.Contains(got, "b2 Chrome 141") {
		t.Fatalf("the key's caller is told of %q", got)
	}
	if got, bad := tabs(there, map[string]any{"action": "use", "browser": "b1"}, mine...); !bad || !strings.Contains(got, `no connected browser is "b1"`) {
		t.Fatalf("the key's caller picked this computer's browser: %q", got)
	}
	if got, bad := tabs(there, map[string]any{}, "Authorization", "Bearer "+secrets[1]); !bad || !strings.Contains(got, "isn't connected with this gateway key") {
		t.Fatalf("another key's caller: %q", got)
	}
	if got, bad := tabs("127.0.0.1:5000", map[string]any{}, BrowserSessionHeader, "s1"); bad || got != "tabs of here" {
		t.Fatalf("this computer's caller: %q", got)
	}
	<-sessions
	if got, _ := tabs("127.0.0.1:5000", map[string]any{"action": "use", "browser": "b2"}, BrowserSessionHeader, "s1"); !strings.Contains(got, `no connected browser is "b2"`) {
		t.Fatalf("this computer's caller picked the key's browser: %q", got)
	}
	// magpie's router runs `magpie mcp browser` here, which names its
	// keyed caller's session as the router gave it (hubCall)
	if got, bad := tabs("127.0.0.1:5000", map[string]any{}, BrowserSessionHeader, "hub:"+keyedSession(keys[0].ID, "c1")); bad || got != "tabs of the key's" {
		t.Fatalf("the router's keyed caller: %q", got)
	}
	<-sessions
	// the GUI's status, this computer's only, says one is connected
	if s.browser.current() == nil {
		t.Fatal("no browser for the GUI")
	}
}

func TestBrowserOwner(t *testing.T) {
	for in, want := range map[string]string{
		"s1": "", "default": "", "agent:claude": "", "hub:hub-claude-x": "",
		"key:k1/s1": "k1", "hub:key:k1/c": "k1", "key:k1/hub:key:k2/x": "k1",
		"key:/s1": "?", "key:k1": "?", "hub:key:": "?",
	} {
		if got := browserOwner(in); got != want {
			t.Errorf("browserOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

// The browser connected with the key magpie's own page made for it
// (OwnBrowserKey) is this computer's too when it connected from this
// computer (local), as through a name other than loopback's; one
// connected with another key isn't. From elsewhere the own key proves
// nothing: it is in a zip in Downloads, and a browser a copy of it
// connects from another computer (the Docker bridge's 172.17.0.1, say) is
// handed none of this computer's calls. Nor can the key call the browser
// MCP itself: it connects the extension and nothing else.
func TestOwnBrowserKeyIsThisComputers(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	if OwnBrowserKey() != "" {
		t.Fatal("an own browser key with none recorded")
	}
	s := New()
	// testPeer: the request comes from that address, to testHost's name
	const testPeer, testHost = "X-Test-Peer", "X-Test-Host"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get(testPeer); p != "" {
			r.RemoteAddr, r.Host = p, cmp.Or(r.Header.Get(testHost), "192.0.2.1:3425")
			r.Header.Del(testPeer)
			r.Header.Del(testHost)
		}
		lanGuard(s.Handler()).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	keys, secrets := newCaller(t, "Magpie Browser", "Other")
	if err := SetOwnBrowserKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := OwnBrowserKey(); got != keys[0].ID {
		t.Fatalf("OwnBrowserKey = %q, want %q", got, keys[0].ID)
	}
	// as a magpie from before made it, with full access: held to the
	// extension as the gateway starts
	ScopeOwnBrowserKey()
	if k, _ := access.List(); !k[len(k)-2].Browser {
		t.Fatal("the own key isn't held to the extension")
	}
	away := http.Header{testPeer: {"172.17.0.1:40000"}}
	reply := func(name string) func(_, _, _ string, _ map[string]any) any {
		return func(_, _, _ string, _ map[string]any) any {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "tabs of " + name}}}
		}
	}
	h := lanGuard(s.Handler())
	tabs := func(addr string, hdr ...string) (string, bool) {
		t.Helper()
		m := readImageReply(t, browserPost(h, addr, imageToolCall("browser_tabs", map[string]any{}), hdr...))
		var all []string
		for _, c := range m.Result.Content {
			all = append(all, c.Text)
		}
		return strings.Join(all, "\n"), m.Result.IsError
	}
	connected := func(id string, n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); len(s.browser.all(keyedSession(id, "x"))) < n; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the extension never connected")
			}
		}
	}
	// another key's browser alone: not this computer's
	fakeExtensionWith(t, srv.URL, secrets[1], away, "Chrome/141", reply("the other key's"))
	connected(keys[1].ID, 1)
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); !bad || got == "tabs of the other key's" {
		t.Fatalf("this computer's caller used another key's browser: %q", got)
	}
	// the own key's copy, from another computer: not this computer's
	fakeExtensionWith(t, srv.URL, secrets[0], away, "Chrome/141", reply("a copy's"))
	connected(keys[0].ID, 1)
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); !bad || got == "tabs of a copy's" {
		t.Fatalf("this computer's caller used the browser a copy of the own key connected from elsewhere: %q", got)
	}
	// nor from loopback through a proxy, which is someone else's
	fakeExtensionWith(t, srv.URL, secrets[0], http.Header{testPeer: {"127.0.0.1:40001"}, testHost: {"magpie.example:3425"}, "X-Forwarded-For": {"203.0.113.9"}}, "Chrome/141", reply("a proxied copy's"))
	connected(keys[0].ID, 2)
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); !bad || strings.Contains(got, "copy's") {
		t.Fatalf("this computer's caller used the browser a proxied copy connected: %q", got)
	}
	// from this computer, by a name that isn't loopback's: this computer's
	fakeExtensionWith(t, srv.URL, secrets[0], http.Header{testPeer: {"127.0.0.1:40002"}, testHost: {"host.docker.internal:3425"}}, "Chrome/141", reply("own"))
	connected(keys[0].ID, 3)
	if got, bad := tabs("127.0.0.1:5000", BrowserSessionHeader, "s1"); bad || got != "tabs of own" {
		t.Fatalf("this computer's caller: %q", got)
	}
	// the own key calls no MCP server, the browser's included
	r := httptest.NewRequest("POST", BrowserMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.RemoteAddr = "172.17.0.1:40000"
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), BrowserKeyCode) {
		t.Fatalf("the own key's caller reached the browser MCP: %d %.200s", w.Code, w.Body.String())
	}
	if got, bad := tabs("172.17.0.1:40000", "Authorization", "Bearer "+secrets[1], BrowserSessionHeader, "s1"); bad || got != "tabs of the other key's" {
		t.Fatalf("the other key's caller: %q", got)
	}
}

// A record that can't be read names no key, so no keyed browser is this
// computer's.
func TestOwnBrowserKeyUnreadable(t *testing.T) {
	fresh(t)
	if err := os.MkdirAll(ownBrowserKeyFile(), 0o700); err != nil { // a folder in its place
		t.Fatal(err)
	}
	if got := OwnBrowserKey(); got != "" {
		t.Fatalf("OwnBrowserKey from an unreadable record = %q", got)
	}
	c := &browserConn{key: "k1"}
	if c.serves("") {
		t.Fatal("a keyed browser not own serves this computer")
	}
	c.own = true
	if !c.serves("") || !c.serves("k1") || c.serves("k2") || c.serves("?") {
		t.Fatal("an own browser serves the wrong owners")
	}
	if (&browserConn{}).serves("?") || (&browserConn{}).serves("k1") {
		t.Fatal("this computer's browser serves a key")
	}
}
