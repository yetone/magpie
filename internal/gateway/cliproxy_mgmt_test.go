package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// #7402: T3 Code's Usage providers → Add hub reads a CLIProxyAPI hub's
// accounts and their usage through /v0/management/*. The gateway answers
// the reads from its quota cards, to a gateway key alone, and sends
// nothing on. The shapes and the reader below are T3 Code's own
// (apps/server/src/usage/cliproxyApi.ts, codexUsageLimits.ts,
// claudeUsageLimits.ts, provider-grok's usageLimits.ts and
// UsageLimitSources.ts' hubPoolResetAt, at 0c8e46b0), and CLIProxyAPI's
// (internal/api/handlers/management, at dbe641f2).

// mgmtFixture is what the cache holds: a Codex account spent in its 5
// hours, a Claude account spent in its week and Opus's, a Grok account,
// a Codex account never read, and cards T3 Code doesn't read.
func mgmtFixture(t *testing.T, now time.Time) {
	t.Helper()
	at := func(d time.Duration) *time.Time { x := now.Add(d).Truncate(time.Second); return &x }
	cards := []provider.SubscriptionQuota{
		{Provider: "codex", Name: "Codex", Plan: "plus", User: "me@example.com", Kind: "subscription", Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 100, ResetsAt: at(2 * time.Hour), Span: 5 * time.Hour},
			{Name: "7 days", Used: 40, ResetsAt: at(72 * time.Hour), Span: 7 * 24 * time.Hour},
		}},
		{Provider: "claude", Name: "Claude", Plan: "max", User: "c@example.com", Kind: "subscription", Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 30, ResetsAt: at(3 * time.Hour), Span: 5 * time.Hour},
			{Name: "7 days", Used: 100, ResetsAt: at(48 * time.Hour), Span: 7 * 24 * time.Hour},
			{Name: "7 days · Opus", Used: 100, ResetsAt: at(50 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
			{Name: "Extra usage", Used: 100, Text: true},
		}},
		{Provider: "grok", Name: "Grok", User: "g@example.com", Kind: "subscription", Windows: []provider.QuotaWindow{
			// after a restart a window's Span is gone (json "-"): its name says it
			{Name: "7 days", Used: 42.5, ResetsAt: at(96 * time.Hour)},
			{Name: "On-demand", Used: 90, ResetsAt: at(96 * time.Hour), Aside: true},
		}},
		{Provider: "codex", Name: "Codex", User: "broken@example.com", Kind: "subscription", Error: "token refresh failed: refresh_token=rt_secret", Windows: []provider.QuotaWindow{}},
		{Provider: "kimi", Name: "Kimi", User: "k@example.com", Kind: "subscription", Windows: []provider.QuotaWindow{{Name: "7 days", Used: 10}}},
		{Provider: "codex", Name: "Codex key", Kind: "balance", Balance: "$3"},
	}
	old := mgmtCards
	mgmtCards = func(time.Time) []provider.SubscriptionQuota { return slices.Clone(cards) }
	t.Cleanup(func() { mgmtCards = old })
}

// mgmtVendors fails the test if anything is sent to a vendor T3 Code
// names: the hub answers from its cache alone.
func mgmtVendors(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		sent = append(sent, r.URL.String())
		mu.Unlock()
		return nil, fmt.Errorf("no request leaves a test: %s", r.URL)
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return &sent
}

type mgmtClient struct {
	t *testing.T
	h http.Handler
}

func (c mgmtClient) do(method, path, from, host, origin, secret, body string) (int, string) {
	c.t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = from
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.2 Safari/605.1.15")
	}
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

const mgmtLoop, mgmtLAN, mgmtGW = "127.0.0.1:50123", "192.168.1.9:5000", "127.0.0.1:3425"

