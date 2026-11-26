package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// tmr11235 on X, after Test on account… took the accounts that are off
// (af75743be): "用关闭的账号发请求的能力希望暴露给插件也能用". A plugin
// sends with X-Magpie-Account, and a key the user turned off was 404. Now
// it is sent the request alone, by its pin, its id or its name, while
// routing still leaves it out; its failure is the reply and rests nothing.
// A key held to limits is told no, and the vendor isn't asked.
func TestAccountPinnedOffKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	v := &keyVendor{bad: map[string]bool{}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "keyed", Name: "Keyed", Chat: up.URL + "/v1", Models: []string{"m"},
		Key: firstKey, Keys: []provider.KeyAccount{{Key: offKey, Name: "spare", Off: true}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	id := provider.KeyID(offKey)
	for _, pin := range []string{"keyed/" + id, id, "spare"} {
		v.asked = nil
		if code, body := keyedPost(t, s, "keyed/m", pin); code != 200 || !strings.Contains(body, "from "+offKey) {
			t.Fatalf("pinned %q to the key off: %d %s", pin, code, body)
		}
		if got := v.seen(); !slices.Equal(got, []string{offKey}) {
			t.Fatalf("pinned %q: keys asked %v, want the one off alone", pin, got)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Order) != 1 || r.Order[0].Who != "spare" || r.Order[0].Kind != "key" {
			t.Fatalf("pinned %q: route order %+v", pin, r.Order)
		}
	}
	// unpinned, routing still leaves it out
	v.asked = nil
	for range 3 {
		if code, body := keyedPost(t, s, "keyed/m", ""); code != 200 || !strings.Contains(body, "from "+firstKey) {
			t.Fatalf("unpinned: %d %s", code, body)
		}
	}
	if got := v.seen(); slices.Contains(got, offKey) {
		t.Fatalf("unpinned requests went to the key off: %v", got)
	}
	// refused, its own 401 is the answer
	v.bad[offKey] = true
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", "keyed/"+id); code != http.StatusUnauthorized || !strings.Contains(body, "Incorrect API key") {
		t.Fatalf("the key off, refused: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{offKey}) {
		t.Fatalf("keys asked %v, want the one off alone", got)
	}
	delete(v.bad, offKey)
	// limited till a time its vendor named, which rests a key on (the last
	// one left), it rests for nothing: once it is turned on it is asked as
	// any other
	v.limited = map[string]bool{offKey: true}
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", "keyed/"+id); code != http.StatusTooManyRequests || !strings.Contains(body, "Rate limit") {
		t.Fatalf("the key off, limited: %d %s", code, body)
	}
	if got := v.seen(); !slices.Equal(got, []string{offKey}) {
		t.Fatalf("keys asked %v, want the one off alone", got)
	}
	for _, k := range []string{"keyed#" + id, "keyed#" + id + "/m", "keyed", "keyed/m"} {
		if r, ok := restOf(k); ok {
			t.Fatalf("%s rests after the key off was limited: %+v", k, r)
		}
	}
	v.limited = nil
	// a name nothing has is still 404, and asks nobody
	v.asked = nil
	if code, body := keyedPost(t, s, "keyed/m", "nobody"); code != http.StatusNotFound {
		t.Fatalf("pinned to none: %d %s", code, body)
	}
	if got := v.seen(); len(got) != 0 {
		t.Fatalf("asked %v for a pin naming none", got)
	}

	// from the local network: magpie's key held to nothing reaches it; a
	// key held to models is told no
	keys, secrets := newCaller(t, "Owner", "Models")
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"keyed/*"}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(s.Handler())
	lan := func(secret string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"keyed/m","messages":[{"role":"user","content":"ping"}]}`))
		req.RemoteAddr = "192.168.1.9:5000"
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set(AccountHeader, "keyed/"+id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	v.asked = nil
	if code, body := lan(secrets[0]); code != 200 || !strings.Contains(body, "from "+offKey) {
		t.Fatalf("a key held to nothing, pinned to the key off: %d %s", code, body)
	}
	v.asked = nil
	if code, body := lan(secrets[1]); code != http.StatusForbidden || !strings.Contains(body, "turned off") {
		t.Fatalf("a held key, pinned to the key off: %d %s", code, body)
	}
	if got := v.seen(); len(got) != 0 {
		t.Fatalf("a held key's pin to the key off asked %v", got)
	}
}

// A signed-in account turned off is pinned as a key is: Codex's second
// account, off in magpie, by its user and by its listed pin.
func TestAccountPinnedOffLogin(t *testing.T) {
	heads := twoAccounts(t)
	if err := provider.SetLoginOn("codex", "spare@example.com", false); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, pin := range []string{"spare@example.com", "codex/" + provider.LoginID("codex", "spare@example.com")} {
		*heads = nil
		code, body := pinnedPost(t, s, "/v1/responses", "codex/gpt-5.5", pin)
		if code != 200 || !strings.Contains(body, "from acct-2") {
			t.Fatalf("pinned %q: %d %s", pin, code, body)
		}
		if len(*heads) != 1 || (*heads)[0].Get("chatgpt-account-id") != "acct-2" {
			t.Fatalf("pinned %q: upstream %v", pin, *heads)
		}
	}
	*heads = nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer magpie")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from acct-1") {
		t.Fatalf("unpinned: %d %s", rec.Code, rec.Body.String())
	}
}

// The ask itself: a plugin's account the user turned off (a fake plugin,
// two API keys signed in) is listed on:false, and a request pinned to it
// by the listed pin or its user reaches the plugin's fetch with that
// account's key; one unpinned goes to the account on.
func TestPluginAccountPinnedOff(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var seen []string
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+r.Header.Get("Authorization")+`"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k1", "k2"} {
		if _, err := provider.PluginAPIKey(ctx, "fakeco", 0, nil, k); err != nil {
			t.Fatal(err)
		}
	}
	ls := provider.Logins("fakeco")
	if len(ls) != 2 || ls[1].Active {
		t.Fatalf("accounts %+v, want a second beside the first", ls)
	}
	second := ls[1].User
	if err := provider.SetLoginOn("fakeco", second, false); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fakeco")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.AlsoOn()); n != 0 {
		t.Fatalf("%d accounts on beside the first, want the second off", n)
	}
	// Test on account… offers it too, marked off
	if !slices.ContainsFunc(p.TestPicks(), func(k provider.TestPick) bool { return k.ID == second && k.Off }) {
		t.Fatalf("test picks %+v lack %s off", p.TestPicks(), second)
	}
	s := New()
	// the pin the account list gives it, as a plugin reads it
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", AccountsPath+"?provider=fakeco", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	s.Handler().ServeHTTP(rec, req)
	var list struct {
		Data []listedAccount `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	pin := ""
	for _, a := range list.Data {
		if a.User == second && !a.On {
			pin = a.Pin
		}
	}
	if pin == "" {
		t.Fatalf("the account list doesn't tell %s off: %s", second, rec.Body.String())
	}
	post := func(account string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fakeco/fake-1","messages":[{"role":"user","content":"ping"}]}`))
		req.Header.Set("Authorization", "Bearer magpie")
		if account != "" {
			req.Header.Set(AccountHeader, account)
		}
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, a := range []string{pin, second} {
		mu.Lock()
		seen = nil
		mu.Unlock()
		if code, body := post(a); code != 200 || !strings.Contains(body, "from Bearer k2") {
			t.Fatalf("pinned %q to the plugin's account off: %d %s", a, code, body)
		}
		mu.Lock()
		got := slices.Clone(seen)
		mu.Unlock()
		if !slices.Equal(got, []string{"Bearer k2"}) {
			t.Fatalf("pinned %q: the vendor saw %v, want the account off alone", a, got)
		}
	}
	mu.Lock()
	seen = nil
	mu.Unlock()
	if code, body := post(""); code != 200 || !strings.Contains(body, "from Bearer k1") {
		t.Fatalf("unpinned: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(seen, "Bearer k2") {
		t.Fatalf("an unpinned request reached the account off: %v", seen)
	}
}

