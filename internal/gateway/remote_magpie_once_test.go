package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// YeYaka88 on X: magpie on a laptop with another computer's magpie as a
// Remote magpie provider ran its gateway, routing and masking, and then
// the remote ran each again. A request is masked once, where it is sent
// from, and a failure the remote answered after its own retries and
// patience isn't sent there again by this magpie's.

// maskOnceSetup is a vendor ("fake", Chat) on a remote magpie served in
// this process, and "office", the Remote magpie provider for it; both
// mask by the same settings, as they share them here. seen is what the
// vendor was sent; masked counts the masker's passes.
func maskOnceSetup(t *testing.T) (remote *httptest.Server, seen func() (string, http.Header), masked *atomic.Int32) {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var body string
	var header http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body, header = string(b), r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true, RedactWords: []string{"acmecorp"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	masked = scanCount(t)
	remote = httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	return remote, func() (string, http.Header) {
		mu.Lock()
		defer mu.Unlock()
		return body, header
	}, masked
}

const maskOnceBody = `{"model":"office/fake/m1","messages":[{"role":"user","content":"a note about acmecorp"}]}`

func TestRemoteMagpieMasksOnce(t *testing.T) {
	_, seen, masked := maskOnceSetup(t)
	code, reply := postAs(t, New(), "", maskOnceBody)
	if code != 200 || !strings.Contains(reply, "done") {
		t.Fatalf("%d %s", code, reply)
	}
	body, header := seen()
	if strings.Contains(body, "acmecorp") {
		t.Fatalf("the vendor was sent the word unmasked: %s", body)
	}
	if n := masked.Load(); n != 1 {
		t.Errorf("the request was masked %d times on its way, want once (here, not again on the remote magpie)", n)
	}
	if v := header.Get(MaskedAsHeader); v != "" {
		t.Errorf("the vendor was sent %s: %q", MaskedAsHeader, v)
	}
}

// The remote masks a request again unless a magpie says it masked it by
// what the remote masks by: one masked by other settings, or another
// build's masker, is masked again, and so is a request that only names
// the header without being a magpie's.
func TestRemoteMagpieMasksWhatItIsNotToldWasMasked(t *testing.T) {
	remote, seen, masked := maskOnceSetup(t)
	send := func(ua, as string) string {
		t.Helper()
		req, _ := http.NewRequest("POST", remote.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"a note about acmecorp"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		if as != "" {
			req.Header.Set(MaskedAsHeader, as)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
		body, _ := seen()
		return body
	}
	own := ownMaskedAs(context.Background())
	if own == "" {
		t.Fatal("no fingerprint with masking on")
	}
	for _, c := range []struct{ name, ua, as string }{
		{"no header", magpieUA(), ""},
		{"other settings", magpieUA(), "0.0.1/" + strings.Repeat("0", 32)},
		{"not a magpie's request", "curl/8.0", own},
	} {
		masked.Store(0)
		if body := send(c.ua, c.as); strings.Contains(body, "acmecorp") || masked.Load() != 1 {
			t.Errorf("%s: masked %d times, the vendor sent %s", c.name, masked.Load(), body)
		}
	}
	// told by a magpie that masks alike, it goes on as it came
	masked.Store(0)
	send(magpieUA(), own)
	if n := masked.Load(); n != 0 {
		t.Errorf("masked again %d times, told it was masked by the same settings", n)
	}
	// another build's header, the same settings: not masked again either
	_, fp, _ := strings.Cut(own, "/")
	masked.Store(0)
	send(magpieUA(), "v0.1.3638/"+fp)
	if n := masked.Load(); n != 0 {
		t.Errorf("masked again %d times, told so by another build with the same settings", n)
	}
}

// One agent request with the remote's vendor busy: the remote's own
// retries and patience answer it, and its error, named as its provider's
// (providerHeader), isn't asked again here, where each ask ran all of
// them again (35 asks of the vendor over 174s for one request).
func TestRemoteMagpieRoutedFailureNotAskedAgain(t *testing.T) {
	fresh(t)
	var asked atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(providerHeader, "fake")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":{"message":"Fake: overloaded","type":"server_error"}}`)
	}))
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "k",
		Chat: remote.URL + "/v1", Models: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := postAs(t, New(), "", `{"model":"office/fake/m1","messages":[{"role":"user","content":"hi"}]}`); code != 503 {
		t.Fatalf("%d %s", code, body)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("the remote magpie was asked %d times for one request, its own retries and patience spent each time; want once", n)
	}
}

// A request masked by another build of magpie with the same settings is
// not masked again, whichever build is newer (Luxzinw on X); one from a
// build before the masker last changed, or a header that isn't a build's,
// is.
func TestMaskedBeforeAcrossBuilds(t *testing.T) {
	fresh(t)
	own := "v0.1.3800/" + strings.Repeat("a", 32)
	other := strings.Repeat("b", 32)
	for _, c := range []struct {
		told string
		want bool
	}{
		{own, true},
		{"v0.1.3771/" + strings.Repeat("a", 32), true},       // older build, as new a masker
		{"0.1.3638/" + strings.Repeat("a", 32), true},        // no v
		{"v0.1.3900-dirty/" + strings.Repeat("a", 32), true}, // newer build
		{"v0.1.3583/" + strings.Repeat("a", 32), false},      // masker found less
		{"v0.1.100/" + strings.Repeat("a", 32), false},
		{"dev/" + strings.Repeat("a", 32), false},
		{"v0.1.3771/" + other, false}, // other settings
		{"v0.1.3771/", false},
		{"nonsense", false},
		{"", false},
	} {
		if got := sameMasking(own, c.told); got != c.want {
			t.Errorf("told %q: %v, want %v", c.told, got, c.want)
		}
	}
}
