package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// arkNative is Ark's API on a test server for the native route: it
// records each request and answers a stream as events.
type arkNative struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newArkNative(t *testing.T) *arkNative {
	t.Helper()
	a := &arkNative{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.seen = append(a.seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+string(body))
		a.mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: image_generation.partial_succeeded\ndata: {\"url\":\"https://example.com/1.png\"}\n\n")
			w.(http.Flusher).Flush()
			io.WriteString(w, "event: image_generation.completed\ndata: {}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cgt-1","items":[]}`)
	}))
	t.Cleanup(a.Close)
	return a
}

func arkNativeMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	for _, m := range []string{"POST ", "GET ", "DELETE "} {
		mux.HandleFunc(m+NativePath+"{provider}/{path...}", s.native)
	}
	return mux
}

func arkNativeAsk(t *testing.T, h http.Handler, who access.Identity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(access.WithIdentity(req.Context(), who))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The native route passes Ark's own images and video tasks on, signed
// with the provider's key, its stream as it comes; nothing else of
// Ark's, and no key held to models, accounts or a limit.
func TestNativeArkRoute(t *testing.T) {
	fresh(t)
	up := newArkNative(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: up.URL + "/api/v3", Key: "key", Models: []string{"doubao-seedream-4-5-251128"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: up.URL + "/v1", Key: "key2", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	h := arkNativeMux(New())
	free := access.Identity{KeyID: "desk", KeyName: "Desk"}

	rec := arkNativeAsk(t, h, free, "POST", NativePath+"ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"四季","stream":true,"seed":12345678901234567}`)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "image_generation.completed") {
		t.Fatalf("stream: %d %q %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = arkNativeAsk(t, h, free, "GET", NativePath+"ark/contents/generations/tasks?page_size=3&filter.status=succeeded", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = arkNativeAsk(t, h, free, "DELETE", NativePath+"ark/contents/generations/tasks/cgt-1", "")
	if rec.Code != 200 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	up.mu.Lock()
	seen := append([]string(nil), up.seen...)
	up.mu.Unlock()
	want := []string{
		`POST /api/v3/images/generations Bearer key {"model":"doubao-seedream-4-5-251128","prompt":"四季","seed":12345678901234567,"stream":true}`,
		`GET /api/v3/contents/generations/tasks?page_size=3&filter.status=succeeded Bearer key `,
		`DELETE /api/v3/contents/generations/tasks/cgt-1 Bearer key `,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Ark was asked\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}

	for _, c := range []struct {
		who          access.Identity
		method, path string
		code         int
	}{
		{free, "POST", NativePath + "ark/chat/completions", 404},
		{free, "GET", NativePath + "ark/images/generations", 404},
		{free, "GET", NativePath + "ark/contents/generations/tasks/a/b", 404},
		{free, "POST", NativePath + "other/images/generations", 404},
		{free, "POST", NativePath + "nobody/images/generations", 404},
		{access.Identity{KeyName: "held", Models: []string{"ark/doubao-seedream-4-5-251128"}}, "POST", NativePath + "ark/images/generations", 403},
		{access.Identity{KeyName: "held", Accounts: []string{"ark"}}, "POST", NativePath + "ark/images/generations", 403},
		{access.Identity{KeyName: "held", Limit: &access.Limit{}}, "POST", NativePath + "ark/images/generations", 403},
		{access.Identity{KeyName: "issued", Issuer: &access.Identity{KeyName: "boss", Limit: &access.Limit{}}}, "POST", NativePath + "ark/images/generations", 403},
	} {
		up.mu.Lock()
		before := len(up.seen)
		up.mu.Unlock()
		rec := arkNativeAsk(t, h, c.who, c.method, c.path, `{"model":"doubao-seedream-4-5-251128","prompt":"x"}`)
		up.mu.Lock()
		asked := len(up.seen) > before
		up.mu.Unlock()
		if rec.Code != c.code || asked {
			t.Errorf("%s %s as %+v: %d (Ark asked: %v) %s", c.method, c.path, c.who, rec.Code, asked, rec.Body)
		}
	}
}

// A stream reaches the caller as Ark sends it: the first event before
// Ark's last, not the whole answer once it ends.
func TestNativeArkStreamsAsItComes(t *testing.T) {
	fresh(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: image_generation.partial_succeeded\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		select { // until the caller has the first event
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "event: image_generation.completed\ndata: {}\n\n")
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: up.URL + "/api/v3", Key: "key", Models: []string{"doubao-seedream-4-5-251128"}}); err != nil {
		t.Fatal(err)
	}
	mux := arkNativeMux(New())
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: "desk", KeyName: "Desk"})))
	}))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+NativePath+"ark/images/generations", strings.NewReader(`{"model":"doubao-seedream-4-5-251128","prompt":"四季","stream":true}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "partial_succeeded") {
		t.Fatalf("the first event didn't come while Ark held the rest: %q %v", line, err)
	}
	close(release)
}