// The ask as the reporter meant it: a plugin, running in magpie's plugin
// host, sends a request with an account turned off. The fake plugin
// ($FAKE_PIN_OFF) lists the accounts on $MAGPIE_ADDR, as the host tells
// it, takes the key that is off, and sends its request on through the
// gateway pinned to it. The key off answers; the one on, and the plugin's
// own vendor, are never asked.
func TestPluginSendsWithAKeyOff(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var asked []string
	keyed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key)
		mu.Unlock()
		if !strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(keyed.Close)
	if err := provider.Save(provider.Provider{ID: "keyed", Name: "Keyed", Chat: keyed.URL + "/v1", Models: []string{"m"},
		Key: firstKey, Keys: []provider.KeyAccount{{Key: offKey, Name: "spare", Off: true}}}); err != nil {
		t.Fatal(err)
	}
	own := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the plugin's own vendor was asked %s, not magpie's gateway", r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(own.Close)
	s := New()
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	t.Setenv("MAGPIE_ADDR", gw.Listener.Addr().String())
	t.Setenv("FAKE_PIN_OFF", "keyed/m")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PluginAPIKey(ctx, "fakeco", 0, nil, "k1"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fakeco/fake-1","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer magpie")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from "+offKey) {
		t.Fatalf("a plugin sending with the key off: %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{offKey}) {
		t.Fatalf("keys asked %v, want the one off alone", asked)
	}
}
