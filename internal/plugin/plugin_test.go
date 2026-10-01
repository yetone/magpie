package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// sandbox gives the test its own magpie folders and the Bun on PATH; a
// machine without Bun skips it.
func sandbox(t *testing.T) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(Settle)
}

func TestFakePlugin(t *testing.T) {
	sandbox(t)
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s %s\n\n", r.URL.Path, b)
		fl.Flush()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ls, err := Plugins(ctx)
	if err != nil || len(ls) != 1 || ls[0].Error != "" {
		t.Fatalf("Plugins = %+v, %v", ls, err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != "fakeco" || ps[0].Name != "FakeCo" || ps[0].SignedIn || len(ps[0].Methods) != 2 {
		t.Fatalf("Providers = %+v", ps)
	}
	npm := map[string]string{}
	for _, m := range ps[0].Models {
		npm[m.ID] = m.NPM
	}
	if npm["fake-1"] != "@ai-sdk/openai-compatible" || npm["fake-claude"] != "@ai-sdk/anthropic" || npm["fake-gemini"] != "@ai-sdk/google" {
		t.Fatalf("models = %+v", ps[0].Models)
	}
	// whether a model takes images is said only where the plugin said it
	for _, m := range ps[0].Models {
		if want := map[string][2]bool{"fake-1": {false, false}, "fake-claude": {true, true}, "fake-gemini": {false, true}}[m.ID]; m.Image != want[0] || m.ImageSaid != want[1] {
			t.Errorf("%s: image %v, said %v; want %v", m.ID, m.Image, m.ImageSaid, want)
		}
	}

	// the browser method asks where, then a team only for work
	p, err := NextPrompt(ctx, "fakeco", 1, map[string]string{})
	if err != nil || p == nil || p.Key != "where" || len(p.Options) != 2 {
		t.Fatalf("first prompt = %+v, %v", p, err)
	}
	if p, _ := NextPrompt(ctx, "fakeco", 1, map[string]string{"where": "home"}); p != nil {
		t.Fatalf("home asked %+v", p)
	}
	if p, _ := NextPrompt(ctx, "fakeco", 1, map[string]string{"where": "work"}); p == nil || p.Key != "team" {
		t.Fatalf("work asked %+v", p)
	}
	if e, _ := Validate(ctx, "fakeco", 1, "team", ""); e != "Required" {
		t.Fatalf("Validate = %q", e)
	}
	in := map[string]string{"where": "work", "team": "blue"}
	a, err := Authorize(ctx, "fakeco", 1, in, NewAccount)
	if err != nil || a.Method != "code" || !strings.Contains(a.URL, "where=work") {
		t.Fatalf("Authorize = %+v, %v", a, err)
	}
	if _, err := Finish(ctx, a.Session, "bad"); err != ErrFailed {
		t.Fatalf("a bad code: %v", err)
	}
	// the plugin's reason, where it tells one
	a, _ = Authorize(ctx, "fakeco", 1, in, NewAccount)
	if _, err := Finish(ctx, a.Session, "expired"); !errors.Is(err, ErrFailed) || err.Error() != "the sign-in failed: the sign-in page expired" {
		t.Fatalf("an expired sign-in: %v", err)
	}
	a, _ = Authorize(ctx, "fakeco", 1, in, NewAccount)
	if got, err := Finish(ctx, a.Session, "good"); err != nil || got != (Saved{"fakeco", "fakeco"}) {
		t.Fatalf("Finish = %+v, %v", got, err)
	}
	var saved map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if saved["fakeco"]["type"] != "oauth" || saved["fakeco"]["refresh"] != "r-blue" || saved["fakeco"]["accountId"] != "blue@fake" {
		t.Fatalf("saved %v", saved)
	}
	if fi, _ := os.Stat(AuthPath()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no mode bits
		t.Fatalf("plugin-auth.json is %v", fi.Mode().Perm())
	}
	if !SignedIn("fakeco") {
		t.Fatal("not signed in")
	}

	o, err := LoaderOptions(ctx, "fakeco", "")
	if err != nil || o.BaseURL != srv.URL+"/v1" || !o.Fetch || o.APIKey != "dummy" {
		t.Fatalf("LoaderOptions = %+v, %v", o, err)
	}
	res, err := Fetch(ctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: o.BaseURL + "/chat/completions", Method: "POST",
		Headers: map[string]string{"content-type": "application/json"},
		Body:    []byte(`{"model":"fake-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Fetch = %d %v %v", res.StatusCode, res.Header, err)
	}
	if want := "data: /v1/chat/completions {\"model\":\"fake-1\"}\n\ndata: [DONE]\n\n"; string(body) != want {
		t.Fatalf("body = %q", body)
	}
	h := seen[len(seen)-1]
	// the plugin refreshed the stale token and saved it; the chat.headers
	// hook added its header; the plugin saw the provider's models
	if h.Get("Authorization") != "Bearer fresh-r-blue" || h.Get("X-Plugin-Model") != "fake-1" || h.Get("X-Models") != "fake-1,fake-claude,fake-gemini" {
		t.Fatalf("headers = %v", h)
	}
	// the plugin's fetch got the body as a string, as OpenCode hands it
	if h.Get("X-Body-Type") != "string" {
		t.Fatalf("the plugin got a %s body", h.Get("X-Body-Type"))
	}
	b, _ = os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if saved["fakeco"]["access"] != "fresh-r-blue" {
		t.Fatalf("refresh not saved: %v", saved)
	}

	// a request that is abandoned is aborted, and the host still answers
	cctx, ccancel := context.WithCancel(ctx)
	res, err = Fetch(cctx, FetchRequest{Provider: "fakeco", Model: "fake-1", URL: o.BaseURL + "/chat/completions", Method: "POST", Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ccancel()
	io.ReadAll(res.Body)
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}

	if err := SignOut(ctx, "fakeco", ""); err != nil || SignedIn("fakeco") {
		t.Fatalf("SignOut: %v", err)
	}
	if err := SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	if ps := Cached(); len(ps) != 0 {
		t.Fatalf("a plugin turned off still lists %+v", ps)
	}
}

// A provider is signed in to more than once: each account is kept apart,
// its requests made with its own sign-in and its refreshes saved to it; a
// sign-in to an account already there replaces it.
func TestPluginAccounts(t *testing.T) {
	sandbox(t)
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		fmt.Fprint(w, "{}")
	}))
	defer srv.Close()
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	signIn := func(team string) Saved {
		t.Helper()
		a, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": team}, NewAccount)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Finish(ctx, a.Session, "good")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	blue, red := signIn("blue"), signIn("red")
	if blue.Account != "fakeco" || red.Provider != "fakeco" || !strings.HasPrefix(red.Account, "fakeco#") {
		t.Fatalf("accounts %+v %+v", blue, red)
	}
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || len(ps[0].Accounts) != 2 || ps[0].Accounts[0].AccountID != "blue@fake" || !reflect.DeepEqual(ps[0].Accounts[1], Account{Key: red.Account, Type: "oauth", AccountID: "red@fake", Models: ps[0].Accounts[0].Models}) || len(ps[0].Accounts[1].Models) == 0 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	if c := Cached(); len(c) != 1 || len(c[0].Accounts) != 2 || c[0].Accounts[1].Key != red.Account || c[0].AccountID != "blue@fake" {
		t.Fatalf("Cached = %+v", c)
	}

	fetch := func(account string) {
		t.Helper()
		res, err := Fetch(ctx, FetchRequest{Provider: "fakeco", Account: account, Model: "fake-1", URL: srv.URL + "/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(res.Body)
		res.Body.Close()
	}
	fetch(red.Account)
	fetch("")
	if len(auths) != 2 || auths[0] != "Bearer fresh-r-red" || auths[1] != "Bearer fresh-r-blue" {
		t.Fatalf("sent %v", auths)
	}
	var saved map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if saved[red.Account]["access"] != "fresh-r-red" || saved["fakeco"]["access"] != "fresh-r-blue" || len(saved) != 2 {
		t.Fatalf("saved %v", saved)
	}

	// red again: the same account, its sign-in replaced
	if again := signIn("red"); again != red {
		t.Fatalf("signed in again as %+v, not %+v", again, red)
	}
	b, _ = os.ReadFile(AuthPath())
	saved = nil
	json.Unmarshal(b, &saved)
	if len(saved) != 2 || saved[red.Account]["access"] != "stale" {
		t.Fatalf("after signing in again %v", saved)
	}

	if err := SignOut(ctx, "fakeco", red.Account); err != nil {
		t.Fatal(err)
	}
	if c := Cached(); len(c) != 1 || len(c[0].Accounts) != 1 || c[0].Accounts[0].Key != "fakeco" {
		t.Fatalf("after signing red out %+v", c)
	}
	// with the host stopped, too
	signIn("green")
	Restart()
	if err := SignOut(ctx, "fakeco", ""); err != nil || SignedIn("fakeco") {
		t.Fatalf("SignOut all: %v", err)
	}
}

// A plugin that fails to load (a broken update, its files gone) keeps its
// providers in sight, as it keeps its sign-ins: a provider moved onto it
// doesn't vanish as if the plugin were removed.
func TestUnloadedPluginKeepsProviders(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src, _ := os.ReadFile("testdata/fake/index.js")
	file := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(file, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, file); err != nil {
		t.Fatal(err)
	}
	if ps, err := Providers(ctx); err != nil || len(ps) != 1 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	if err := os.WriteFile(file, []byte("export default {{"), 0o644); err != nil {
		t.Fatal(err)
	}
	Restart()
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || ps[0].ID != "fakeco" {
		t.Fatalf("with the plugin failing to load, Providers = %+v, %v", ps, err)
	}
	if cs := Cached(); len(cs) != 1 || cs[0].ID != "fakeco" {
		t.Fatalf("Cached = %+v", cs)
	}
	// removed, it goes
	if err := Remove(ctx, file); err != nil {
		t.Fatal(err)
	}
	if ps, _ := Providers(ctx); len(ps) != 0 {
		t.Fatalf("after removing it, Providers = %+v", ps)
	}
}

// A request made for a provider or an account with its own proxy goes
// through it, the plugin's own fetches and its usage's among them; one
// set to go direct goes through none, though magpie's global proxy is in
// the host's env.
func TestPluginProxy(t *testing.T) {
	sandbox(t)
	var mu sync.Mutex
	var own, global []string // what each proxy was asked for
	proxy := func(seen *[]string, answer string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*seen = append(*seen, r.URL.String())
			mu.Unlock()
			fmt.Fprint(w, answer)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	ownSrv, globalSrv := proxy(&own, "own"), proxy(&global, "global")
	t.Setenv("HTTP_PROXY", globalSrv.URL)
	t.Setenv("http_proxy", globalSrv.URL)
	t.Setenv("FAKE_BASE", "http://vendor.invalid/v1")
	t.Setenv("FAKE_USAGE", "http://vendor.invalid/usage")
	t.Setenv("FAKE_MODELS", "http://vendor.invalid/models")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	fetch := func(ctx context.Context) (string, error) {
		res, err := Fetch(ctx, FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: "http://vendor.invalid/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	last := func(seen *[]string) string {
		mu.Lock()
		defer mu.Unlock()
		if len(*seen) == 0 {
			return ""
		}
		return (*seen)[len(*seen)-1]
	}

	// none of its own: magpie's
	if b, err := fetch(ctx); err != nil || b != "global" {
		t.Fatalf("with no proxy of its own: %q, %v", b, err)
	}
	// its own, as host:port
	mine := netproxy.With(ctx, strings.TrimPrefix(ownSrv.URL, "http://"))
	if b, err := fetch(mine); err != nil || b != "own" || last(&own) != "http://vendor.invalid/v1/chat/completions" {
		t.Fatalf("with its own proxy: %q, %v; the proxy saw %q", b, err, last(&own))
	}
	if u, err := AccountUsage(mine, "fakeco", ""); err != nil || u.Plan != "own" || last(&own) != "http://vendor.invalid/usage" {
		t.Fatalf("usage with its own proxy: %+v, %v", u, err)
	}
	// a move's check of the account asks the vendor as its requests do
	mu.Lock()
	own = nil
	mu.Unlock()
	c, err := Check(mine, "fakeco", "")
	mu.Lock()
	asked := slices.Clone(own)
	mu.Unlock()
	if err != nil || !slices.Contains(c.Models, "fake-own") || !slices.Contains(asked, "http://vendor.invalid/models") || !slices.Contains(asked, "http://vendor.invalid/usage") {
		t.Fatalf("a check with its own proxy: %+v, %v; the proxy saw %q", c, err, asked)
	}
	// direct: vendor.invalid can't be reached but through a proxy
	mu.Lock()
	n := len(global) + len(own)
	mu.Unlock()
	if b, err := fetch(netproxy.With(ctx, "direct")); err == nil {
		t.Fatalf("went direct, yet answered %q", b)
	}
	mu.Lock()
	m := len(global) + len(own)
	mu.Unlock()
	if m != n {
		t.Fatalf("a direct request went through a proxy: own %v, global %v", own, global)
	}
	// and the next with none of its own takes magpie's again
	if b, err := fetch(ctx); err != nil || b != "global" {
		t.Fatalf("after a direct one: %q, %v", b, err)
	}
}

// Plugins changed by another magpie (magpie plugin add or move in a
// terminal while the app runs) reach the one running: its host starts
// again with them, and meanwhile it knows the providers the other magpie
// was told, where it went on without them until restarted.
func TestPluginsChangedElsewhere(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	gen := generation.Load()

	// the other magpie: its providers told, then plugins.json written
	var ps []Provider
	b, _ := os.ReadFile(providersPath())
	_ = json.Unmarshal(b, &ps)
	ps = append(ps, Provider{ID: "elsewhere", Spec: abs, Name: "Elsewhere"})
	b, _ = json.Marshal(ps)
	if err := os.WriteFile(providersPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := os.ReadFile(listPath())
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(listPath(), append(l, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(Cached(), func(p Provider) bool { return p.ID == "elsewhere" }) {
		t.Fatalf("the other magpie's providers unknown: %+v", Cached())
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if generation.Load() == gen {
		t.Fatal("the host running kept the plugins it had loaded")
	}
}
