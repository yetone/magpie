package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebGuard(t *testing.T) {
	h := webGuard("c", "key", 0, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusTeapot) }))
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
	t.Setenv("MAGPIE_WEB_KEY", "0123456789abcdef-kept")
	if k, fixed, err := webKey(); err != nil || !fixed || k != "0123456789abcdef-kept" {
		t.Fatalf("MAGPIE_WEB_KEY: %q %v %v", k, fixed, err)
	}

	// a fixed key's cookie outlives the browser
	h := webGuard("c", "0123456789abcdef-kept", webCookieAge, http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/?k=0123456789abcdef-kept", nil))
	if ck := rec.Result().Cookies(); len(ck) != 1 || time.Duration(ck[0].MaxAge)*time.Second != webCookieAge {
		t.Fatalf("fixed key's cookie: %+v", ck)
	}
}
