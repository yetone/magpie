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
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// vendorSeen is one request a vendor's API was sent.
type vendorSeen struct {
	method, uri string
	header      http.Header
	body        string
}

// vendorAPI is a vendor's own API on a test server: it records each
// request and answers JSON, or what answer says for the path.
type vendorAPI struct {
	*httptest.Server
	mu     sync.Mutex
	seen   []vendorSeen
	answer func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

func newVendorAPI(t *testing.T) *vendorAPI {
	t.Helper()
	v := &vendorAPI{}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.seen = append(v.seen, vendorSeen{r.Method, r.URL.RequestURI(), r.Header.Clone(), string(body)})
		answer := v.answer
		v.mu.Unlock()
		if answer != nil && answer(w, r, body) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Vendor", "yes")
		w.Header().Set("Set-Cookie", "vendor=1")
		w.WriteHeader(201)
		io.WriteString(w, `{"echo":`+strconvQuote(string(body))+`}`)
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *vendorAPI) asked() []vendorSeen {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]vendorSeen(nil), v.seen...)
}

// nativeServe is the gateway as it listens: lanGuard, then every guard
// Handler puts before a route.
func nativeServe(t *testing.T) http.Handler {
	t.Helper()
	return lanGuard(New().Handler())
}

func nativeDo(h http.Handler, method, target, remote, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func saveVendor(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// Every method reaches the vendor at its host, at the path the caller
// wrote (not under the base's /v1), with the query and body as they
// came, signed with the provider's key the way it takes one; the
// caller's own credentials, its gateway ?key= and the browser's headers
// never go on, and the answer comes back with the vendor's status and
// headers, its cookies left out.
func TestNativeAnyForwardsAtTheHost(t *testing.T) {
	fresh(t)
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	saveVendor(t, provider.Provider{ID: "claudeish", Name: "Claudeish", Anthropic: up.URL + "/anthropic", Key: "ak", Models: []string{"c"}})
	h := nativeServe(t)
	body := `{"model":"m","seed":12345678901234567,"x":[1,2.50,"é<>&"]}`
	for _, m := range nativeMethods {
		b := body
		if m == http.MethodGet {
			b = ""
		}
		rec := nativeDo(h, m, "http://127.0.0.1:3425"+NativePath+"vendor/v2/files/f-1?y=1&key=gw-secret&z=%2F", "127.0.0.1:50000", b,
			"Authorization", "Bearer caller-secret", "X-Api-Key", "caller-x", "Cookie", "c=1", "X-Custom", "kept", "Content-Type", "application/json")
		if rec.Code != 201 || rec.Header().Get("X-Vendor") != "yes" || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d %v %s", m, rec.Code, rec.Header(), rec.Body)
		}
	}
	seen := up.asked()
	if len(seen) != len(nativeMethods) {
		t.Fatalf("vendor asked %d times: %+v", len(seen), seen)
	}
	for i, s := range seen {
		m := nativeMethods[i]
		want := body
		if m == http.MethodGet {
			want = ""
		}
		if s.method != m || s.uri != "/v2/files/f-1?y=1&z=%2F" || s.body != want {
			t.Errorf("%s: vendor got %s %s %q", m, s.method, s.uri, s.body)
		}
		if s.header.Get("Authorization") != "Bearer vk" || s.header.Get("X-Api-Key") != "" || s.header.Get("Cookie") != "" ||
			s.header.Get("X-Custom") != "kept" || strings.Contains(strings.Join(flatHeader(s.header), "\n"), "caller") {
			t.Errorf("%s: vendor's headers %v", m, s.header)
		}
	}

	rec := nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+"claudeish/v1/messages/count_tokens", "127.0.0.1:50000", `{"model":"c"}`, "Authorization", "Bearer caller-secret")
	seen = up.asked()
	last := seen[len(seen)-1]
	if rec.Code != 201 || last.uri != "/v1/messages/count_tokens" || last.header.Get("X-Api-Key") != "ak" || strings.Contains(strings.Join(flatHeader(last.header), "\n"), "caller") {
		t.Fatalf("anthropic: %d %+v", rec.Code, last)
	}
}

func flatHeader(h http.Header) []string {
	var out []string
	for k, vs := range h {
		out = append(out, k+": "+strings.Join(vs, ","))
	}
	return out
}

