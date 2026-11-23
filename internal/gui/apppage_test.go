package gui

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// appPageTest is a fresh home and a fresh appPage in appPageServer's place,
// its page a teapot that says it was reached. Whatever it serves is closed
// when the test ends.
func appPageTest(t *testing.T, reached *bool) *appPage {
	t.Helper()
	onePortHome(t)
	g := gateway.New()
	prevGW := served.Swap(g)
	p := &appPage{wake: make(chan struct{}, 1)}
	p.mk = func() http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			*reached = true
			rw.WriteHeader(http.StatusTeapot)
		})
	}
	prev := appPageServer
	appPageServer = p
	t.Cleanup(func() {
		p.mu.Lock()
		if p.srv != nil {
			p.srv.Close()
		}
		p.mu.Unlock()
		appPageServer = prev
		served.Store(prevGW)
	})
	return p
}

// appPagePost is POST /api/web-page on the app's windows' API.
func appPagePost(t *testing.T, body string) (int, appPageJSON) {
	t.Helper()
	mux := http.NewServeMux()
	appPageRoutes(mux)
	r := httptest.NewRequest("POST", "/api/web-page", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var s appPageJSON
	json.Unmarshal(w.Body.Bytes(), &s)
	return w.Code, s
}

// noRedirect is a client that shows a redirect rather than following it.
var noRedirect = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func appPageGet(t *testing.T, port int, path, cookie string, hdr ...string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "magpie_web_" + strconv.Itoa(port), Value: cookie})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := noRedirect.Do(r)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	res.Body.Close()
	return res
}

// jiakun_zhao on X: the page the app's window shows, served to a browser
// (through a Cloudflare tunnel) by the app itself rather than `magpie web`
// kept running beside it. It is off until Settings turns it on, and never
// answers without its key: not the page, not its API, not the gateway's
// client routes without a gateway key; a new key turns every browser
// signed in with the old one away.
func TestAppPageServesOnlyWithItsKey(t *testing.T) {
	var reached bool
	p := appPageTest(t, &reached)
	if a, k := AppPage(); a != "" || k != "" {
		t.Fatalf("a new home serves the page at %q", a)
	}
	port := freePort(t)

	code, s := appPagePost(t, `{"mode":"local","port":`+strconv.Itoa(port)+`}`)
	if code != 200 || !s.Serving || s.Mode != "local" || s.Port != port {
		t.Fatalf("turned on: %d %+v", code, s)
	}
	key := strings.TrimPrefix(s.Link, "http://127.0.0.1:"+strconv.Itoa(port)+"/?k=")
	if CheckWebKey(key) != nil || key == s.Link {
		t.Fatalf("the link carries no key: %q", s.Link)
	}
	if a, k := AppPage(); a != "127.0.0.1:"+strconv.Itoa(port) || k != key {
		t.Errorf("AppPage() = %q, %q", a, k)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(appPageFile()); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("web-page.json: %v %v, want 0600", fi.Mode(), err)
		}
	}
	if b, _ := os.ReadFile(appPageFile()); !strings.Contains(string(b), key) {
		t.Errorf("the key is not in web-page.json: %s", b)
	}
	// only on this computer
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err != nil {
		t.Fatal(err)
	}
	if p.addr != "127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("local listens on %q", p.addr)
	}

	for _, c := range []struct {
		name, path, cookie string
		hdr                []string
	}{
		{"the page", "/", "", nil},
		{"the page, as a browser", "/", "", []string{"Accept", "text/html"}},
		{"the API", "/api/state", "", nil},
		{"the API, from another host", "/api/state", "", []string{"Host", "rebind.attacker.example:" + strconv.Itoa(port)}},
		{"the API, Origin null", "/api/state", "", []string{"Origin", "null"}},
		{"the API, a wrong cookie", "/api/state", "0123456789abcdef-wrong", nil},
		{"the page, a wrong key in the link", "/?k=0123456789abcdef-wrong", "", nil},
	} {
		res := appPageGet(t, port, c.path, c.cookie, c.hdr...)
		if res.StatusCode == http.StatusTeapot || res.StatusCode == 200 || res.StatusCode == http.StatusSeeOther {
			t.Errorf("%s without the key: %d", c.name, res.StatusCode)
		}
	}
	if reached {
		t.Fatal("the page was reached without its key")
	}
	// a browser turned away is told where the app's key is: no magpie
	// web printed a link for it (jenryai on X)
	{
		r, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
		r.Header.Set("Accept", "text/html")
		res, err := noRedirect.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), "Settings › Network and sharing › Browser access") || strings.Contains(string(b), "printed when it started") || strings.Contains(string(b), key) {
			t.Errorf("turned away from the app's page: %d %s", res.StatusCode, b)
		}
	}
	// the gateway's client routes on the page's port need a gateway key,
	// from this computer too (a tunnel's requests come from loopback)
	if res := appPageGet(t, port, "/v1/models", ""); res.StatusCode == 200 {
		t.Errorf("/v1/models without a gateway key: %d", res.StatusCode)
	}
	// shared: a gateway key, and nothing less, lets an agent through
	gwKey := onePortKey(t)
	if res := appPageGet(t, port, "/v1/models", ""); res.StatusCode == 200 {
		t.Errorf("/v1/models shared, without a gateway key: %d", res.StatusCode)
	}
	if res := appPageGet(t, port, "/v1/models", key); res.StatusCode == 200 {
		t.Errorf("/v1/models with the page's cookie alone: %d", res.StatusCode)
	}
	if res := appPageGet(t, port, "/v1/models", "", "Authorization", "Bearer "+gwKey); res.StatusCode != 200 {
		t.Errorf("/v1/models with a gateway key: %d", res.StatusCode)
	}

	res := appPageGet(t, port, "/?k="+key, "")
	var cookie string
	for _, c := range res.Cookies() {
		if c.Name == "magpie_web_"+strconv.Itoa(port) {
			cookie = c.Value
		}
	}
	if res.StatusCode != http.StatusSeeOther || cookie != key {
		t.Fatalf("the link: %d, cookie %q", res.StatusCode, cookie)
	}
	if res := appPageGet(t, port, "/api/state", cookie); res.StatusCode != http.StatusTeapot {
		t.Fatalf("signed in: %d", res.StatusCode)
	}

	// a new key: the old cookie is turned away, the new link signs in
	code, s = appPagePost(t, `{"newKey":true}`)
	if code != 200 || !s.Serving || strings.Contains(s.Link, key) {
		t.Fatalf("new key: %d %+v", code, s)
	}
	reached = false
	if res := appPageGet(t, port, "/api/state", cookie); res.StatusCode != http.StatusUnauthorized || reached {
		t.Errorf("the old key's cookie after a new key: %d", res.StatusCode)
	}
	if res := appPageGet(t, port, strings.TrimPrefix(s.Link, "http://127.0.0.1:"+strconv.Itoa(port)), ""); res.StatusCode != http.StatusSeeOther {
		t.Errorf("the new link: %d", res.StatusCode)
	}

	// off: nothing listens
	if code, s = appPagePost(t, `{"mode":""}`); code != 200 || s.Serving || s.Link != "" {
		t.Fatalf("turned off: %d %+v", code, s)
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
		c.Close()
		t.Error("the page still listens once turned off")
	}
	if a, _ := AppPage(); a != "" {
		t.Errorf("AppPage() once off = %q", a)
	}
}

