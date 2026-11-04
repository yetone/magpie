package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// seedKeptPlan saves provider alpha (model m1, answering from up; its
// Anthropic base is Ollama Cloud's, which makes its key a plan's, and is
// never called) with a
// key whose plan card is kept on disk with a 5 hours and a 7 days window,
// as the status line reads it.
func seedKeptPlan(t *testing.T, up string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "alpha", Name: "Alpha", Key: "alpha-one", Chat: up + "/v1", Anthropic: "https://ollama.com", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("alpha-one"))
	reset := time.Now().Add(2 * time.Hour)
	kept := map[string]any{"alpha/plan " + hex.EncodeToString(sum[:8]): map[string]any{"at": time.Now().Add(-time.Minute), "quota": provider.SubscriptionQuota{
		Provider: "alpha",
		Windows:  []provider.QuotaWindow{{Name: "5 hours", Used: 62, ResetsAt: &reset}, {Name: "7 days", Used: 41, ResetsAt: &reset}}}}}
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quotas.json"), mustJSON(kept), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetKeptCardsForTest()
	provider.ForgetBalances()
	provider.ForgetPlanQuotas()
}

// Claude Code on another computer than the gateway (steven_4422: magpie in
// Docker, Claude Code on the Linux host) runs curl for its status line.
// The line is the CLI's own, the model of the session sent in the body
// first, and another machine is told it only with a gateway key.
func TestStatuslineEndpoint(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","model":"m1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	t.Cleanup(up.Close)
	seedKeptPlan(t, up.URL)
	keys, secrets := newCaller(t, "Host", "Display", "Budget")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("limit-key", access.Change{Key: keys[2].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	do := func(method, target, from, secret, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.RemoteAddr = from
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const here, host = "127.0.0.1:5000", "192.168.1.9:5000"

	// a turn of the session, so it has a route
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alpha/m1","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = here
	req.Header.Set(SessionHeader, "6f1d2c3a")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, req); return w.Code }() != 200 {
		t.Fatal("the session's turn failed")
	}

	const cards = "alpha* 5h 62% 7d 41%"
	const claudeCode = `{"cwd":"/home/me","session_id":"6f1d2c3a","version":"2.1.296"}`
	// this computer: no key, as the CLI is
	if c, b := do("POST", StatuslinePath, here, "", claudeCode); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("POST from this computer: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?session=6f1d2c3a", here, "", ""); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("GET ?session= from this computer: %d %q", c, b)
	}
	// no session: the accounts alone; ?only= keeps those named
	if c, b := do("POST", StatuslinePath, here, "", ""); c != 200 || b != cards+"\n" {
		t.Errorf("POST with no body: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?only=nobody", here, "", ""); c != 200 || strings.TrimSpace(b) != "" {
		t.Errorf("GET ?only=nobody: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?only=alpha,x", here, "", ""); c != 200 || b != cards+"\n" {
		t.Errorf("GET ?only=alpha: %d %q", c, b)
	}

	// another machine (the Docker host): a gateway key or nothing
	for _, m := range []string{"GET", "POST"} {
		if c, b := do(m, StatuslinePath, host, "", claudeCode); c != http.StatusUnauthorized || strings.Contains(b, "alpha") {
			t.Errorf("%s from another machine with no key: %d %q", m, c, b)
		}
		if c, b := do(m, StatuslinePath, host, "not-a-key", claudeCode); c != http.StatusUnauthorized || strings.Contains(b, "alpha") {
			t.Errorf("%s from another machine with a wrong key: %d %q", m, c, b)
		}
	}
	if c, b := do("POST", StatuslinePath, host, secrets[0], claudeCode); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("POST from another machine with a key: %d %q", c, b)
	}
	// a read-only key reads the accounts' use, and is told no route
	if c, b := do("POST", StatuslinePath, host, secrets[1], claudeCode); c != 200 || b != cards+"\n" {
		t.Errorf("POST with a read-only key: %d %q", c, b)
	}
	// a key held to a budget is told none of the accounts
	if c, b := do("POST", StatuslinePath, host, secrets[2], claudeCode); c != http.StatusForbidden || strings.Contains(b, "alpha 5h") {
		t.Errorf("POST with a held key: %d %q", c, b)
	}
}

// seedKeptPlans saves alpha (keys One and Two, models m1 and m2) and beta
// (one key, model o1), each key's plan card kept on disk with a 5 hours
// and a 7 days window, as the status line reads them.
func seedKeptPlans(t *testing.T) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "alpha", Name: "Alpha", Key: "alpha-one", KeyName: "One",
		Keys: []provider.KeyAccount{{Name: "Two", Key: "alpha-two"}}, Chat: up.URL + "/v1", Anthropic: "https://ollama.com", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "beta", Name: "Beta", Key: "beta-key", Chat: up.URL + "/v1", Anthropic: "https://ollama.com", Models: []string{"o1"}}); err != nil {
		t.Fatal(err)
	}
	soon, later := time.Now().Add(2*time.Hour), time.Now().Add(48*time.Hour)
	kept := map[string]any{}
	for _, k := range []struct{ id, key, user string }{{"alpha", "alpha-one", "One"}, {"alpha", "alpha-two", "Two"}, {"beta", "beta-key", ""}} {
		sum := sha256.Sum256([]byte(k.key))
		kept[k.id+"/plan "+hex.EncodeToString(sum[:8])] = map[string]any{"at": time.Now().Add(-time.Hour), "quota": provider.SubscriptionQuota{
			Provider: k.id, User: k.user,
			Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 62, ResetsAt: &soon}, {Name: "7 days", Used: 41, ResetsAt: &later}}}}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quotas.json"), mustJSON(kept), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetKeptCardsForTest()
	provider.ForgetBalances()
	provider.ForgetPlanQuotas()
}