// What a request costs is on record: a usage record for each, with the
// tokens the answer says (a stream's last event here) and none when it
// says none.
func TestNativeAnyRecordsUsage(t *testing.T) {
	fresh(t)
	up := newVendorAPI(t)
	up.answer = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path != "/v1/chat/completions" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","model":"m-2","choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","model":"m-2","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return true
	}
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	h := nativeServe(t)
	if rec := nativeDo(h, "GET", "http://127.0.0.1:3425"+NativePath+"vendor/v1/models", "127.0.0.1:50000", ""); rec.Code != 201 {
		t.Fatalf("models: %d %s", rec.Code, rec.Body)
	}
	if rec := nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+"vendor/v1/chat/completions", "127.0.0.1:50000", `{"model":"m","stream":true}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	var recs []usage.Record
	for i := 0; i < 100; i++ {
		if recs = usage.Load(time.Time{}); len(recs) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var plain, chat *usage.Record
	for i := range recs {
		switch {
		case recs[i].Operation == "native" && recs[i].Status == 201:
			plain = &recs[i]
		case recs[i].Operation == "native" && recs[i].Status == 200:
			chat = &recs[i]
		}
	}
	if plain == nil || plain.Provider != "vendor" || plain.Input != 0 || plain.Output != 0 {
		t.Fatalf("models record %+v in %+v", plain, recs)
	}
	if chat == nil || chat.Model != "m" || chat.Input != 7 || chat.Output != 3 || chat.ProviderKeyID != provider.KeyID("vk") {
		t.Fatalf("chat record %+v in %+v", chat, recs)
	}
}

// Only a provider reached with an API key is served: a subscription's
// or a sign-in's account, a remote magpie's gateway key, or no key at
// all never; a key held to models, accounts or a limit not on any path.
func TestNativeAnyRefusals(t *testing.T) {
	fresh(t)
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	saveVendor(t, provider.Provider{ID: "keyless", Name: "Keyless", Chat: up.URL + "/v1", Models: []string{"m"}})
	saveVendor(t, provider.Provider{ID: "vendor-off", Name: "Off", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}, Off: true})
	mux := arkNativeMux(New())
	free := access.Identity{KeyID: "desk", KeyName: "Desk"}
	for _, c := range []struct {
		who          access.Identity
		method, path string
		code         int
	}{
		{free, "POST", NativePath + "keyless/v1/chat/completions", 403},
		{free, "GET", NativePath + "nobody/v1/models", 404},
		{free, "GET", NativePath + "vendor-off/v1/models", 404},
		{access.Identity{KeyName: "held", Models: []string{"vendor/m"}}, "GET", NativePath + "vendor/v1/models", 403},
		{access.Identity{KeyName: "held", Accounts: []string{"vendor"}}, "PUT", NativePath + "vendor/v1/x", 403},
		{access.Identity{KeyName: "held", Limit: &access.Limit{}}, "DELETE", NativePath + "vendor/v1/files/f", 403},
		{access.Identity{KeyName: "issued", Issuer: &access.Identity{KeyName: "boss", Models: []string{"vendor/m"}}}, "PATCH", NativePath + "vendor/v1/x", 403},
	} {
		before := len(up.asked())
		rec := arkNativeAsk(t, mux, c.who, c.method, c.path, `{"model":"m"}`)
		if rec.Code != c.code || len(up.asked()) > before {
			t.Errorf("%s %s as %+v: %d (asked: %v) %s", c.method, c.path, c.who, rec.Code, len(up.asked()) > before, rec.Body)
		}
	}
	for _, c := range []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{ID: "a", Key: "k", Chat: "https://x/v1"}, true},
		{provider.Provider{ID: "a", Chat: "https://x/v1"}, false},
		{provider.Provider{ID: "a", Key: "k", Chat: "https://x/v1", Account: &provider.Account{}}, false},
	} {
		if got := nativeKeyed(c.p); got != c.want {
			t.Errorf("nativeKeyed(%+v) = %v", c.p, got)
		}
	}
}

// A path never leaves the provider's host: a "." or ".." segment,
// encoded or not, an encoded slash or backslash that would start
// another host or path, or a scheme, is refused before the vendor is
// asked.
func TestNativeAnyStaysOnTheHost(t *testing.T) {
	fresh(t)
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	h := nativeServe(t)
	for _, p := range []string{
		"a/../b", "a/./b", "a/%2e%2e/b", "a/%2E%2E/%2E%2E/admin", "..%2Fx", "%2e%2e%2fx", "a%2F..%2Fb", "%2e/x",
		"%2Fevil.example/x", "%2F%2Fevil.example/x", "%5Cevil.example/x", "a%5C..%5Cb",
		"https:%2F%2Fevil.example/x", "http:", "https:/evil.example", "javascript:x",
		"a%00b", "",
	} {
		before := len(up.asked())
		rec := nativeDo(h, "GET", "http://127.0.0.1:3425"+NativePath+"vendor/"+p, "127.0.0.1:50000", "")
		if rec.Code < 300 || len(up.asked()) > before {
			t.Errorf("%q: %d (asked: %+v) %s", p, rec.Code, up.asked()[before:], rec.Body)
		}
	}
	// and the target itself, for what the mux would clean first
	for _, c := range []struct {
		raw  string
		want string
	}{
		{"v2/x", "https://api.example/v2/x?q=1"},
		{"v1/models/gemini-2.5-pro:generateContent", "https://api.example/v1/models/gemini-2.5-pro:generateContent?q=1"},
		{"v1/files/a%2Fb", "https://api.example/v1/files/a%2Fb?q=1"},
		{"a/../b", ""}, {"./a", ""}, {"a/.", ""}, {"/evil.example/x", ""}, {"%2F%2Fevil.example", ""},
		{"evil.example%3A80@x/y", ""}, {"v1/x%3A80@evil/y", "https://api.example/v1/x%3A80@evil/y?q=1"}, {"mailto:a@b/x", ""}, {"http://evil.example/x", ""}, {"a\\b", ""}, {"%zz", ""},
	} {
		u, err := nativeTarget("https://api.example/v1", c.raw, "q=1")
		got := ""
		if err == nil {
			got = u.String()
		}
		if got != c.want {
			t.Errorf("nativeTarget(%q) = %q, %v; want %q", c.raw, got, err, c.want)
		}
	}
	if _, err := nativeTarget("unix:///sock", "x", ""); err == nil {
		t.Error("a base with no http host was taken")
	}
	if got := nativeQuery("a=1&key=gw&b=%6Bey&k%65y=x&keys=2"); got != "a=1&b=%6Bey&keys=2" {
		t.Errorf("nativeQuery = %q", got)
	}
}

// Without a gateway key, a page the gateway doesn't trust reaches no
// vendor through the native route, by any method: a foreign Origin, a
// sandboxed frame's null one, a *.localhost page (WebKit asks DNS for
// it), a page rebound onto 127.0.0.1 under its own name, or another
// machine.
func TestNativeAnyRefusesPagesWithoutAKey(t *testing.T) {
	fresh(t)
	newCaller(t, "desk")
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	h := nativeServe(t)
	chrome := []string{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/27.2 Safari/605.1.15", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"}
	for _, c := range []struct {
		name, remote, host string
		hdr                []string
	}{
		{"a foreign page", "127.0.0.1:50123", "127.0.0.1:3425", append([]string{"Origin", "https://evil.example"}, chrome...)},
		{"Origin null", "127.0.0.1:50123", "127.0.0.1:3425", append([]string{"Origin", "null"}, chrome...)},
		{"a localhost subdomain", "127.0.0.1:50123", "127.0.0.1:3425", append([]string{"Origin", "http://a.localhost"}, chrome...)},
		{"a rebound page", "127.0.0.1:50123", "rebind.attacker.example:3425", chrome},
		{"a rebound page's POST", "127.0.0.1:50123", "rebind.attacker.example:3425", append([]string{"Origin", "http://rebind.attacker.example:3425"}, chrome...)},
		{"another machine", "192.168.1.20:50123", "192.168.1.10:3425", nil},
	} {
		for _, m := range nativeMethods {
			before := len(up.asked())
			rec := nativeDo(h, m, "http://"+c.host+NativePath+"vendor/v1/chat/completions", c.remote, `{"model":"m"}`, c.hdr...)
			if rec.Code != 403 && rec.Code != 401 || len(up.asked()) > before {
				t.Errorf("%s %s: %d (asked: %v) %s", c.name, m, rec.Code, len(up.asked()) > before, rec.Body)
			}
		}
	}
	// the same route from this computer's own caller is served
	if rec := nativeDo(h, "GET", "http://127.0.0.1:3425"+NativePath+"vendor/v1/models", "127.0.0.1:50123", "", "User-Agent", "curl/8"); rec.Code != 201 {
		t.Fatalf("own caller: %d %s", rec.Code, rec.Body)
	}
}

// With Settings › Privacy masking, a secret in the body never reaches
// the vendor and comes back in the answer; a body that isn't JSON can't
// be masked, so it isn't sent.
func TestNativeAnyMasks(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	up := newVendorAPI(t)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	h := nativeServe(t)
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	rec := nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+"vendor/v2/notes", "127.0.0.1:50000", `{"model":"m","text":"key `+secret+`"}`)
	seen := up.asked()
	if rec.Code != 201 || len(seen) != 1 || strings.Contains(seen[0].body, secret) || !strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("%d %s; vendor got %+v", rec.Code, rec.Body, seen)
	}
	rec = nativeDo(h, "POST", "http://127.0.0.1:3425"+NativePath+"vendor/v2/upload", "127.0.0.1:50000", "raw "+secret)
	if rec.Code != 415 || len(up.asked()) != 1 {
		t.Fatalf("not JSON: %d %s; vendor asked %d", rec.Code, rec.Body, len(up.asked()))
	}
}

// An answer reaches the caller as the vendor sends it: a stream's first
// event, and the first part of a large JSON answer, before the vendor's
// last byte.
func TestNativeAnyStreamsAsItComes(t *testing.T) {
	fresh(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/big" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"items":["`+strings.Repeat("a", 64<<10))
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: first\ndata: {}\n\n")
		}
		w.(http.Flusher).Flush()
		select { // until the caller has the first part
		case <-release:
		case <-r.Context().Done():
		}
		if r.URL.Path == "/v2/big" {
			io.WriteString(w, `"]}`)
		} else {
			io.WriteString(w, "event: last\ndata: {}\n\n")
		}
	}))
	t.Cleanup(up.Close)
	saveVendor(t, provider.Provider{ID: "vendor", Name: "Vendor", Chat: up.URL + "/v1", Key: "vk", Models: []string{"m"}})
	gw := httptest.NewServer(lanGuard(New().Handler()))
	t.Cleanup(gw.Close)
	for _, path := range []string{"v2/stream", "v2/big"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+NativePath+"vendor/"+path, strings.NewReader(`{"model":"m"}`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		got := make(chan string, 1)
		go func() {
			if path == "v2/big" {
				b := make([]byte, 32<<10)
				n, _ := io.ReadFull(res.Body, b)
				got <- string(b[:n])
				return
			}
			line, _ := bufio.NewReader(res.Body).ReadString('\n')
			got <- line
		}()
		select {
		case first := <-got:
			if (path == "v2/stream" && first != "event: first\n") || (path == "v2/big" && !strings.HasPrefix(first, `{"items":["aaa`)) {
				t.Errorf("%s: first part %.40q", path, first)
			}
		case <-ctx.Done():
			t.Errorf("%s: nothing came before the vendor's last byte", path)
		}
		release <- struct{}{}
		res.Body.Close()
		cancel()
	}
}

