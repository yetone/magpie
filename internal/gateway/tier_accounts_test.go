package gateway

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// tierAccounts signs Codex in to a Plus ChatGPT account, first, with a Pro
// one saved beside it, against a fake ChatGPT backend whose model lists
// offer gpt-6-luna's Ultrafast only to the Pro account. It returns the
// account and service tier each request was tried with, in order. With
// echo set, a reply says the tier it ran at, as OpenAI's API does
// (service_tier on the response: the one asked, or "default"). With said
// given, response.created and response.completed say the tiers it gives
// for the one asked.
func tierAccounts(t *testing.T, proOut, echo *bool, said ...func(asked string) (created, completed string)) func() []string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct, plan string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token": claims(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan}}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("plus@example.com", "acct-plus", "plus")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "pro@example.com", "plan": "pro", "on": true, "seen": time.Now(), "auth": auth("pro@example.com", "acct-pro", "pro")},
	}), 0o600)

	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		if strings.HasSuffix(r.URL.Path, "/models") {
			tiers := `[{"id":"priority","name":"Fast","description":""}]`
			if acct == "acct-pro" {
				tiers = `[{"id":"priority","name":"Fast","description":""},{"id":"ultrafast","name":"Ultrafast","description":""}]`
			}
			io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","visibility":"list","service_tiers":`+tiers+`}]}`)
			return
		}
		var q struct {
			Tier string `json:"service_tier"`
		}
		json.Unmarshal(b, &q)
		mu.Lock()
		tried = append(tried, acct+":"+q.Tier)
		mu.Unlock()
		if acct == "acct-pro" && proOut != nil && *proOut {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"You've hit your usage limit"}}`)
			return
		}
		began, ended := "", ""
		if echo != nil && *echo {
			ended = `,"service_tier":"` + cmp.Or(q.Tier, "default") + `"`
		}
		if len(said) > 0 {
			c, d := said[0](q.Tier)
			began, ended = `,"service_tier":"`+c+`"`, `,"service_tier":"`+d+`"`
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-luna"`+began+`}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1"`+ended+`,"usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := tried
		tried = nil
		return out
	}
}

// yxinyu715 on X: Codex's Ultrafast goes first to the account whose plan
// offers it, here the Pro one saved second, and Fast keeps the accounts'
// order. When the Pro one can't answer, the Plus one is asked for Fast,
// the nearest its plan offers.
func TestUltrafastGoesToAnAccountOfferingIt(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil)
	s := New()
	codex := func(tier string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-luna","stream":true,"input":"ping","service_tier":"`+tier+`"}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-plus")
		req.Header.Set("User-Agent", "codex_cli_rs/0.160.1")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
			t.Fatalf("%s: status %d: %s", tier, rec.Code, rec.Body.String())
		}
		return strings.Join(tried(), ",")
	}
	if got := codex("ultrafast"); got != "acct-pro:ultrafast" {
		t.Errorf("ultrafast tried %s", got)
	}
	if got := codex("priority"); got != "acct-plus:priority" {
		t.Errorf("priority tried %s", got)
	}
	proOut = true
	if got := codex("ultrafast"); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Errorf("pro out: ultrafast tried %s", got)
	}

	// an agent asking through magpie's own endpoint is ordered the same
	proOut = false
	if code, body := post(t, "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping","service_tier":"ultrafast"}`); code != 200 || !strings.HasPrefix(strings.Join(tried(), ","), "acct-pro:") {
		t.Errorf("/v1/responses: status %d: %s", code, body)
	}
}

