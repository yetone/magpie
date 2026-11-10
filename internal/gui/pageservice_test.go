package gui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// fakeService is a plugin's local service on 127.0.0.1, as the chat
// page's server is: what it saw of the last request, a page, an event
// stream that sends one event and then waits for more, and a redirect to
// its own address.
type fakeService struct {
	srv  *httptest.Server
	port int
	mu   sync.Mutex
	seen http.Header
	host string
	path string
	more chan struct{}
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	f := &fakeService{more: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen, f.host, f.path = r.Header.Clone(), r.Host, r.URL.RequestURI()
		f.mu.Unlock()
		switch r.URL.Path {
		case "/":
			http.SetCookie(rw, &http.Cookie{Name: "svc", Value: "1"})
			// YangHonghang's chat service on #4198
			rw.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:")
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(rw, `<!doctype html><title>chat</title><script src="app.js"></script>`)
		case "/api/chat":
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(200)
			// big enough that a compressor would hold nothing back for
			// size, small enough to sit in its buffer
			fmt.Fprint(rw, "data: first\n\n")
			rw.(http.Flusher).Flush()
			select {
			case <-f.more:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(rw, "data: second\n\n")
		case "/go":
			http.Redirect(rw, r, "http://127.0.0.1:"+strconv.Itoa(f.port)+"/api/x?a=1", http.StatusFound)
		case "/away":
			http.Redirect(rw, r, "https://example.com/elsewhere", http.StatusFound)
		default:
			rw.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(rw, "path "+r.URL.Path)
		}
	}))
	t.Cleanup(f.srv.Close)
	_, p, _ := net.SplitHostPort(f.srv.Listener.Addr().String())
	f.port, _ = strconv.Atoi(p)
	return f
}

func (f *fakeService) last() (http.Header, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen, f.host, f.path
}

