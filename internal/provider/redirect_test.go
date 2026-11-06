package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// keyHeaders are the credentials a vendor's request may carry, a
// provider's own header among them: none of them may reach a host the
// vendor redirects to.
var keyHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "X-Custom-Secret"}

// thief is a host a vendor's redirect names, which records what reached it.
type thief struct {
	*httptest.Server
	mu   sync.Mutex
	seen []http.Header
}

func newThief(t *testing.T) *thief {
	th := &thief{}
	th.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		th.mu.Lock()
		th.seen = append(th.seen, r.Header.Clone())
		th.mu.Unlock()
	}))
	t.Cleanup(th.Close)
	return th
}

func (th *thief) reached() int {
	th.mu.Lock()
	defer th.mu.Unlock()
	return len(th.seen)
}

func keyedRequest(t *testing.T, method, u string) *http.Request {
	req, err := http.NewRequest(method, u, strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keyHeaders {
		req.Header.Set(k, "secret-"+k)
	}
	return req
}

// A vendor's redirect to another origin is never followed by a request
// sent for a provider (Provider.Do, through any client it is given): its
// 3xx comes back as it came, and the host it names gets no request, so no
// key. One to the vendor's own origin is followed, with every key.
func TestRedirectKeepsTheKeyAtItsOrigin(t *testing.T) {
	th := newThief(t)
	var mu sync.Mutex
	var landed http.Header
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, th.URL+"/steal", http.StatusTemporaryRedirect)
		case "/away302":
			http.Redirect(w, r, th.URL+"/steal", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/landed", http.StatusTemporaryRedirect)
		case "/landed":
			mu.Lock()
			landed = r.Header.Clone()
			mu.Unlock()
			w.WriteHeader(200)
		}
	}))
	defer vendor.Close()
	p := Provider{ID: "v", Chat: vendor.URL}
	for _, c := range []*http.Client{http.DefaultClient, {}, {Transport: http.DefaultTransport}} {
		for _, path := range []string{"/away", "/away302"} {
			res, err := p.Do(c, keyedRequest(t, "POST", vendor.URL+path))
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			res.Body.Close()
			if res.StatusCode/100 != 3 || res.Header.Get("Location") != th.URL+"/steal" {
				t.Errorf("%s: %d %q, want the vendor's 3xx as it came", path, res.StatusCode, res.Header.Get("Location"))
			}
		}
		res, err := p.Do(c, keyedRequest(t, "POST", vendor.URL+"/here"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("a same-origin redirect wasn't followed: %d", res.StatusCode)
		}
		mu.Lock()
		for _, k := range keyHeaders {
			if landed.Get(k) != "secret-"+k {
				t.Errorf("a same-origin redirect lost %s: %v", k, landed)
			}
		}
		mu.Unlock()
	}
	if n := th.reached(); n != 0 {
		t.Fatalf("the redirect's host was sent %d requests: %v", n, th.seen)
	}
}

// An https vendor's redirect to http is not followed, its own host and
// port included, so its key never goes in the clear.
func TestRedirectNeverDowngrades(t *testing.T) {
	th := newThief(t)
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plain":
			// its own host and port, over http
			http.Redirect(w, r, "http://"+r.Host+"/x", http.StatusTemporaryRedirect)
		case "/elsewhere":
			http.Redirect(w, r, th.URL+"/steal", http.StatusPermanentRedirect)
		}
	}))
	defer vendor.Close()
	p := Provider{ID: "v", Chat: vendor.URL}
	for _, path := range []string{"/plain", "/elsewhere"} {
		res, err := p.Do(vendor.Client(), keyedRequest(t, "POST", vendor.URL+path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode/100 != 3 {
			t.Errorf("%s: %d, want the vendor's 3xx as it came", path, res.StatusCode)
		}
	}
	if n := th.reached(); n != 0 {
		t.Fatalf("an https vendor's redirect to http was followed %d times", n)
	}
}

func TestSameOrigin(t *testing.T) {
	for _, c := range []struct {
		from, host, to, toHost string
		want                   bool
	}{
		{"https", "api.example", "https", "api.example", true},
		{"https", "api.example", "https", "API.example:443", true},
		{"https", "api.example:8443", "https", "api.example:8443", true},
		{"http", "api.example", "https", "api.example", true}, // an upgrade
		{"https", "api.example", "http", "api.example", false},
		{"https", "api.example:443", "http", "api.example:443", false},
		{"https", "api.example", "https", "api.example:8443", false},
		{"https", "api.example", "https", "evil.example", false},
		{"https", "api.example", "https", "x.api.example", false},
		{"http", "127.0.0.1:1", "http", "127.0.0.1:2", false},
		{"http", "[::1]:1", "http", "[::1]:1", true},
		{"https", "", "https", "", false},
	} {
		if got := SameOrigin(c.from, c.host, c.to, c.toHost); got != c.want {
			t.Errorf("SameOrigin(%s://%s → %s://%s) = %v", c.from, c.host, c.to, c.toHost, got)
		}
	}
}

// Keyed keeps a client's own redirect rule, and adds its own after it.
func TestKeyedKeepsTheClientsRule(t *testing.T) {
	asked := 0
	own := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { asked++; return http.ErrUseLastResponse }}
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/here" {
			http.Redirect(w, r, "/landed", http.StatusFound)
		}
	}))
	defer vendor.Close()
	res, err := Keyed(own).Get(vendor.URL + "/here")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if asked != 1 || res.StatusCode != http.StatusFound || own.CheckRedirect == nil {
		t.Fatalf("asked %d, %d", asked, res.StatusCode)
	}
}
