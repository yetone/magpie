package gui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWebGuard(t *testing.T) {
	h := webGuard("c", "key", 0, nil, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusTeapot) }))
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if c := do(httptest.NewRequest("GET", "/api/state", nil)).Code; c != http.StatusUnauthorized {
		t.Fatalf("no key: %d", c)
	}
	if c := do(httptest.NewRequest("GET", "/?k=nope", nil)).Code; c != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", c)
	}
	rec := do(httptest.NewRequest("GET", "/?k=key&view=settings", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?view=settings" {
		t.Fatalf("link: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Value != "key" || !ck[0].HttpOnly || ck[0].SameSite != http.SameSiteLaxMode || ck[0].MaxAge != 0 {
		t.Fatalf("cookie: %+v", ck)
	}
	r := httptest.NewRequest("POST", "/api/settings", nil)
	r.AddCookie(ck[0])
	if c := do(r).Code; c != http.StatusTeapot {
		t.Fatalf("with cookie: %d", c)
	}
	r = httptest.NewRequest("POST", "/api/settings", nil)
	r.AddCookie(&http.Cookie{Name: "c", Value: "other"})
	if c := do(r).Code; c != http.StatusUnauthorized {
		t.Fatalf("wrong cookie: %d", c)
	}
}

func TestWebKey(t *testing.T) {
	t.Setenv("MAGPIE_WEB_KEY", "")
	a, fixed, err := webKey()
	if err != nil || fixed || len(a) != 32 {
		t.Fatalf("a run's own key: %q %v %v", a, fixed, err)
	}
	if b, _, _ := webKey(); b == a {
		t.Fatal("two runs got one key")
	}
	t.Setenv("MAGPIE_WEB_KEY", "short")
	if _, _, err := webKey(); err == nil {
		t.Fatal("a short MAGPIE_WEB_KEY was taken")
	}
	t.Setenv("MAGPIE_WEB_KEY", "0123456789abcdef x;y")
	if _, _, err := webKey(); err == nil {
		t.Fatal("a MAGPIE_WEB_KEY a cookie can't hold was taken")
	}
	t.Setenv("MAGPIE_WEB_KEY", "0123456789abcdef-kept")
	if k, fixed, err := webKey(); err != nil || !fixed || k != "0123456789abcdef-kept" {
		t.Fatalf("MAGPIE_WEB_KEY: %q %v %v", k, fixed, err)
	}

	// a fixed key's cookie outlives the browser
	h := webGuard("c", "0123456789abcdef-kept", webCookieAge, nil, http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/?k=0123456789abcdef-kept", nil))
	if ck := rec.Result().Cookies(); len(ck) != 1 || time.Duration(ck[0].MaxAge)*time.Second != webCookieAge {
		t.Fatalf("fixed key's cookie: %+v", ck)
	}
}

// #2033 (321sssrt-bit): the sign-in cookie is the last Chrome drops when
// other services on the same host fill its cookie limit; a browser that
// holds two cookies of the page's name, a stale one sent first, still gets
// in; one turned away is told why and can paste the key, not sent back to
// the start-up log.
func TestWebGuardBookmark(t *testing.T) {
	const key = "0123456789abcdef-2033"
	h := webHandler("3430", key, webCookieAge, nil, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusTeapot) }))
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	page := func(cookie string) *http.Request {
		r := httptest.NewRequest("GET", "/?view=usage", nil)
		r.Header.Set("Accept", "text/html,application/xhtml+xml")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		return r
	}

	// the link's cookie, and the form's below, are Chrome's high priority
	link := do(httptest.NewRequest("GET", "/?view=usage&k="+key, nil))
	if sc := link.Header().Get("Set-Cookie"); link.Code != http.StatusSeeOther || !strings.HasSuffix(sc, "; Priority=High") || !strings.HasPrefix(sc, "magpie_web_3430="+key+";") {
		t.Fatalf("link: %d %q", link.Code, sc)
	}

	// the reporter's case: the stale value first, the right one after
	if c := do(page("magpie_web_3430=0123456789abcdef-old; magpie_web_3430=" + key)).Code; c != http.StatusTeapot {
		t.Fatalf("stale cookie first: %d", c)
	}

	// no cookie: a form, saying none was sent, and never the key
	rec := do(page(""))
	body := rec.Body.String()
	if rec.Code != http.StatusUnauthorized || !strings.Contains(body, "sent no sign-in cookie") || !strings.Contains(body, `action="`+webSignInPath+`"`) || !strings.Contains(body, `value="/?view=usage"`) || strings.Contains(body, key) {
		t.Fatalf("no cookie: %d %s", rec.Code, body)
	}
	// a cookie with another key: says so
	if body := do(page("magpie_web_3430=0123456789abcdef-old")).Body.String(); !strings.Contains(body, "holds a key that isn") {
		t.Fatalf("other key: %s", body)
	}
	// the page's own fetches get a plain 401 with the reason
	r := httptest.NewRequest("GET", "/api/state", nil)
	if rec := do(r); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sent no sign-in cookie") {
		t.Fatalf("api: %d %s", rec.Code, rec.Body.String())
	}

	post := func(k, back string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", webSignInPath, strings.NewReader(url.Values{"key": {k}, "back": {back}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return do(r)
	}
	// a wrong key pasted: turned away again, no cookie
	if rec := post("nope-nope-nope-nope", "/?view=usage"); rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong pasted key: %d %v", rec.Code, rec.Result().Cookies())
	}
	// the right one: the cookie, back where the bookmark was
	rec = post(key, "/?view=usage")
	ck := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?view=usage" || len(ck) != 1 || ck[0].Value != key || time.Duration(ck[0].MaxAge)*time.Second != webCookieAge || !strings.HasSuffix(rec.Header().Get("Set-Cookie"), "; Priority=High") {
		t.Fatalf("pasted key: %d %q %+v", rec.Code, rec.Header().Get("Location"), ck)
	}
	// the return address never leaves this server
	for _, back := range []string{"https://evil.example/", "//evil.example/", `/\evil.example`, "javascript:alert(1)", "", webSignInPath, "/a\r\nSet-Cookie: x=1"} {
		if l := post(key, back).Header().Get("Location"); l != "/" {
			t.Fatalf("back %q went to %q", back, l)
		}
	}
}