// A request with no gateway key is told nothing, from wherever it
// comes: this computer, or a page or app the gateway lets in on
// loopback without a key (Origin null, an app's scheme, a *.localhost or
// rebound page), or another machine.
func TestCLIProxyManagementNeedsAKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now()
	mgmtFixture(t, now)
	sent := mgmtVendors(t)
	_, secrets := newCaller(t, "T3")
	c := mgmtClient{t, lanGuard(New().Handler())}
	call := `{"auth_index":"x","method":"GET","url":"https://chatgpt.com/backend-api/wham/usage","header":{"Authorization":"Bearer $TOKEN$"}}`
	refused := []struct {
		why                        string
		from, host, origin, secret string
	}{
		{"this computer, no key", mgmtLoop, mgmtGW, "", ""},
		{"a sandboxed frame (Origin null), no key", mgmtLoop, mgmtGW, "null", ""},
		{"an app's scheme, no key", mgmtLoop, mgmtGW, "tauri://localhost", ""},
		{"a *.localhost page, no key", mgmtLoop, mgmtGW, "http://evil.localhost:8080", ""},
		{"a rebound page (its own Host), no key", mgmtLoop, "evil.example:3425", "", ""},
		{"a foreign page, no key", mgmtLoop, mgmtGW, "https://evil.example", ""},
		{"a foreign page with the key", mgmtLoop, mgmtGW, "https://evil.example", secrets[0]},
		{"another machine, no key", mgmtLAN, mgmtGW, "", ""},
		{"another machine, a wrong key", mgmtLAN, mgmtGW, "", "sk-magpie-wrong"},
		{"this computer, a wrong key", mgmtLoop, mgmtGW, "", "sk-magpie-wrong"},
	}
	for _, r := range refused {
		for _, q := range []struct{ method, path, body string }{
			{"GET", "/v0/management/auth-files", ""},
			{"POST", "/v0/management/api-call", call},
		} {
			code, body := c.do(q.method, q.path, r.from, r.host, r.origin, r.secret, q.body)
			if code < 400 || code == http.StatusNotFound || strings.Contains(body, "example.com") || strings.Contains(body, "used_percent") {
				t.Errorf("%s, %s %s: %d %s", r.why, q.method, q.path, code, body)
			}
		}
	}
	// no key from this computer: CLIProxyAPI's own words, which T3 Code
	// shows as "The hub answered with HTTP 401."
	if code, body := c.do("GET", "/v0/management/auth-files", mgmtLoop, mgmtGW, "", "", ""); code != http.StatusUnauthorized || !strings.Contains(body, `"missing management key"`) {
		t.Fatalf("no key: %d %s", code, body)
	}
	if len(*sent) > 0 {
		t.Fatalf("a vendor was asked: %v", *sent)
	}
}