// servicePlugins makes the sandbox of pagePlugins hold a hosted plugin
// whose page declares service (a JSON value), with the plugin's options,
// and answers its page as /api/plugin-pages lists it to a browser.
func servicePlugins(t *testing.T, service string, options map[string]any) {
	t.Helper()
	on, _ := pagePlugins(t)
	hosted := filepath.Join(filepath.Dir(on), "chat")
	if err := os.CopyFS(hosted, os.DirFS("testdata/page-plugin")); err != nil {
		t.Fatal(err)
	}
	pkg := `{"name": "page-chat", "main": "./index.mjs", "magpie": {"page": {"title": "Chat", "file": "page/index.html", "service": ` + service + `}}}`
	if err := os.WriteFile(filepath.Join(hosted, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hosted, "index.mjs"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"spec": hosted, "options": options}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	warmHost = func(context.Context) error { return nil }
	t.Cleanup(func() { warmHost = plugin.Warm })
}

// serviceMux is the window's page routes, served to a browser (web) or
// to the desktop window.
func serviceMux(web bool) http.Handler {
	mux := http.NewServeMux()
	pluginPageRoutes(mux, web)
	return noFramedAPI(mux)
}

func listed(t *testing.T, h http.Handler) pluginPage {
	t.Helper()
	var pages []pluginPage
	w := do(h, httptest.NewRequest("GET", "/api/plugin-pages", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &pages); err != nil || len(pages) != 1 {
		t.Fatalf("pages: %s %v", w.Body, err)
	}
	return pages[0]
}

// aigcfree on Discord: `magpie web` on a server, opened from a Windows
// PC's browser, sent the chat page's frame to 127.0.0.1, the PC itself. A
// page served to a browser is told where its service is on magpie web's
// own address, and the desktop window is told nothing and keeps going
// to 127.0.0.1 itself.
func TestPluginPageServiceListedToBrowserOnly(t *testing.T) {
	f := newFakeService(t)
	servicePlugins(t, `{"port": `+strconv.Itoa(f.port)+`, "option": "port"}`, nil)
	p := listed(t, serviceMux(true))
	if p.Service != pageServicePath {
		t.Errorf("a browser's page: service %q, want %q (relative to the page's own address)", p.Service, pageServicePath)
	}
	if d := listed(t, serviceMux(false)); d.Service != "" {
		t.Errorf("the desktop window's page: service %q, want none", d.Service)
	}
	// the desktop window serves no service path at all
	svc := strings.TrimSuffix(p.URL, "index.html") + pageServicePath
	if w := do(serviceMux(false), httptest.NewRequest("GET", svc, nil)); w.Code != 404 {
		t.Errorf("the service through the desktop window's routes: %d, want 404", w.Code)
	}
	if _, _, path := f.last(); path != "" {
		t.Errorf("the service was reached from the desktop window's routes: %q", path)
	}
}

// What a page names wrong, or a plugin that is only a page names, is no
// service; the user's option moves it; the gateway's port never is one.
func TestPluginPageServiceSpec(t *testing.T) {
	f := newFakeService(t)
	for _, c := range []struct {
		name, service string
		options       map[string]any
		want          int
	}{
		{"the port named", `{"port": ` + strconv.Itoa(f.port) + `}`, nil, f.port},
		{"a string port", `{"port": "` + strconv.Itoa(f.port) + `"}`, nil, f.port},
		{"the option set", `{"port": 1, "option": "port"}`, map[string]any{"port": float64(f.port)}, f.port},
		{"the option set as text", `{"port": 1, "option": "port"}`, map[string]any{"port": strconv.Itoa(f.port)}, f.port},
		{"the option set wrong", `{"port": ` + strconv.Itoa(f.port) + `, "option": "port"}`, map[string]any{"port": "x"}, f.port},
		{"no port", `{"option": "port"}`, nil, 0},
		{"port out of range", `{"port": 70000}`, nil, 0},
		{"a fraction", `{"port": 3477.5}`, nil, 0},
		{"not an object", `"3477"`, nil, 0},
		{"the gateway's port", `{"port": 3425}`, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			servicePlugins(t, c.service, c.options)
			t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
			ps := pluginPages()
			if len(ps) != 1 {
				t.Fatalf("a wrong service lost the page: %+v", ps)
			}
			if got := ps[0].servicePort(); got != c.want {
				t.Errorf("service port %d, want %d", got, c.want)
			}
		})
	}
	// a page-only plugin's page gets no service, whatever it names
	pagePlugins(t)
	on := filepath.Join(os.Getenv("HOME"), "src", "glance")
	pkg := `{"name": "glance", "magpie": {"page": {"title": "G", "file": "page/index.html", "service": {"port": ` + strconv.Itoa(f.port) + `}}}}`
	if err := os.WriteFile(filepath.Join(on, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	ps := pluginPages()
	if len(ps) != 1 || ps[0].servicePort() != 0 {
		t.Errorf("a page-only plugin's service: %+v", ps)
	}
	p := listed(t, serviceMux(true))
	if p.Service != "" {
		t.Errorf("a page-only plugin's page listed a service: %q", p.Service)
	}
	svc := strings.TrimSuffix(p.URL, "index.html") + pageServicePath
	if w := do(serviceMux(true), httptest.NewRequest("GET", svc, nil)); w.Code != 404 {
		t.Errorf("a page-only plugin's service path: %d, want 404", w.Code)
	}
}

// The service through `magpie web`'s whole chain, with no cookie (a
// sandboxed frame sends none): the page's token lets it in, a made-up one
// doesn't; the service sees its own Host and no cookie; what comes back
// is sandboxed, sets no cookie here, and an event stream arrives as it is
// sent, not when it ends.
func TestPluginPageServiceProxied(t *testing.T) {
	f := newFakeService(t)
	servicePlugins(t, `{"port": `+strconv.Itoa(f.port)+`}`, nil)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	page := serviceMux(true)
	p := listed(t, page)
	srv := httptest.NewServer(webHandler("3430", onePortWebKey, 0, nil, page))
	t.Cleanup(srv.Close)
	base := srv.URL + strings.TrimSuffix(p.URL, "index.html") + pageServicePath
	get := func(target string, hdr ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}

	res := get(base, "Cookie", "magpie_web_3430=the-window", "Origin", "null")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), "<title>chat</title>") {
		t.Fatalf("the service's page: %d %s", res.StatusCode, body)
	}
	seen, host, path := f.last()
	if want := "127.0.0.1:" + strconv.Itoa(f.port); host != want {
		t.Errorf("the service saw Host %q, want its own %q", host, want)
	}
	if path != "/" {
		t.Errorf("the service saw path %q, want /", path)
	}
	if seen.Get("Cookie") != "" {
		t.Errorf("the window's cookie reached the service: %q", seen.Get("Cookie"))
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Errorf("the service set a cookie on magpie web's address: %q", res.Header.Get("Set-Cookie"))
	}
	if csp := strings.Join(res.Header.Values("Content-Security-Policy"), ", "); !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("the service's page isn't sandboxed: %q", csp)
	}
	if res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy %q", res.Header.Get("Referrer-Policy"))
	}
	// the service's own 'self' named with its scheme, for WebKit's
	// sandboxed frame (#4198); magpie's sandbox still after it
	root := strings.TrimPrefix(srv.URL, "http://") + strings.TrimSuffix(p.URL, "index.html") + pageServicePath
	if v := res.Header.Values("Content-Security-Policy"); len(v) != 2 || v[1] != pageServicePolicy ||
		v[0] != "default-src 'self' https://"+root+" http://"+root+" wss://"+root+" ws://"+root+
			"; connect-src 'self' https://"+root+" http://"+root+" wss://"+root+" ws://"+root+
			"; img-src 'self' https://"+root+" http://"+root+" data:" {
		t.Errorf("the service's policy: %q", v)
	}

	// a path and query of the service's own
	res = get(base + "api/icon/a%2Fb.png?size=2")
	if _, _, path := f.last(); path != "/api/icon/a%2Fb.png?size=2" {
		t.Errorf("the service saw %q", path)
	}

	// another site's page, or a made-up token, gets nowhere
	if res = get(base, "Origin", "https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Errorf("another site's Origin: %d, want 403", res.StatusCode)
	}
	if res = get(srv.URL + pluginPagePrefix + strings.Repeat("a", 32) + "/" + pageServicePath); res.StatusCode != 404 {
		t.Errorf("a made-up token: %d, want 404", res.StatusCode)
	}

	// redirects to the service's own address stay on magpie web's
	res = get(base + "go")
	if loc := res.Header.Get("Location"); loc != "api/x?a=1" {
		t.Errorf("a redirect to the service's own address: Location %q, want api/x?a=1", loc)
	}
	res = get(base + "away")
	if loc := res.Header.Get("Location"); loc != "https://example.com/elsewhere" {
		t.Errorf("a redirect elsewhere: Location %q", loc)
	}

	// the event stream: the first event arrives while the service still
	// holds the response open
	req, _ := http.NewRequest("POST", base+"api/chat", strings.NewReader(`{}`))
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Origin", "null")
	sres, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sres.Body.Close()
	if sres.Header.Get("Content-Encoding") != "" {
		t.Errorf("the event stream was compressed: %q", sres.Header.Get("Content-Encoding"))
	}
	if sres.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("X-Accel-Buffering %q, want no (nginx would hold the stream)", sres.Header.Get("X-Accel-Buffering"))
	}
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(sres.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	select {
	case l := <-lines:
		if l != "data: first" {
			t.Errorf("first line %q", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first event didn't arrive while the stream was open")
	}
	close(f.more)
	got := ""
	for l := range lines {
		got += l + "\n"
	}
	if !strings.Contains(got, "data: second") {
		t.Errorf("the rest of the stream: %q", got)
	}
}

// A service that isn't up yet (the plugin's host still starting) gets
// the page's frame a page that tries again, and a call an error.
func TestPluginPageServiceDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// held, not freed (a freed port can be handed to anything next): it
	// hangs up on every connection, as a service not serving yet would
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, ps, _ := net.SplitHostPort(l.Addr().String())
	servicePlugins(t, `{"port": `+ps+`}`, nil)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	page := serviceMux(true)
	p := listed(t, page)
	base := strings.TrimSuffix(p.URL, "index.html") + pageServicePath
	w := do(page, httptest.NewRequest("GET", base, nil))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Errorf("the frame before the service is up: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts") {
		t.Errorf("the retry page's CSP %q", csp)
	}
	w = do(page, httptest.NewRequest("POST", base+"api/chat", strings.NewReader("{}")))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "refresh") {
		t.Errorf("a call before the service is up: %d %s", w.Code, w.Body)
	}
}

