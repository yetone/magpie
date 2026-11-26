package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newAPIPage is the web app new-api's NoRoute serves, 200 text/html, for
// any path outside /v1, /api and /assets, POSTs included (router/web-router.go).
const newAPIPage = `<!doctype html><html lang="zh"><head><meta charset="utf-8" /><title>New API</title><script type="module" crossorigin src="/assets/index.js"></script></head><body><div id="root"></div></body></html>`

// newAPIRelay answers as new-api routes: OpenAI's API under /v1 (chat,
// Responses and the model list), the key sk-good taken and any other
// refused 401, and its site's page for everything else.
func newAPIRelay(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer sk-good" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"无效的令牌","type":"new_api_error"}}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
		case "POST /v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`))
		case "GET /v1/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"},{"id":"claude-sonnet-4-5","object":"model"}]}`))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(newAPIPage))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// sarosaka1023 on X: a relay (new-api) added at its root, no /v1. Its
// site answers the root's /chat/completions 200 with its page, which was
// taken for the route being there, so the base was kept as typed and
// every request met the page. The list is found under /v1, the page is
// no route, and the base is saved with its /v1.
func TestNewAPIRootListsUnderV1(t *testing.T) {
	crsHome(t)
	srv, asked := newAPIRelay(t)
	for _, x := range []struct {
		name string
		p    Provider
	}{
		{"chat", Provider{Chat: srv.URL}},
		{"slash", Provider{Chat: srv.URL + "/"}},
		{"responses", Provider{Responses: srv.URL}},
	} {
		t.Run(x.name, func(t *testing.T) {
			p := x.p
			p.ID, p.Name, p.Key = "relay-"+x.name, "Relay", "sk-good"
			if err := Save(p); err != nil {
				t.Fatal(err)
			}
			q, _ := Find(p.ID)
			ms, err := q.Fetch(context.Background())
			if err != nil || len(ms) == 0 {
				t.Fatalf("fetch: %v %v (asked %v)", ms, err, asked())
			}
			q, _ = Find(p.ID)
			if got := q.Chat + q.Responses; got != srv.URL+"/v1" {
				t.Errorf("base %q, want %s/v1", got, srv.URL)
			}
		})
	}
}

// Test models at the root: the page is no answer, the model is asked
// under /v1, answers there, and the base is saved with it.
func TestNewAPIRootTestsUnderV1(t *testing.T) {
	crsHome(t)
	srv, _ := newAPIRelay(t)
	for _, proto := range []Protocol{Chat, Responses} {
		t.Run(string(proto), func(t *testing.T) {
			p := Provider{ID: "relay-" + string(proto), Name: "Relay", Key: "sk-good", Models: []string{"gpt-5.5"}, ListOff: true}
			if proto == Chat {
				p.Chat = srv.URL
			} else {
				p.Responses = srv.URL
			}
			if err := Save(p); err != nil {
				t.Fatal(err)
			}
			q, _ := Find(p.ID)
			r := q.TestModels(context.Background(), []string{"gpt-5.5"})[0]
			if !r.OK || r.V1 != srv.URL+"/v1" {
				t.Fatalf("%+v", r)
			}
			if q, _ := Find(p.ID); q.Base(proto) != srv.URL+"/v1" {
				t.Errorf("base %q", q.Base(proto))
			}
		})
	}
}

// A key the API under /v1 refuses: its refusal is the answer, nothing is
// moved (the base isn't known to answer), and the error says where the API
// is.
func TestNewAPIRootBadKeySaysV1(t *testing.T) {
	crsHome(t)
	srv, _ := newAPIRelay(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL, Key: "sk-bad", Models: []string{"gpt-5.5"}, ListOff: true}); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("relay")
	r := q.TestModels(context.Background(), []string{"gpt-5.5"})[0]
	if r.OK || r.V1 != "" || r.Status != 401 || !strings.Contains(r.Error, "无效的令牌") || !strings.Contains(r.Error, "set the Base URL to "+srv.URL+"/v1") {
		t.Fatalf("%+v", r)
	}
	if q, _ := Find("relay"); q.Chat != srv.URL {
		t.Errorf("base moved to %q", q.Chat)
	}
}

// A base typed with its version, or one whose /v1 is a page too (a site
// with no API), is left as typed and says nothing of /v1.
func TestPageBaseKeptWhenNoAPIUnderV1(t *testing.T) {
	crsHome(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(newAPIPage))
	}))
	defer site.Close()
	for _, base := range []string{site.URL, site.URL + "/api/v3"} {
		if err := Save(Provider{ID: "site", Name: "Site", Chat: base, Key: "sk-good", Models: []string{"gpt-5.5"}, ListOff: true}); err != nil {
			t.Fatal(err)
		}
		q, _ := Find("site")
		r := q.TestModels(context.Background(), []string{"gpt-5.5"})[0]
		if r.OK || r.V1 != "" || strings.Contains(r.Error, "/v1") || !ServedPage(r.Error) {
			t.Errorf("%s: %+v", base, r)
		}
		if q, _ := Find("site"); q.Chat != base {
			t.Errorf("%s: base moved to %q", base, q.Chat)
		}
	}
}