// #1784, zhenghaowen0967: OMP, with gpt-6.1-sol switched fast in its
// picker (fastPicks.omp), asked for Ultrafast and went as priority, the
// fast mode's rewrite writing over the client's own, faster ask. It goes
// as Ultrafast to the account that offers it, and as priority to one that
// doesn't; a request that asked for no tier is sent fast as before.
func TestFastPickKeepsTheClientsUltrafast(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil)
	if err := provider.SetFastPick("omp", "codex/gpt-6-luna", true); err != nil {
		t.Fatal(err)
	}
	// at chatgpt.com, where Ultrafast is sent (buildResponses), reaching
	// the fake backend
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	omp := func(tier string) string {
		t.Helper()
		r := postFrom(t, s, "omp", "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping"`+tier+`}`)
		if len(r.Tries) == 0 || !r.Tries[0].Fast {
			t.Errorf("%s: not traced as sent fast: %+v", tier, r.Tries)
		}
		return strings.Join(tried(), ",")
	}
	if got := omp(`,"service_tier":"ultrafast"`); got != "acct-pro:ultrafast" {
		t.Errorf("ultrafast tried %s", got)
	}
	proOut = true
	if got := omp(`,"service_tier":"ultrafast"`); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Errorf("pro out: ultrafast tried %s", got)
	}
	proOut = false
	if got := omp(""); got != "acct-plus:priority" {
		t.Errorf("no tier asked: tried %s", got)
	}
	if b := withFast(provider.Chat, []byte(`{"model":"m","service_tier":"ultrafast"}`)); askedTier(b) != "ultrafast" {
		t.Errorf("chat: %s", b)
	}
}

// Codex's Fast for another agent (莫 on Discord: "gpt在自己里面有1.5倍速，
// 现在接入后别人agent怎么用1.5倍速呢"): a ChatGPT account's GPT switched
// fast in Claude Code's or OpenCode's picker (#954) goes to the ChatGPT
// backend at service_tier "priority", as Codex's own /fast sends it,
// whatever API the agent spoke; Claude Code's own speed "fast" asks it too.
// Not switched, and not asked, it goes at the default tier.
func TestCodexFastForOtherAgents(t *testing.T) {
	tried := tierAccounts(t, nil, nil)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	asks := []struct{ agent, path, body string }{
		{"claude", "/v1/messages", `{"model":"codex/gpt-6-luna","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`},
		{"opencode", "/v1/chat/completions", `{"model":"codex/gpt-6-luna","messages":[{"role":"user","content":"ping"}]}`},
	}
	last := func() string {
		got := tried()
		if len(got) == 0 {
			return ""
		}
		return got[len(got)-1]
	}
	for _, a := range asks {
		postFrom(t, s, a.agent, a.path, a.body)
		if got := last(); !strings.HasSuffix(got, ":") {
			t.Errorf("%s not switched: went at %q, want no tier", a.agent, got)
		}
		if err := provider.SetFastPick(a.agent, "codex/gpt-6-luna", true); err != nil {
			t.Fatal(err)
		}
		r := postFrom(t, s, a.agent, a.path, a.body)
		if got := last(); !strings.HasSuffix(got, ":priority") {
			t.Errorf("%s switched fast: went at %q, want priority", a.agent, got)
		}
		if len(r.Tries) == 0 || !r.Tries[0].Fast {
			t.Errorf("%s: not traced as sent fast: %+v", a.agent, r.Tries)
		}
	}
	// Claude Code's own fast mode, not switched in the picker
	postFrom(t, s, "claude-desktop", "/v1/messages", `{"model":"codex/gpt-6-luna","max_tokens":64,"speed":"fast","messages":[{"role":"user","content":"ping"}]}`)
	if got := last(); !strings.HasSuffix(got, ":priority") {
		t.Errorf("speed fast: went at %q, want priority", got)
	}
}