// A read-only key, the one a usage reader is given, is told the accounts
// and their usage, and nothing else of the management API: no write, no
// other upstream URL or method, nothing sent on. Every other route is
// 404, or 405 for a known one, to any key.
func TestCLIProxyManagementReadOnlyKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now()
	mgmtFixture(t, now)
	sent := mgmtVendors(t)
	keys, secrets := newCaller(t, "T3", "Full", "Held", "Held, quotas on")
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys[2:] {
		if _, err := access.Update("limit-key", access.Change{Key: k.ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := access.Update("quotas-key", access.Change{Key: keys[3].ID, Quotas: true}); err != nil {
		t.Fatal(err)
	}
	c := mgmtClient{t, lanGuard(New().Handler())}
	ro, full := secrets[0], secrets[1]

	for _, from := range []string{mgmtLoop, mgmtLAN} {
		code, body := c.do("GET", "/v0/management/auth-files", from, mgmtGW, "", ro, "")
		if code != 200 {
			t.Fatalf("read-only key from %s: %d %s", from, code, body)
		}
	}
	code, body := c.do("GET", "/v0/management/auth-files", mgmtLAN, mgmtGW, "", ro, "")
	var listed struct {
		Files []map[string]any `json:"files"`
	}
	if json.Unmarshal([]byte(body), &listed) != nil {
		t.Fatalf("auth-files: %d %s", code, body)
	}
	var providers []string
	index := map[string]string{}
	for _, f := range listed.Files {
		providers = append(providers, f["provider"].(string))
		index[f["email"].(string)] = f["auth_index"].(string)
		for _, k := range []string{"id", "auth_index", "name", "type", "provider", "status", "disabled"} {
			if _, ok := f[k]; !ok {
				t.Errorf("auth file %v has no %s, which CLIProxyAPI's has", f, k)
			}
		}
	}
	if want := []string{"codex", "claude", "xai", "codex"}; !slices.Equal(providers, want) {
		t.Fatalf("providers %v, want %v (kimi and a key's balance aren't told)", providers, want)
	}
	for _, secret := range []string{"rt_secret", "refresh", "access_token", "token refresh failed"} {
		if strings.Contains(body, secret) {
			t.Fatalf("auth-files tells %q: %s", secret, body)
		}
	}

	usage := func(secret, email, method, url string) (int, apiCallReply) {
		t.Helper()
		req, _ := json.Marshal(map[string]any{"auth_index": index[email], "method": method, "url": url,
			"header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
		code, body := c.do("POST", "/v0/management/api-call", mgmtLAN, mgmtGW, "", secret, string(req))
		var out apiCallReply
		if code == 200 && json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("api-call: %s", body)
		}
		return code, out
	}
	if code, out := usage(ro, "me@example.com", "GET", codexUsageURL); code != 200 || out.StatusCode != 200 || !strings.Contains(out.Body, `"used_percent":100`) {
		t.Fatalf("read-only key's usage read: %d %+v", code, out)
	}
	// a write or an upstream URL other than the usage read is refused in
	// the call's status, which T3 Code reads as the provider refusing it
	for _, x := range []struct{ email, method, url string }{
		{"me@example.com", "POST", "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"},
		{"me@example.com", "GET", "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"},
		{"me@example.com", "POST", codexUsageURL},
		{"me@example.com", "GET", "https://api.anthropic.com/api/oauth/usage"},
		{"me@example.com", "GET", "http://169.254.169.254/latest/meta-data/"},
		{"me@example.com", "GET", "https://chatgpt.com/backend-api/wham/usage/../accounts"},
		{"c@example.com", "GET", codexUsageURL},
		{"c@example.com", "DELETE", "https://api.anthropic.com/api/oauth/usage"},
		{"g@example.com", "GET", "https://cli-chat-proxy.grok.com/v1/billing"},
	} {
		for _, secret := range []string{ro, full} {
			if code, out := usage(secret, x.email, x.method, x.url); code != 200 || out.StatusCode != http.StatusForbidden || strings.Contains(out.Body, "used_percent") {
				t.Errorf("%s %s for %s: %d %+v", x.method, x.url, x.email, code, out)
			}
		}
	}
	// CLIProxyAPI's own refusals of a call it can't make
	for _, x := range []struct{ body, want string }{
		{`{`, "invalid body"},
		{`{"auth_index":"` + index["me@example.com"] + `","url":"` + codexUsageURL + `"}`, "missing method"},
		{`{"auth_index":"` + index["me@example.com"] + `","method":"GET"}`, "missing url"},
		{`{"auth_index":"` + index["me@example.com"] + `","method":"GET","url":"chatgpt.com"}`, "invalid url"},
		{`{"auth_index":"nope","method":"GET","url":"` + codexUsageURL + `"}`, "auth credential not found for auth_index"},
		{`{"method":"GET","url":"` + codexUsageURL + `"}`, "auth credential not found for auth_index"},
	} {
		if code, body := c.do("POST", "/v0/management/api-call", mgmtLAN, mgmtGW, "", ro, x.body); code != http.StatusBadRequest || !strings.Contains(body, `"error":"`+x.want+`"`) {
			t.Errorf("%s: %d %s", x.body, code, body)
		}
	}
	// an account never read: the call fails as a vendor's would, and says
	// nothing of why
	if code, out := usage(ro, "broken@example.com", "GET", codexUsageURL); code != 200 || out.StatusCode != http.StatusBadGateway || strings.Contains(out.Body, "rt_secret") {
		t.Fatalf("an account never read: %d %+v", code, out)
	}

	// every other management route: CLIProxyAPI's writes, its config, the
	// models list. A read-only key is refused them before they answer.
	others := []struct{ method, path string }{
		{"POST", "/v0/management/reset-quota"},
		{"PUT", "/v0/management/config.yaml"},
		{"GET", "/v0/management/config"},
		{"PATCH", "/v0/management/api-keys"},
		{"DELETE", "/v0/management/auth-files?name=codex.json"},
		{"POST", "/v0/management/auth-files"},
		{"GET", "/v0/management/auth-files/models?name=codex-x"},
		{"GET", "/v0/management/api-call"},
		{"GET", "/v0/management/"},
	}
	for _, o := range others {
		if code, body := c.do(o.method, o.path, mgmtLAN, mgmtGW, "", ro, ""); code != http.StatusForbidden || !strings.Contains(body, ReadOnlyCode) {
			t.Errorf("read-only key, %s %s: %d %s", o.method, o.path, code, body)
		}
		code, body := c.do(o.method, o.path, mgmtLAN, mgmtGW, "", full, "")
		path, _, _ := strings.Cut(o.path, "?")
		known := path == "/v0/management/api-call" || path == "/v0/management/auth-files"
		if known && code != http.StatusMethodNotAllowed || !known && code != http.StatusNotFound || !strings.Contains(body, `"error":"`) {
			t.Errorf("full key, %s %s: %d %s", o.method, o.path, code, body)
		}
	}

	// a key held to a budget is told no quotas, unless its owner lets
	// it, and then only those of what it may use (none here: no
	// provider of these is in the catalog)
	if code, body := c.do("GET", "/v0/management/auth-files", mgmtLAN, mgmtGW, "", secrets[2], ""); code != http.StatusForbidden || !strings.Contains(body, provider.KeyHeldCode) {
		t.Fatalf("held key: %d %s", code, body)
	}
	if code, body := c.do("GET", "/v0/management/auth-files", mgmtLAN, mgmtGW, "", secrets[3], ""); code != 200 || strings.Contains(body, "example.com") {
		t.Fatalf("held key with quotas on: %d %s", code, body)
	}
	if len(*sent) > 0 {
		t.Fatalf("a vendor was asked: %v", *sent)
	}
}

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

type apiCallReply struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

// T3 Code, reading the gateway as a hub, sees each account's windows as
// magpie read them, and blocks a model only while every account it is
// routed to is spent, until the earliest of them comes back.
func TestCLIProxyManagementReadByT3Code(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	now := time.Now()
	mgmtFixture(t, now)
	mgmtVendors(t)
	keys, secrets := newCaller(t, "T3")
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(lanGuard(New().Handler()))
	defer srv.Close()
	accounts, unread, err := t3ReadHub(srv.URL+"/management.html", secrets[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 0 {
		t.Fatalf("unread providers %v", unread)
	}
	got := map[string]t3Account{}
	for _, a := range accounts {
		got[a.email] = a
	}
	iso := func(d time.Duration) string { return now.Add(d).Truncate(time.Second).UTC().Format(time.RFC3339) }
	want := map[string][]t3Window{
		"me@example.com": {
			{id: "primary", kind: "session", used: 100, mins: 300, resetsAt: iso(2 * time.Hour)},
			{id: "secondary", kind: "weekly", used: 40, mins: 10080, resetsAt: iso(72 * time.Hour)},
		},
		"c@example.com": {
			{id: "five_hour", kind: "session", used: 30, mins: 300, resetsAt: iso(3 * time.Hour)},
			{id: "seven_day", kind: "weekly", used: 100, mins: 10080, resetsAt: iso(48 * time.Hour)},
			{id: "seven_day_opus", kind: "weekly", used: 100, mins: 10080, resetsAt: iso(50 * time.Hour)},
		},
		"g@example.com": {
			{id: "subscription", kind: "weekly", used: 42.5, mins: 10080, resetsAt: iso(96 * time.Hour)},
		},
	}
	for email, ws := range want {
		a := got[email]
		if a.unavailable != "" || !slices.Equal(a.windows, ws) {
			t.Errorf("%s (%s): %+v %q, want %+v", email, a.driver, a.windows, a.unavailable, ws)
		}
	}
	if a := got["me@example.com"]; a.driver != "codex" || a.plan != "plus" {
		t.Errorf("codex account: %+v", a)
	}
	if a := got["c@example.com"]; a.driver != "claudeAgent" || a.plan != "Claude Subscription" {
		t.Errorf("claude account: %+v", a)
	}
	if a := got["g@example.com"]; a.driver != "grok" {
		t.Errorf("grok account: %+v", a)
	}
	if a := got["broken@example.com"]; a.unavailable != "The provider refused the hub request (HTTP 502)." {
		t.Errorf("an account never read: %+v", a)
	}

	// Codex: one account spent, one unread, so its refill is unknown
	if at, ok := t3PoolResetAt(accounts, "codex", "gpt-5.5", now); ok {
		t.Errorf("codex with an unread account: resets %s", at)
	}
	var read []t3Account
	for _, a := range accounts {
		if a.email != "broken@example.com" {
			read = append(read, a)
		}
	}
	if at, ok := t3PoolResetAt(read, "codex", "gpt-5.5", now); !ok || at != iso(2*time.Hour) {
		t.Errorf("codex spent: resets %q %v, want %s", at, ok, iso(2*time.Hour))
	}
	// Claude: the week is spent; Opus's own week comes back later still
	if at, ok := t3PoolResetAt(read, "claudeAgent", "claude-sonnet-4-6", now); !ok || at != iso(48*time.Hour) {
		t.Errorf("sonnet: resets %q %v", at, ok)
	}
	if at, ok := t3PoolResetAt(read, "claudeAgent", "claude-opus-4-6", now); !ok || at != iso(50*time.Hour) {
		t.Errorf("opus: resets %q %v", at, ok)
	}
	// Grok: 42.5% used, not spent
	if at, ok := t3PoolResetAt(read, "grok", "grok-4", now); ok {
		t.Errorf("grok isn't spent: %s", at)
	}
}

// What follows is T3 Code's hub reader (cliproxyApi.ts readAccounts and
// its HUB_PROVIDERS), its usage parsers and hubPoolResetAt, in Go.

type t3Window struct {
	id, kind string
	used     float64
	mins     float64
	resetsAt string
}

type t3Account struct {
	id, driver, email, plan string
	windows                 []t3Window
	unavailable             string
}

func t3Get(base, key, path string, body any) (any, error) {
	b := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/management.html") + "/v0/management/" + path
	r, _ := http.NewRequest("GET", b, nil)
	if body != nil {
		j, _ := json.Marshal(body)
		r, _ = http.NewRequest("POST", b, strings.NewReader(string(j)))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+key)
	res, err := (&http.Client{Transport: &http.Transport{}, Timeout: 15 * time.Second}).Do(r)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("The hub answered with HTTP %d.", res.StatusCode)
	}
	var v any
	return v, json.NewDecoder(res.Body).Decode(&v)
}

// t3Schema checks v against T3 Code's Schema.Struct: each key of req
// present with its type (a "?" type is optional, a "|null" one nullable).
func t3Schema(v any, req map[string]string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not an object: %v", v)
	}
	for k, typ := range req {
		x, has := m[k]
		optional := strings.HasPrefix(typ, "?")
		typ = strings.TrimPrefix(typ, "?")
		if !has {
			if optional {
				continue
			}
			return nil, fmt.Errorf("%s missing", k)
		}
		nullable := strings.HasSuffix(typ, "|null")
		typ = strings.TrimSuffix(typ, "|null")
		if x == nil {
			if nullable {
				continue
			}
			return nil, fmt.Errorf("%s is null", k)
		}
		okType := false
		switch typ {
		case "string":
			_, okType = x.(string)
		case "number":
			_, okType = x.(float64)
		case "boolean":
			_, okType = x.(bool)
		case "object":
			_, okType = x.(map[string]any)
		case "array":
			_, okType = x.([]any)
		case "unknown":
			okType = true
		}
		if !okType {
			return nil, fmt.Errorf("%s is %T, not %s", k, x, typ)
		}
	}
	return m, nil
}

func t3ReadHub(base, key string) ([]t3Account, []string, error) {
	raw, err := t3Get(base, key, "auth-files", nil)
	if err != nil {
		return nil, nil, err
	}
	list, err := t3Schema(raw, map[string]string{"files": "array"})
	if err != nil {
		return nil, nil, err
	}
	drivers := map[string]string{"codex": "codex", "claude": "claudeAgent", "xai": "grok"}
	var out []t3Account
	unread := []string{}
	for _, f := range list["files"].([]any) {
		file, err := t3Schema(f, map[string]string{"id": "string", "auth_index": "string", "provider": "string", "email": "?string", "disabled": "?boolean", "id_token": "?object"})
		if err != nil {
			return nil, nil, fmt.Errorf("auth file: %w", err)
		}
		prov := file["provider"].(string)
		driver, readable := drivers[prov]
		if disabled, _ := file["disabled"].(bool); disabled {
			continue
		}
		if !readable {
			unread = append(unread, prov)
			continue
		}
		// auth-files/models is optional: unanswered, the account counts
		// for every model of its driver
		if _, err := t3Get(base, key, "auth-files/models?name="+file["id"].(string), nil); err == nil {
			return nil, nil, fmt.Errorf("auth-files/models answered; the reader then routes by its list")
		}
		a := t3Account{id: file["id"].(string), driver: driver}
		a.email, _ = file["email"].(string)
		call := func(url string) (string, error) {
			raw, err := t3Get(base, key, "api-call", map[string]any{"auth_index": file["auth_index"], "method": "GET", "url": url, "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
			if err != nil {
				return "", err
			}
			r, err := t3Schema(raw, map[string]string{"status_code": "number", "body": "string"})
			if err != nil {
				return "", err
			}
			if s := r["status_code"].(float64); s < 200 || s >= 300 {
				return "", fmt.Errorf("The provider refused the hub request (HTTP %d).", int(s))
			}
			return r["body"].(string), nil
		}
		var read error
		switch prov {
		case "codex":
			var tok map[string]any
			if x, ok := file["id_token"].(map[string]any); ok {
				tok = x
			}
			a.plan, a.windows, read = t3Codex(call, tok)
			// the reset credits are optional, their failure ignored
			_, _ = call("https://chatgpt.com/backend-api/wham/rate-limit-reset-credits")
		case "claude":
			a.plan = "Claude Subscription"
			a.windows, read = t3Claude(call)
		case "xai":
			a.windows, read = t3Grok(call)
		}
		if read != nil {
			a.windows, a.unavailable = nil, read.Error()
		}
		out = append(out, a)
	}
	return out, unread, nil
}

func t3JSON(body string) (any, error) {
	var v any
	return v, json.Unmarshal([]byte(body), &v)
}

func t3Iso(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
}

func t3Clamp(v float64) float64 { return math.Max(0, math.Min(100, v)) }

func t3Codex(call func(string) (string, error), tok map[string]any) (string, []t3Window, error) {
	body, err := call(codexUsageURL)
	if err != nil {
		return "", nil, err
	}
	raw, err := t3JSON(body)
	if err != nil {
		return "", nil, err
	}
	u, err := t3Schema(raw, map[string]string{"plan_type": "?string", "rate_limit": "object|null"})
	if err != nil {
		return "", nil, err
	}
	plan, _ := u["plan_type"].(string)
	if plan == "" {
		plan, _ = tok["plan_type"].(string)
	}
	monthly := plan == "free" || plan == "go"
	var out []t3Window
	rl, _ := u["rate_limit"].(map[string]any)
	if rl != nil {
		if _, err := t3Schema(rl, map[string]string{"primary_window": "?object|null", "secondary_window": "?object|null"}); err != nil {
			return "", nil, err
		}
	}
	for _, p := range []struct {
		id       string
		fallback float64
	}{{"primary", map[bool]float64{true: 43200, false: 300}[monthly]}, {"secondary", 10080}} {
		w, _ := rl[p.id+"_window"].(map[string]any)
		if w == nil {
			continue
		}
		if _, err := t3Schema(w, map[string]string{"used_percent": "number", "reset_at": "?number|null", "limit_window_seconds": "?number"}); err != nil {
			return "", nil, err
		}
		mins := p.fallback
		if s, ok := w["limit_window_seconds"].(float64); ok {
			mins = s / 60
		}
		kind := "session"
		if mins >= 43200 {
			kind = "monthly"
		} else if mins >= 10080 {
			kind = "weekly"
		}
		x := t3Window{id: p.id, kind: kind, used: t3Clamp(w["used_percent"].(float64)), mins: mins}
		if r, ok := w["reset_at"].(float64); ok && r > 0 {
			x.resetsAt = t3Iso(r * 1000)
		}
		out = append(out, x)
	}
	return plan, t3Sorted(out), nil
}

var t3ScopeToken = regexp.MustCompile(`[^a-z0-9]+`)

func t3Claude(call func(string) (string, error)) ([]t3Window, error) {
	body, err := call("https://api.anthropic.com/api/oauth/usage")
	if err != nil {
		return nil, err
	}
	raw, err := t3JSON(body)
	if err != nil {
		return nil, err
	}
	u, err := t3Schema(raw, map[string]string{"five_hour": "?object|null", "seven_day": "?object|null", "spend": "?unknown", "extra_usage": "?object|null", "limits": "?array"})
	if err != nil {
		return nil, err
	}
	reset := func(v any) string {
		s, _ := v.(string)
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
		return ""
	}
	var out []t3Window
	for _, k := range []struct {
		id, kind string
		mins     float64
	}{{"five_hour", "session", 300}, {"seven_day", "weekly", 10080}} {
		w, _ := u[k.id].(map[string]any)
		if w == nil {
			continue
		}
		if _, err := t3Schema(w, map[string]string{"utilization": "number|null", "resets_at": "string|null"}); err != nil {
			return nil, err
		}
		used, ok := w["utilization"].(float64)
		if !ok {
			continue
		}
		out = append(out, t3Window{id: k.id, kind: k.kind, used: t3Clamp(used), mins: k.mins, resetsAt: reset(w["resets_at"])})
	}
	if ex, _ := u["extra_usage"].(map[string]any); ex != nil {
		if _, err := t3Schema(ex, map[string]string{"is_enabled": "boolean", "monthly_limit": "number|null", "used_credits": "number|null", "utilization": "number|null"}); err != nil {
			return nil, err
		}
	}
	ls, _ := u["limits"].([]any)
	for _, l := range ls {
		m, err := t3Schema(l, map[string]string{"kind": "string", "percent": "?number|null", "resets_at": "?string|null", "scope": "?object|null"})
		if err != nil {
			return nil, err
		}
		scope, _ := m["scope"].(map[string]any)
		model, _ := scope["model"].(map[string]any)
		name, _ := model["display_name"].(string)
		pct, ok := m["percent"].(float64)
		if m["kind"] != "weekly_scoped" || model == nil || !ok {
			continue
		}
		out = append(out, t3Window{id: "seven_day_" + t3ScopeToken.ReplaceAllString(strings.ToLower(name), "_"), kind: "weekly", used: t3Clamp(pct), mins: 10080, resetsAt: reset(m["resets_at"])})
	}
	return t3Sorted(out), nil
}

var t3Boundary = regexp.MustCompile(`^(?:\d{4}|[+-]\d{6})-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?|24:00(?::00(?:\.0+)?)?)(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func t3Grok(call func(string) (string, error)) ([]t3Window, error) {
	body, err := call("https://cli-chat-proxy.grok.com/v1/billing?format=credits")
	if err != nil {
		return nil, err
	}
	raw, err := t3JSON(body)
	if err != nil {
		return nil, err
	}
	u, err := t3Schema(raw, map[string]string{"config": "?object"})
	if err != nil {
		return nil, err
	}
	cfg, _ := u["config"].(map[string]any)
	if cfg != nil {
		if _, err := t3Schema(cfg, map[string]string{"creditUsagePercent": "?number", "currentPeriod": "?object"}); err != nil {
			return nil, err
		}
	}
	period, _ := cfg["currentPeriod"].(map[string]any)
	if period != nil {
		if _, err := t3Schema(period, map[string]string{"type": "?string", "start": "?unknown", "end": "?string"}); err != nil {
			return nil, err
		}
	}
	boundary := func(v any) (time.Time, bool) {
		s, ok := v.(string)
		if !ok || !t3Boundary.MatchString(s) {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		return t, err == nil
	}
	typ, _ := period["type"].(string)
	kind := map[string]string{"WEEKLY": "weekly", "MONTHLY": "monthly"}[strings.TrimPrefix(typ, "USAGE_PERIOD_TYPE_")]
	if kind == "" {
		kind = "other"
	}
	start, okStart := boundary(period["start"])
	end, okEnd := boundary(period["end"])
	used, ok := cfg["creditUsagePercent"].(float64)
	if !ok && (kind == "weekly" || kind == "monthly") && okStart && okEnd && start.Before(end) {
		used, ok = 0, true
	}
	if !ok {
		return nil, nil
	}
	w := t3Window{id: "subscription", kind: kind, used: t3Clamp(used)}
	if okEnd {
		w.resetsAt = end.UTC().Format(time.RFC3339)
	}
	if okStart && okEnd {
		if m := math.Round(end.Sub(start).Minutes()); m > 0 {
			w.mins = m
		}
	}
	return []t3Window{w}, nil
}

func t3Sorted(ws []t3Window) []t3Window {
	order := map[string]int{"session": 0, "weekly": 1, "monthly": 2, "other": 3}
	slices.SortStableFunc(ws, func(a, b t3Window) int {
		if d := order[a.kind] - order[b.kind]; d != 0 {
			return d
		}
		return strings.Compare(a.id, b.id)
	})
	return ws
}

// t3PoolResetAt is hubPoolResetAt with hubRoutes' fallback (no account
// lists its models): every account of the driver is routed the model.
func t3PoolResetAt(accounts []t3Account, driver, model string, now time.Time) (string, bool) {
	var earliest time.Time
	for _, a := range accounts {
		if a.driver != driver {
			continue
		}
		if a.unavailable != "" {
			return "", false
		}
		var recovers time.Time
		spent := 0
		for _, w := range a.windows {
			if w.used < 100 {
				continue
			}
			if strings.HasPrefix(w.id, "seven_day_") && !strings.Contains(t3ScopeToken.ReplaceAllString(strings.ToLower(model), "_"), strings.TrimPrefix(w.id, "seven_day_")) {
				continue
			}
			t, err := time.Parse(time.RFC3339, w.resetsAt)
			if err != nil {
				return "", false
			}
			spent++
			if t.After(recovers) {
				recovers = t
			}
		}
		if spent == 0 || !recovers.After(now) {
			return "", false
		}
		if earliest.IsZero() || recovers.Before(earliest) {
			earliest = recovers
		}
	}
	if earliest.IsZero() {
		return "", false
	}
	return earliest.UTC().Format(time.RFC3339), true
}
