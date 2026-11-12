package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// listAsk asks srv's list, waiting wait seconds at the list tag names when
// wait is set.
func listAsk(t *testing.T, url, wait, tag string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/v1/models", nil)
	if wait != "" {
		req.Header.Set(provider.ListWaitHeader, wait)
	}
	if tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("ETag"), string(b)
}

// listHeld is the wait srv's list answer says it held.
func listHeld(t *testing.T, url, wait, tag string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/v1/models", nil)
	if wait != "" {
		req.Header.Set(provider.ListWaitHeader, wait)
	}
	if tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.Header.Get(provider.ListWaitHeader)
}

// A client polling the list with If-None-Match (clyzhicore on Discord: a
// magpie or a script following another's list) is answered 304 with no
// body, at once, while the list is the one it names, and the new list
// with its new tag once it changed. A tag it doesn't name, or none, is
// answered the list.
func TestModelsListConditional(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	_, tag, plain := listAsk(t, srv.URL, "", "")
	if tag == "" || !strings.HasPrefix(tag, `"`) {
		t.Fatalf("ETag %q", tag)
	}
	if _, again, body := listAsk(t, srv.URL, "", ""); again != tag || body != plain {
		t.Fatalf("the same list asked again: ETag %q (was %q), same body %v", again, tag, body == plain)
	}
	for _, named := range []string{tag, "W/" + tag, `"other", ` + tag, "*"} {
		code, again, body := listAsk(t, srv.URL, "", named)
		if code != http.StatusNotModified || again != tag || body != "" {
			t.Errorf("If-None-Match %s: %d, ETag %q, body %q; want 304 %s and none", named, code, again, body, tag)
		}
	}
	if code, _, body := listAsk(t, srv.URL, "", `"ml-other"`); code != 200 || body != plain {
		t.Errorf("another tag: %d, same body %v", code, body == plain)
	}

	if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake 2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	code, now, body := listAsk(t, srv.URL, "", tag)
	if code != 200 || now == "" || now == tag || !strings.Contains(body, "m2") {
		t.Fatalf("after a change: %d, ETag %q (was %q), m2 listed %v", code, now, tag, strings.Contains(body, "m2"))
	}

	// a list refused is answered as before, untagged
	req, _ := http.NewRequest("GET", srv.URL+"/v1/models?kind=bogus", nil)
	req.Header.Set("If-None-Match", "*")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 || res.Header.Get("ETag") != "" {
		t.Errorf("a refused list: %d, ETag %q; want 400 and none", res.StatusCode, res.Header.Get("ETag"))
	}
}

// Another magpie waiting at the list (KaCli) is answered the list's ETag;
// the same list is held to the end of the wait and answered 304. A list
// asked without the header is answered the same bytes with the same ETag
// (clyzhicore), and only a wait's answer says how long it held.
func TestModelsWaitTagged(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	code, plainTag, plain := listAsk(t, srv.URL, "", "")
	if code != 200 || plainTag == "" {
		t.Fatalf("a plain list: %d, ETag %q; want 200 and a tag", code, plainTag)
	}
	if held := listHeld(t, srv.URL, "", ""); held != "" {
		t.Errorf("a plain list says it held %q", held)
	}
	code, tag, body := listAsk(t, srv.URL, "1", "")
	if code != 200 || tag != plainTag || body != plain {
		t.Fatalf("a wait with no tag: %d, ETag %q (plain %q), same body %v", code, tag, plainTag, body == plain)
	}
	if held := listHeld(t, srv.URL, "1", ""); held != "1" {
		t.Errorf("a wait's list says it held %q, want 1", held)
	}
	start := time.Now()
	code, again, _ := listAsk(t, srv.URL, "1", tag)
	if code != http.StatusNotModified || again != tag {
		t.Fatalf("the same list: %d, ETag %q; want 304 %q", code, again, tag)
	}
	if held := listHeld(t, srv.URL, "1", tag); held != "1" {
		t.Errorf("a wait's 304 says it held %q, want 1", held)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("the same list answered after %v, before its wait ended", d)
	}
}

// A change saved while another magpie waits answers it the new list at
// once, not at the end of its wait.
func TestModelsWaitAnswersAChange(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()
	_, tag, _ := listAsk(t, srv.URL, "1", "")

	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := provider.Save(provider.Provider{ID: "fake2", Name: "Fake 2", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	code, now, body := listAsk(t, srv.URL, "30", tag)
	if code != 200 || now == "" || now == tag {
		t.Fatalf("after a change: %d, ETag %q (was %q)", code, now, tag)
	}
	if !strings.Contains(body, "m2") {
		t.Errorf("the new list lacks m2: %s", body)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the change answered after %v", d)
	}
}

// A gateway stopping ends every wait with 304: the other magpie keeps its
// tag and asks the next gateway.
func TestModelsWaitEndsWithTheGateway(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	_, tag, _ := listAsk(t, srv.URL, "1", "")
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.endStreams()
	}()
	start := time.Now()
	code, _, _ := listAsk(t, srv.URL, "30", tag)
	if code != http.StatusNotModified {
		t.Fatalf("a wait as the gateway stops: %d, want 304", code)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("answered after %v", d)
	}
}

// The wait is the list's own route: a caller the list refuses (another
// computer with no key, a foreign page) is refused it the same with the
// header, before anything waits.
func TestModelsWaitGuardedAsTheList(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	setup(t, provider.Chat, &fake{t: t})
	newCaller(t, "LAN client")
	h := New().Front()
	ask := func(remote, origin string, wait bool) (int, string) {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = remote
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if wait {
			r.Header.Set(provider.ListWaitHeader, "30")
			r.Header.Set("If-None-Match", `"ml-anything"`)
		}
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); h.ServeHTTP(w, r) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s %s was held", remote, origin)
		}
		return w.Code, w.Body.String()
	}
	for _, c := range []struct{ remote, origin string }{
		{"192.168.1.9:5000", ""},
		{"127.0.0.1:5000", "https://evil.example"},
		{"127.0.0.1:5000", "null"},
	} {
		plain, _ := ask(c.remote, c.origin, false)
		if plain == 200 {
			t.Fatalf("%s %s: the plain list was answered; the case tests nothing", c.remote, c.origin)
		}
		waited, body := ask(c.remote, c.origin, true)
		if waited != plain {
			t.Errorf("%s %s: %d with the wait, %d without (%s)", c.remote, c.origin, waited, plain, body)
		}
	}
}

// A proxy that compresses the list (Cloudflare's) makes its ETag weak and
// sends it back so in If-None-Match. The wait still names the list it
// has: held to the end and answered 304, not answered at once (y0ngb1n on
// X: a magpie behind Cloudflare was asked for /v1/models once a second).
func TestModelsWaitNamedByAWeakTag(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()
	_, tag, _ := listAsk(t, srv.URL, "1", "")

	start := time.Now()
	code, _, _ := listAsk(t, srv.URL, "1", "W/"+tag)
	if code != http.StatusNotModified {
		t.Fatalf("a wait naming W/%s: %d, want 304", tag, code)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("answered after %v, before its wait ended", d)
	}
}