// Ark keeps its first routes (its short paths under /api/v3, as before,
// in ark_native_test.go) and reaches the rest of its API at its host.
func TestNativeAnyArkHost(t *testing.T) {
	fresh(t)
	up := newVendorAPI(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	saveVendor(t, provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: up.URL + "/api/v3", Key: "key", Models: []string{"doubao"}})
	h := arkNativeMux(New())
	free := access.Identity{KeyID: "desk", KeyName: "Desk"}
	if rec := arkNativeAsk(t, h, free, "POST", NativePath+"ark/images/generations", `{"model":"doubao","prompt":"x"}`); rec.Code != 201 {
		t.Fatalf("short path: %d %s", rec.Code, rec.Body)
	}
	if rec := arkNativeAsk(t, h, free, "POST", NativePath+"ark/api/v3/chat/completions", `{"model":"doubao"}`); rec.Code != 201 {
		t.Fatalf("host path: %d %s", rec.Code, rec.Body)
	}
	if rec := arkNativeAsk(t, h, free, "GET", NativePath+"ark/api/v3/contents/generations/tasks/a/b", ``); rec.Code != 201 {
		t.Fatalf("host GET: %d %s", rec.Code, rec.Body)
	}
	var uris []string
	for _, s := range up.asked() {
		uris = append(uris, s.method+" "+s.uri)
	}
	want := "POST /api/v3/images/generations\nPOST /api/v3/chat/completions\nGET /api/v3/contents/generations/tasks/a/b"
	if strings.Join(uris, "\n") != want {
		t.Fatalf("Ark was asked\n%s\nwant\n%s", strings.Join(uris, "\n"), want)
	}
}