// #1784: the service tier the client asked for, the one each try went out
// at and the one the reply said it ran at are in the route, the usage log
// and its CSV, and the served one only when the reply said one.
func TestTierRecordedPerTry(t *testing.T) {
	proOut, echo := true, false
	tried := tierAccounts(t, &proOut, &echo)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	r := postFrom(t, s, "omp", "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping","service_tier":"ultrafast"}`)
	if got := strings.Join(tried(), ","); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Fatalf("tried %s", got)
	}
	sr := sessionRouteOf(r)
	if sr.AskedTier != "ultrafast" || len(sr.Tries) != 2 || sr.Tries[0].Tier != "ultrafast" || sr.Tries[1].Tier != "priority" ||
		sr.Tier != "priority" || sr.TierServed != "" || sr.Tries[1].TierServed != "" {
		t.Errorf("route: %+v", sr)
	}
	recs := usage.Load(time.Time{})
	if n := len(recs); n == 0 || recs[n-1].TierAsked != "ultrafast" || recs[n-1].TierSent != "priority" || recs[n-1].TierServed != "" {
		t.Fatalf("usage: %+v", recs)
	}

	// the reply says the tier it ran at
	proOut, echo = false, true
	r = postFrom(t, s, "omp", "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping","service_tier":"ultrafast"}`)
	tried()
	if sr := sessionRouteOf(r); sr.Tier != "ultrafast" || sr.TierServed != "ultrafast" {
		t.Errorf("route: %+v", sr)
	}
	// none asked: none in the log, and the ChatGPT backend's "default"
	// kept as unknown, which it is there (servedTierAt)
	postFrom(t, s, "omp", "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping"}`)
	tried()
	recs = usage.Load(time.Time{})
	if n := len(recs); n < 3 || recs[n-2].TierAsked != "ultrafast" || recs[n-2].TierSent != "ultrafast" || recs[n-2].TierServed != "ultrafast" ||
		recs[n-1].TierAsked != "" || recs[n-1].TierSent != "" || recs[n-1].TierServed != "" {
		t.Fatalf("usage: %+v", recs)
	}

	// the CSV's last columns
	var b strings.Builder
	first, _ := answersOf(recs)
	if err := usage.WriteCSV(&b, []usage.Row{{Record: first[0]}}); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil || len(cells) != 2 {
		t.Fatalf("%v %q", err, b.String())
	}
	for col, want := range map[string]string{"tier_asked": "ultrafast", "tier_sent": "priority", "tier_served": ""} {
		if i := slices.Index(usage.CSVHeader, col); i < 0 || cells[1][i] != want {
			t.Errorf("%s: %v", col, cells)
		}
	}

	// Codex's own sign-in, relayed as it sent it, asking the ChatGPT
	// backend for priority
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-luna","stream":true,"input":"ping","service_tier":"priority"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-plus")
	req.Header.Set("User-Agent", "codex_cli_rs/0.160.1")
	s.Handler().ServeHTTP(rec, req)
	tried()
	recs = usage.Load(time.Time{})
	if n := len(recs); rec.Code != 200 || recs[n-1].TierAsked != "priority" || recs[n-1].TierSent != "priority" || recs[n-1].TierServed != "priority" {
		t.Fatalf("codex: %d %+v", rec.Code, recs[len(recs)-1])
	}
}

// #1954, zhenghaowen0967: a fast pick switched to Ultrafast is asked for
// Ultrafast without the client saying a tier, first of the account that
// offers it, and for priority of one that doesn't when that one can't
// answer; whatever API the agent spoke. Another agent's requests go as
// they did, and a Chat client's own Ultrafast ask is kept too.
func TestUltraPickSentUltrafast(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	if !provider.UltraModels()["gpt-6-luna"] {
		t.Fatal("the Pro account's Ultrafast not seen")
	}
	if err := provider.SetFastTier("omp", "codex/gpt-6-luna", "ultrafast"); err != nil {
		t.Fatal(err)
	}
	if !provider.IsFastPick("omp", "codex/gpt-6-luna") || !provider.IsUltraPick("omp", "codex/gpt-6-luna") {
		t.Fatal("not picked")
	}
	send := func(agent, path, body string) string {
		t.Helper()
		r := postFrom(t, s, agent, path, body)
		if len(r.Tries) == 0 {
			t.Errorf("%s: no tries", agent)
		}
		return strings.Join(tried(), ",")
	}
	responses := `{"model":"codex/gpt-6-luna","input":"ping"}`
	if got := send("omp", "/v1/responses", responses); got != "acct-pro:ultrafast" {
		t.Errorf("omp: tried %s", got)
	}
	proOut = true
	if got := send("omp", "/v1/responses", responses); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Errorf("pro out: tried %s", got)
	}
	proOut = false
	if got := send("opencode", "/v1/responses", responses); got != "acct-plus:" {
		t.Errorf("another agent: tried %s", got)
	}

	chat := `{"model":"codex/gpt-6-luna","messages":[{"role":"user","content":"ping"}]}`
	messages := `{"model":"codex/gpt-6-luna","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`
	for _, a := range []struct{ agent, path, body string }{
		{"opencode", "/v1/chat/completions", chat},
		{"claude", "/v1/messages", messages},
	} {
		if err := provider.SetFastTier(a.agent, "codex/gpt-6-luna", "ultrafast"); err != nil {
			t.Fatal(err)
		}
		if got := send(a.agent, a.path, a.body); got != "acct-pro:ultrafast" {
			t.Errorf("%s: tried %s", a.agent, got)
		}
		// switched back to Fast, it is priority on the first account
		if err := provider.SetFastPick(a.agent, "codex/gpt-6-luna", true); err != nil {
			t.Fatal(err)
		}
		if provider.IsUltraPick(a.agent, "codex/gpt-6-luna") {
			t.Errorf("%s: still Ultrafast", a.agent)
		}
		if got := send(a.agent, a.path, a.body); got != "acct-plus:priority" {
			t.Errorf("%s fast: tried %s", a.agent, got)
		}
	}
	// a Chat client's own Ultrafast, not switched in the picker
	if got := send("cline", "/v1/chat/completions", `{"model":"codex/gpt-6-luna","service_tier":"ultrafast","messages":[{"role":"user","content":"ping"}]}`); got != "acct-pro:ultrafast" {
		t.Errorf("chat's own ultrafast: tried %s", got)
	}
	// off: none of it is kept
	if err := provider.SetFastPick("omp", "codex/gpt-6-luna", false); err != nil {
		t.Fatal(err)
	}
	if provider.IsUltraPick("omp", "codex/gpt-6-luna") {
		t.Error("Ultrafast kept after the pick was switched off")
	}
	if got := send("omp", "/v1/responses", responses); got != "acct-plus:" {
		t.Errorf("off: tried %s", got)
	}
}
