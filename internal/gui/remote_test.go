package gui

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// remoteWin is the app's windows, as far as another magpie's window goes:
// the links it was asked to open.
type remoteWin struct {
	Windows
	opened []string
}

func (w *remoteWin) OpenRemote(link, title string) { w.opened = append(w.opened, link) }

func remotePost(t *testing.T, w Windows, path, body string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	remoteRoutes(mux, w)
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// What is typed as the other magpie's address: the link it printed, with
// its key taken out, or a bare address; plain http only where the key
// doesn't cross the internet.
func TestRemoteAddr(t *testing.T) {
	for _, c := range []struct{ in, addr, key, err string }{
		{"https://magpie.example.com/?k=0123456789abcdef-key", "https://magpie.example.com/", "0123456789abcdef-key", ""},
		{"magpie.example.com:3430", "https://magpie.example.com:3430/", "", ""},
		{"http://192.168.1.20:3430/?k=abc&view=usage#x", "http://192.168.1.20:3430/?view=usage", "abc", ""},
		{"http://100.101.1.2:3430", "http://100.101.1.2:3430/", "", ""},
		{"http://nas.local:3430", "http://nas.local:3430/", "", ""},
		{"http://server:3430", "http://server:3430/", "", ""},
		{"https://example.com/magpie/", "https://example.com/magpie/", "", ""},
		{"http://magpie.example.com:3430", "", "", "name on the internet"},
		{"https://u:p@magpie.example.com", "", "", "user name"},
		{"ftp://example.com", "", "", "http:// or https://"},
		{"file:///etc/passwd", "", "", "isn't an address"},
		{"  ", "", "", "type the other"},
	} {
		addr, key, err := remoteAddr(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || addr != c.addr || key != c.key {
			t.Errorf("%q: %q %q %v, want %q %q", c.in, addr, key, err, c.addr, c.key)
		}
	}
}

// xuanwo on X: the app opens a server's magpie, its routing and usage, in
// a window. The key is checked against that magpie's own page before the
// window opens, kept 0600 on this computer, never read back into the page,
// and the link the window opens signs in to the other magpie's page as a
// browser does.
func TestRemoteOpensTheOtherMagpie(t *testing.T) {
	onePortHome(t)
	const key = "0123456789abcdef-remote"
	reached := 0
	other := httptest.NewServer(webHandler("3430", key, webCookieAge, nil, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		reached++
		rw.WriteHeader(http.StatusTeapot)
	})))
	defer other.Close()

	w := &remoteWin{}
	// a wrong key: said, nothing opened, nothing kept
	code, body := remotePost(t, w, "/api/remote", `{"url":`+jsonStr(other.URL)+`,"key":"0123456789abcdef-wrong"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "turned the key away") || len(w.opened) != 0 || reached != 0 {
		t.Fatalf("wrong key: %d %s %v", code, body, w.opened)
	}
	if _, err := os.Stat(remoteFile()); !os.IsNotExist(err) {
		t.Fatalf("a refused key was kept: %v", err)
	}
	// no key at all
	if code, body := remotePost(t, w, "/api/remote", `{"url":`+jsonStr(other.URL)+`}`); code != http.StatusBadRequest || !strings.Contains(body, "type that magpie's key") {
		t.Fatalf("no key: %d %s", code, body)
	}

	// the link it printed, pasted whole
	code, body = remotePost(t, w, "/api/remote", `{"url":`+jsonStr(other.URL+"/?k="+key)+`}`)
	if code != http.StatusOK || len(w.opened) != 1 || w.opened[0] != other.URL+"/?k="+key {
		t.Fatalf("open: %d %s %v", code, body, w.opened)
	}
	if strings.Contains(body, key) {
		t.Fatalf("the key went back to the page: %s", body)
	}
	if reached != 0 {
		t.Fatal("the check read the page behind the key")
	}
	fi, err := os.Stat(remoteFile())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("remote-magpie.json is %v", fi.Mode().Perm())
	}
	mux := http.NewServeMux()
	remoteRoutes(mux, w)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/remote", nil))
	if s := rec.Body.String(); strings.Contains(s, key) || !strings.Contains(s, `"hasKey":true`) || !strings.Contains(s, other.URL) {
		t.Fatalf("GET /api/remote: %s", s)
	}

	// the window's link signs in to the other magpie's page, as a browser
	jar, _ := cookiejar.New(nil)
	res, err := (&http.Client{Jar: jar, Timeout: 5 * time.Second}).Get(w.opened[0])
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTeapot || reached != 1 || strings.Contains(res.Request.URL.String(), key) {
		t.Fatalf("the window's link: %d at %s, reached %d", res.StatusCode, res.Request.URL, reached)
	}

	// opened again without the key: the one kept for the address
	if code, body := remotePost(t, w, "/api/remote", `{"url":`+jsonStr(other.URL)+`}`); code != http.StatusOK || len(w.opened) != 2 || w.opened[1] != other.URL+"/?k="+key {
		t.Fatalf("again: %d %s %v", code, body, w.opened)
	}
	// forgotten
	if code, _ := remotePost(t, w, "/api/remote/forget", `{}`); code != http.StatusOK {
		t.Fatalf("forget: %d", code)
	}
	if _, err := os.Stat(remoteFile()); !os.IsNotExist(err) {
		t.Fatalf("forgotten, still there: %v", err)
	}
}

// Something at the address that isn't a magpie page, or none: said, and
// no window opened.
func TestRemoteNotMagpie(t *testing.T) {
	onePortHome(t)
	plain := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/login", http.StatusSeeOther) // a redirect with no magpie cookie
	}))
	defer plain.Close()
	w := &remoteWin{}
	if code, body := remotePost(t, w, "/api/remote", `{"url":`+jsonStr(plain.URL)+`,"key":"0123456789abcdef-key"}`); code != http.StatusBadRequest || !strings.Contains(body, "not as a magpie page") || len(w.opened) != 0 {
		t.Fatalf("not magpie: %d %s", code, body)
	}
	plain.Close()
	if code, body := remotePost(t, w, "/api/remote", `{"url":`+jsonStr(plain.URL)+`,"key":"0123456789abcdef-key"}`); code != http.StatusBadRequest || !strings.Contains(body, "doesn't answer") || len(w.opened) != 0 {
		t.Fatalf("gone: %d %s", code, body)
	}
}

// A page served to a browser (magpie web, the app's Browser access) has no
// /api/remote: only the app's own windows open another magpie, and a
// browser can't read or change the key kept for it.
func TestRemoteNotOnBrowserPages(t *testing.T) {
	onePortHome(t)
	h := webPageHandler(t, webHost{}, nil)
	for _, m := range []string{"GET", "POST"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/remote", strings.NewReader(`{"url":"http://127.0.0.1:1"}`)))
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "hasKey") {
			t.Fatalf("%s /api/remote on a browser's page: %d %s", m, rec.Code, rec.Body.String())
		}
	}
	// and a host that can't open windows refuses rather than keeping it
	if code, body := remotePost(t, webHost{}, "/api/remote", `{"url":"http://127.0.0.1:1/?k=0123456789abcdef-key"}`); code != http.StatusBadRequest || !strings.Contains(body, "only the app") {
		t.Fatalf("browser host: %d %s", code, body)
	}
}