// A port another program holds (`magpie web`, or the magpie before a
// restart to update) is said, and taken once it is let go.
func TestAppPageWaitsForABusyPort(t *testing.T) {
	var reached bool
	p := appPageTest(t, &reached)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := held.Addr().(*net.TCPAddr).Port
	code, s := appPagePost(t, `{"mode":"local","port":`+strconv.Itoa(port)+`}`)
	if code != 200 || s.Serving || !strings.Contains(s.Error, "port "+strconv.Itoa(port)) {
		t.Fatalf("on a held port: %d %+v", code, s)
	}
	if !p.waiting() {
		t.Error("a held port isn't tried again")
	}
	held.Close()
	p.apply()
	if s := p.state(); !s.Serving || s.Error != "" {
		t.Fatalf("once let go: %+v", s)
	}
	if p.waiting() {
		t.Error("still trying once served")
	}
}

// A web-page.json that can't be read as a choice serves nothing, and says
// why; Settings writes it anew only with the whole choice.
func TestAppPageUnreadFileServesNothing(t *testing.T) {
	for _, body := range []string{"{not json", `{"mode":"everywhere","key":"0123456789abcdef-key"}`} {
		var reached bool
		p := appPageTest(t, &reached)
		if err := os.MkdirAll(strings.TrimSuffix(appPageFile(), "web-page.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(appPageFile(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		p.apply()
		if s := p.state(); s.Serving || s.Error == "" || s.Link != "" {
			t.Errorf("%s: %+v", body, s)
		}
		if a, _ := AppPage(); a != "" {
			t.Errorf("%s: AppPage() = %q", body, a)
		}
		if code, _ := appPagePost(t, `{"newKey":true}`); code == 200 {
			t.Errorf("%s: a new key alone wrote the file", body)
		}
		if code, s := appPagePost(t, `{"mode":""}`); code != 200 || s.Error != "" {
			t.Errorf("%s: turned off: %d %+v", body, code, s)
		}
	}
}

// Settings turns down what isn't a choice: a mode not known, a port below
// 1024 or the gateway's own.
func TestAppPageTurnsDownBadChoices(t *testing.T) {
	var reached bool
	appPageTest(t, &reached)
	gwPort := freePort(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:"+strconv.Itoa(gwPort))
	for _, body := range []string{`{"mode":"everywhere"}`, `{"port":80}`, `{"port":70000}`, `{"port":` + strconv.Itoa(gwPort) + `}`} {
		if code, _ := appPagePost(t, body); code == 200 {
			t.Errorf("%s was taken", body)
		}
	}
	if _, err := os.Stat(appPageFile()); err == nil {
		t.Error("a choice turned down was written")
	}
}

// A browser on the app's page is a browser tab: its page says web, the
// app's Browser access isn't on its API (it can't turn the page off under
// itself, nor read the key), and it doesn't make the app's own windows
// `magpie web`'s (webPage).
func TestAppPageBrowserIsATab(t *testing.T) {
	onePortHome(t)
	webPage.Store(false)
	t.Cleanup(func() { webPage.Store(false) })
	b := appWebHost{webHost{}, nil}
	if !isWeb(b) || ownWeb(b) {
		t.Fatalf("isWeb %v, ownWeb %v", isWeb(b), ownWeb(b))
	}
	h := Handler(b, nil)
	if webPage.Load() {
		t.Error("the app's page set webPage, as magpie web's does")
	}
	for _, m := range []string{"GET", "POST"} {
		r := httptest.NewRequest(m, "/api/web-page", strings.NewReader(`{"mode":"lan"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Errorf("%s /api/web-page from the browser: %d %s", m, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(appPageFile()); err == nil {
		t.Error("the browser wrote web-page.json")
	}
	r := httptest.NewRequest("GET", "/boot.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"web":true`) {
		t.Errorf("boot.js doesn't say web: %.200s", w.Body)
	}
	// the app's own windows have it
	r = httptest.NewRequest("GET", "/api/web-page", nil)
	w = httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("GET /api/web-page from the app: %d", w.Code)
	}
}