func TestServiceLocation(t *testing.T) {
	for _, c := range []struct{ loc, up, want string }{
		{"http://127.0.0.1:3477/", "", "./"},
		{"http://localhost:3477/a/b?x=1#f", "../", "../a/b?x=1#f"},
		{"/api/x", "../../", "../../api/x"},
		{"api/x", "../", "api/x"},
		{"http://127.0.0.1:3478/", "", "http://127.0.0.1:3478/"},
		{"https://127.0.0.1:3477/", "", "https://127.0.0.1:3477/"},
		{"https://example.com/", "", "https://example.com/"},
	} {
		if got := serviceLocation(c.loc, 3477, c.up); got != c.want {
			t.Errorf("serviceLocation(%q, %q) = %q, want %q", c.loc, c.up, got, c.want)
		}
	}
}

// serviceSelf names the service's root beside each 'self', in any case,
// once a directive, and leaves a policy with none as it came.
func TestServiceSelf(t *testing.T) {
	self := []string{"https://h/r/", "http://h/r/"}
	for _, c := range []struct{ in, want string }{
		{"connect-src 'self'", "connect-src 'self' https://h/r/ http://h/r/ wss://h/r/ ws://h/r/"},
		{"script-src 'SELF' 'unsafe-inline'; frame-ancestors 'none'", "script-src 'SELF' https://h/r/ http://h/r/ 'unsafe-inline'; frame-ancestors 'none'"},
		{"img-src 'self' 'self'", "img-src 'self' https://h/r/ http://h/r/ 'self'"},
		{"default-src 'none'; style-src https://cdn.example", "default-src 'none'; style-src https://cdn.example"},
		{"", ""},
		{"upgrade-insecure-requests; script-src 'self'", "upgrade-insecure-requests; script-src 'self' https://h/r/ http://h/r/"},
	} {
		if got := serviceSelf(c.in, self); got != c.want {
			t.Errorf("serviceSelf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