// steven_4422 (Discord): a key held to Claude's models got 403 from the
// status line while /v1/magpie/limit said {"limited":false}. A held key
// its owner lets see quotas gets the line of what it may use and of
// nothing else, as text and as JSON; one whose owner hasn't is told how
// to; /v1/magpie/limit says what holds a key and which providers it is
// told; a key held to nothing, and this computer, see every account.
func TestStatuslineHeldKeyScope(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	seedKeptPlans(t)
	p, err := provider.Find("alpha")
	if err != nil {
		t.Fatal(err)
	}
	var two string
	for _, a := range p.AccountIDs() {
		if a.User == "Two" {
			two = "alpha/" + a.ID
		}
	}
	if two == "" {
		t.Fatal("alpha has no key Two")
	}
	keys, secrets := newCaller(t, "Owner", "AlphaAll", "AlphaTwo", "AlphaOff", "AlphaSome")
	for _, c := range []struct {
		action string
		in     access.Change
	}{
		{"models-key", access.Change{Key: keys[1].ID, Models: []string{"alpha/*"}}},
		{"models-key", access.Change{Key: keys[2].ID, Models: []string{"alpha/*"}}},
		{"accounts-key", access.Change{Key: keys[2].ID, Accounts: []string{two}}},
		{"models-key", access.Change{Key: keys[3].ID, Models: []string{"alpha/*"}}},
		{"models-key", access.Change{Key: keys[4].ID, Models: []string{"alpha/m1"}}},
		{"quotas-key", access.Change{Key: keys[1].ID, Quotas: true}},
		{"quotas-key", access.Change{Key: keys[2].ID, Quotas: true}},
		{"quotas-key", access.Change{Key: keys[4].ID, Quotas: true}},
	} {
		if _, err := access.Update(c.action, c.in); err != nil {
			t.Fatal(c.action, err)
		}
	}
	h := lanGuard(New().Handler())
	do := func(target, from, secret string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = from
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const here, host = "127.0.0.1:5000", "192.168.1.9:5000"
	// the cards a line names, by "provider user"
	cards := func(line string) []string {
		var out []string
		for _, part := range strings.Split(strings.TrimSpace(line), " | ") {
			if f := strings.Fields(part); len(f) > 0 && (f[0] == "alpha" || f[0] == "beta") {
				name := f[0]
				if len(f) > 1 && f[1] != "5h" {
					name += " " + strings.TrimSuffix(f[1], "*")
				}
				out = append(out, name)
			}
		}
		slices.Sort(out)
		return out
	}
	every := []string{"alpha One", "alpha Two", "beta"}
	for _, c := range []struct {
		name, from, secret string
		want               []string
	}{
		{"this computer", here, "", every},
		{"a key held to nothing", host, secrets[0], every},
		{"a key held to alpha's models", host, secrets[1], []string{"alpha One", "alpha Two"}},
		{"a key held to alpha's Two", host, secrets[2], []string{"alpha Two"}},
		// alpha/m2 spends alpha's windows too, and beta isn't its
		{"a key held to some of alpha's models", host, secrets[4], nil},
	} {
		code, b := do(StatuslinePath, c.from, c.secret)
		if code != 200 || !slices.Equal(cards(b), c.want) {
			t.Errorf("%s: %d %q, want the cards %v", c.name, code, b, c.want)
		}
		if c.want != nil && !strings.Contains(b, "5h 62% 7d 41%") {
			t.Errorf("%s: %q has not the windows as read", c.name, b)
		}
		// the JSON tells the same cards, the line with them
		code, b = do(StatuslinePath+"?format=json", c.from, c.secret)
		var out struct {
			Text     string
			Model    string
			Accounts []provider.Quota
		}
		if code != 200 || json.Unmarshal([]byte(b), &out) != nil || out.Accounts == nil {
			t.Fatalf("%s ?format=json: %d %s", c.name, code, b)
		}
		var got []string
		for _, q := range out.Accounts {
			got = append(got, strings.TrimSpace(q.Provider+" "+q.User))
			if len(q.Windows) != 2 || q.Windows[0].Used != 62 || q.Windows[0].ResetsAt == nil || q.Windows[1].Used != 41 || q.Windows[1].ResetsAt == nil {
				t.Errorf("%s ?format=json: %s's windows %+v", c.name, q.Provider, q.Windows)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) || !slices.Equal(cards(out.Text), c.want) {
			t.Errorf("%s ?format=json: %v and %q, want %v", c.name, got, out.Text, c.want)
		}
	}
	// a held key whose owner hasn't turned its quotas on is told how
	for _, target := range []string{StatuslinePath, StatuslinePath + "?format=json"} {
		if code, b := do(target, host, secrets[3]); code != http.StatusForbidden || !strings.Contains(b, provider.KeyHeldCode) || !strings.Contains(b, "gateway-key quotas <id> on") || strings.Contains(b, "62") {
			t.Errorf("%s, quotas off: %d %s", target, code, b)
		}
	}

	// /v1/magpie/limit agrees
	limit := func(secret string) map[string]any {
		t.Helper()
		code, b := do("/v1/magpie/limit", host, secret)
		var out map[string]any
		if code != 200 || json.Unmarshal([]byte(b), &out) != nil {
			t.Fatalf("limit: %d %s", code, b)
		}
		return out
	}
	if out := limit(secrets[0]); out["heldTo"] != nil || out["quotas"] != nil || out["models"] != nil || out["limited"] != false {
		t.Errorf("a key held to nothing: %v", out)
	}
	for i, want := range []struct {
		heldTo, quotaProviders string
		quotas                 bool
	}{
		1: {"[models]", "[alpha]", true},
		2: {"[models accounts]", "[alpha]", true},
		3: {"[models]", "<nil>", false},
		4: {"[models]", "[]", true},
	} {
		if i == 0 {
			continue
		}
		out := limit(secrets[i])
		if fmt.Sprint(out["heldTo"]) != want.heldTo || fmt.Sprint(out["quotaProviders"]) != want.quotaProviders || out["quotas"] != want.quotas || out["limited"] != false || out["models"] == nil {
			t.Errorf("%s: %v", keys[i].Name, out)
		}
		if strings.Contains(fmt.Sprint(out), strings.TrimPrefix(two, "alpha/")) {
			t.Errorf("%s: an account is named: %v", keys[i].Name, out)
		}
	}
}
