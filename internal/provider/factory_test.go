package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// factorySite is a fake WorkOS under /wos and a fake Factory API at the
// root, its EU region under /eu.
func factorySite(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	oldW, oldA, oldE := factoryWorkOS, factoryAPI, factoryAPIEU
	factoryWorkOS, factoryAPI, factoryAPIEU = srv.URL+"/wos", srv.URL, srv.URL+"/eu"
	factoryAsked.Clear()
	t.Cleanup(func() { factoryWorkOS, factoryAPI, factoryAPIEU = oldW, oldA, oldE; factoryAsked.Clear() })
	return srv
}

func factoryJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func factoryToken(claims map[string]any) string {
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = float64(time.Now().Add(time.Hour).Unix())
	}
	return fakeJWT(claims)
}

func factoryUsers() string {
	var s []string
	for _, l := range Logins("factory") {
		s = append(s, l.User+map[bool]string{true: "*", false: ""}[l.Active]+map[bool]string{true: "+", false: ""}[l.On])
	}
	return strings.Join(s, " ")
}

// A sign-in as droid runs it: the device code shown, the poll told to wait
// and to slow down, a token with no org put in the account's org, and the
// org's region read; then the account routes, signs and meters in that region.
func TestFactorySignIn(t *testing.T) {
	signIn(t)
	first := factoryToken(map[string]any{"sub": "user_1"})
	inOrg := factoryToken(map[string]any{"sub": "user_1", "org_id": "org_A"})
	var mu sync.Mutex
	var polls int
	var renewedWith string
	factorySite(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/wos/authorize/device":
			if r.Form.Get("client_id") != factoryClientID {
				factoryJSON(w, 400, map[string]any{"error": "invalid_client"})
				return
			}
			factoryJSON(w, 200, map[string]any{"device_code": "dev-1", "user_code": "WDJB-MJHT",
				"verification_uri": "https://factory.example/device", "verification_uri_complete": "https://factory.example/device?code=WDJB-MJHT", "interval": 0})
		case "/wos/authenticate":
			mu.Lock()
			defer mu.Unlock()
			switch r.Form.Get("grant_type") {
			case "urn:ietf:params:oauth:grant-type:device_code":
				if r.Form.Get("device_code") != "dev-1" {
					factoryJSON(w, 400, map[string]any{"error": "invalid_grant"})
					return
				}
				polls++
				switch polls {
				case 1:
					factoryJSON(w, 400, map[string]any{"error": "authorization_pending"})
				case 2:
					factoryJSON(w, 400, map[string]any{"error": "slow_down"})
				default:
					factoryJSON(w, 200, map[string]any{"access_token": first, "refresh_token": "r1",
						"user": map[string]any{"id": "user_1", "email": "ada@example.com"}})
				}
			case "refresh_token":
				renewedWith = r.Form.Get("refresh_token") + "@" + r.Form.Get("organization_id")
				factoryJSON(w, 200, map[string]any{"access_token": inOrg, "refresh_token": "r2", "organization_id": "org_A"})
			}
		case "/api/cli/org":
			// droid asks it with the bearer token alone
			if r.Header.Get("Authorization") != "Bearer "+first || r.Header.Get("X-Factory-Org-Id") != "" {
				w.WriteHeader(401)
				return
			}
			factoryJSON(w, 200, map[string]any{"workosOrgIds": []string{"org_A", "org_B"}})
		case "/api/cli/whoami":
			// droid's whoami sends the token and the extended flag alone
			if r.Header.Get("Authorization") != "Bearer "+inOrg || r.Header.Get("X-Factory-Whoami-Extended") != "true" ||
				r.Header.Get("X-Factory-Org-Id") != "" || r.Header.Get("X-Factory-Client") != "" || r.Header.Get("X-Client-Version") != "" {
				w.WriteHeader(401)
				return
			}
			// Factory's own id for the org, not WorkOS's
			factoryJSON(w, 200, map[string]any{"userId": "user_1", "orgId": "fac_A", "region": "eu"})
		case "/eu/api/billing/limits":
			if r.Header.Get("Authorization") != "Bearer "+inOrg {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, `{"limits":{
				"standard":{"fiveHour":{"usedPercent":42,"windowEnd":"2026-10-01T05:00:00Z"},
					"weekly":{"usedPercent":10,"windowEnd":1791763200000},
					"monthly":{"usedPercent":130,"windowEnd":"1793000000000"}},
				"core":{"fiveHour":{"usedPercent":100,"windowEnd":"2026-10-01T05:00:00Z"}}},
				"extraUsageBalanceCents":1250,"extraUsageAllowed":true}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})

	st, err := StartSignIn("factory")
	if err != nil || st.Code != "WDJB-MJHT" || st.URL != "https://factory.example/device?code=WDJB-MJHT" {
		t.Fatalf("start: %+v %v", st, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" || st.User != "ada@example.com" || !st.Using {
		t.Fatalf("done: %+v", st)
	}
	if polls != 3 || renewedWith != "r1@org_A" {
		t.Fatalf("polls %d, renewed with %q", polls, renewedWith)
	}
	if got := factoryUsers(); got != "ada@example.com*+" {
		t.Fatalf("logins: %s", got)
	}
	l, _ := factoryLookup("ada@example.com")
	c, _ := factorySaved(l)
	if c.Access != inOrg || c.Refresh != "r2" || c.Org != "org_A" || c.Active != "fac_A" || c.Region != "eu" || c.ExpiresAt == 0 {
		t.Fatalf("kept: org %q active %q region %q refresh %q", c.Org, c.Active, c.Region, c.Refresh)
	}

	p, ok := find(Accounts(), "factory")
	if !ok || p.Account.User != "ada@example.com" {
		t.Fatalf("not an account: %+v", p)
	}
	for model, want := range map[string]Protocol{"claude-opus-5-5": Anthropic, "gpt-5.5": Responses, "grok-4.7": Responses,
		"glm-5.3": Chat, "minimax-m2.7": Anthropic} {
		if got := p.APIs(model); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %v, want %s", model, got, want)
		}
	}

	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, []byte(`{"model":"claude-opus-5-5"}`)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != factoryAPIEU+"/api/llm/a/v1/messages" {
		t.Errorf("not sent to the EU region: %s", req.URL)
	}
	h := req.Header
	if h.Get("Authorization") != "Bearer "+inOrg || h.Get("X-Factory-Org-Id") != "fac_A" || h.Get("X-Factory-Client") != "cli" ||
		h.Get("User-Agent") != "factory-cli/"+factoryVersion || h.Get("x-api-provider") != "anthropic" ||
		h.Get("x-session-id") == "" || h.Get("x-assistant-message-id") == "" || h.Get("OpenAI-Platform") != "" {
		t.Errorf("claude headers: %v", h)
	}
	req, _ = http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, []byte(`{"model":"gpt-5.5"}`)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != factoryAPIEU+"/api/llm/o/v1/responses" || req.Header.Get("x-api-provider") != "openai" || req.Header.Get("OpenAI-Platform") == "" {
		t.Errorf("gpt: %s %v", req.URL, req.Header)
	}
	req, _ = http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	_ = p.Sign(context.Background(), req, Chat, []byte(`{"model":"glm-5.3"}`))
	if req.Header.Get("x-api-provider") != "fireworks" {
		t.Errorf("glm: %v", req.Header)
	}

	var q SubscriptionQuota
	for _, v := range LoginUsage(context.Background(), "factory") {
		q = v
	}
	if q.Error != "" || len(q.Windows) != 5 {
		t.Fatalf("usage: %+v", q)
	}
	ws := map[string]QuotaWindow{}
	for _, w := range q.Windows {
		ws[w.Name] = w
	}
	five, week, month, core, extra := ws["5 hours"], ws["7 days"], ws["30 days"], ws["Droid Core · 5 hours"], ws["Extra usage"]
	if five.Used != 42 || five.Span != 5*time.Hour || five.ResetsAt == nil || !five.ResetsAt.Equal(time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("5 hours: %+v", five)
	}
	if week.ResetsAt == nil || week.ResetsAt.UnixMilli() != 1791763200000 || week.Span != 7*24*time.Hour {
		t.Errorf("weekly: %+v", week)
	}
	if month.Used != 100 || month.ResetsAt == nil || month.ResetsAt.UnixMilli() != 1793000000000 {
		t.Errorf("monthly: %+v", month)
	}
	if extra.Display != "$12.50" || !extra.Aside || !five.Aside || !core.Aside {
		t.Errorf("extra usage: %+v %+v", extra, five)
	}
	if !five.matches("claude-opus-5-5") || five.matches("glm-5.3") || !core.matches("glm-5.3") || core.matches("gpt-5.5") {
		t.Error("standard counts the vendors' models, Droid Core the hosted ones")
	}
}

// saveFactory keeps an account as a sign-in would, its token ending at exp.
func saveFactory(t *testing.T, user, refresh string, exp time.Time) {
	t.Helper()
	c := factoryCreds{Access: factoryToken(map[string]any{"exp": float64(exp.Unix()), "sub": user}), Refresh: refresh,
		ExpiresAt: exp.UnixMilli(), Org: "org_" + user, Active: "fac_" + user, Email: user}
	auth, _ := json.Marshal(c)
	if err := addSideLogin(savedLogin{Agent: "factory", User: user, Auth: auth}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
}

// A token near its end is renewed once, the rotated refresh token kept; one
// WorkOS refuses marks the account lapsed until it is signed in again.
func TestFactoryRefresh(t *testing.T) {
	signIn(t)
	var mu sync.Mutex
	renewals := 0
	fresh := factoryToken(map[string]any{"sub": "bo"})
	factorySite(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		switch {
		// droid's refresh names no org: WorkOS keeps the token's
		case r.URL.Path == "/wos/authenticate" && r.Form.Get("refresh_token") == "r-old" && !r.Form.Has("organization_id"):
			renewals++
			factoryJSON(w, 200, map[string]any{"access_token": fresh, "refresh_token": "r-new"})
		case r.URL.Path == "/wos/authenticate":
			factoryJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "Refresh token already used"})
		case r.URL.Path == "/api/cli/whoami":
			factoryJSON(w, 200, map[string]any{"userId": "bo", "region": "us"})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	saveFactory(t, "bo", "r-old", time.Now().Add(time.Minute))
	p, ok := find(Accounts(), "factory")
	if !ok {
		t.Fatal("no Factory account")
	}
	for range 2 {
		req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
		if err := p.Sign(context.Background(), req, Anthropic, []byte(`{"model":"claude-sonnet-5"}`)); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer "+fresh || req.URL.String() != factoryAPI+"/api/llm/a/v1/messages" {
			t.Fatalf("signed with %s at %s", req.Header.Get("Authorization"), req.URL)
		}
	}
	l, _ := factoryLookup("bo")
	if c, _ := factorySaved(l); renewals != 1 || c.Refresh != "r-new" || c.Access != fresh {
		t.Fatalf("renewals %d, refresh %q", renewals, c.Refresh)
	}

	saveFactory(t, "bo", "r-spent", time.Now().Add(-time.Minute))
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("a refused refresh: %v", err)
	}
	if ls := Logins("factory"); len(ls) != 1 || ls[0].Lapsed == "" {
		t.Fatalf("not lapsed: %+v", ls)
	}
	if q := factoryLoginQuota(context.Background(), Login{User: "bo"}); q.Error == "" {
		t.Fatal("usage of a lapsed account")
	}
	// signed in again, it is back
	if _, err := factorySignedInWith(context.Background(), factoryTokens{Access: fresh, Refresh: "r3", Org: "org_bo",
		User: struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}{ID: "bo", Email: "bo"}}); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("factory"); len(ls) != 1 || ls[0].Lapsed != "" {
		t.Fatalf("still lapsed: %+v", ls)
	}
}

// Several accounts: the first in use, the rest behind it while on, each
// signing with its own token; switched, turned off and forgotten by name.
func TestFactoryAccounts(t *testing.T) {
	signIn(t)
	factorySite(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected %s", r.URL.Path) })
	saveFactory(t, "ada", "ra", time.Now().Add(time.Hour))
	saveFactory(t, "bo", "rb", time.Now().Add(time.Hour))
	if got := factoryUsers(); got != "ada*+ bo+" {
		t.Fatalf("logins: %s", got)
	}
	p, _ := find(All(), "factory")
	also := p.AlsoOn()
	if p.Account.User != "ada" || len(also) != 1 || also[0].Account.User != "bo" {
		t.Fatalf("first %+v, also %+v", p.Account, also)
	}
	req, _ := http.NewRequest("POST", also[0].Anthropic+"/v1/messages", nil)
	if err := also[0].Sign(context.Background(), req, Anthropic, []byte(`{}`)); err != nil || req.Header.Get("X-Factory-Org-Id") != "fac_bo" {
		t.Fatalf("bo signs as itself: %v %v", err, req.Header)
	}
	if err := SetLoginOn("factory", "bo", false); err != nil {
		t.Fatal(err)
	}
	if got := factoryUsers(); got != "ada*+ bo" {
		t.Fatalf("after off: %s", got)
	}
	if err := SwitchLogin("factory", "bo"); err != nil {
		t.Fatal(err)
	}
	if got := factoryUsers(); !strings.HasPrefix(got, "bo*+") {
		t.Fatalf("after switch: %s", got)
	}
	if err := ForgetLogin("factory", "ada"); err != nil {
		t.Fatal(err)
	}
	if got := factoryUsers(); got != "bo*+" {
		t.Fatalf("after forget: %s", got)
	}
}

// factoryOrgSite stands in for Factory as it answered tasselx (#242): a
// request whose X-Factory-Org-Id isn't one of Factory's own org ids the user
// is in, or with no header a token whose org the user has left, gets
// "Requested active organization is not accessible by this user". A WorkOS
// org id (org_…) in the header is one of those. tokens maps each access
// token to the WorkOS org it carries; a refresh into org_A gives renewed.
// It answers with the orgs a refresh was asked to put a token in.
func factoryOrgSite(t *testing.T, tokens map[string]string, renewed string) func() []string {
	t.Helper()
	var mu sync.Mutex
	into := []string{}
	refused := `{"type":"error","error":{"type":"permission_error","message":"Requested active organization is not accessible by this user. If you think this is an error, please update to the latest client version, then refresh or restart your client."}}`
	allowed := func(r *http.Request) (ok, known bool) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		org, known := tokens[tok]
		if !known {
			return false, false
		}
		if h := r.Header.Get("X-Factory-Org-Id"); h != "" {
			return h == "fac_A", true // Factory's id for org_A, the one org the user is in
		}
		return org == "org_A", true
	}
	factorySite(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/api/cli/whoami":
			if r.Header.Get("X-Factory-Org-Id") != "" {
				t.Errorf("whoami asked with an org header: %q", r.Header.Get("X-Factory-Org-Id"))
			}
			if tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] != "org_A" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				io.WriteString(w, refused)
				return
			}
			factoryJSON(w, 200, map[string]any{"userId": "user_1", "orgId": "fac_A", "region": "us"})
		case "/api/cli/org":
			factoryJSON(w, 200, map[string]any{"workosOrgIds": []string{"org_A"}})
		case "/wos/authenticate":
			mu.Lock()
			into = append(into, r.Form.Get("organization_id"))
			mu.Unlock()
			if r.Form.Get("organization_id") != "org_A" {
				factoryJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			factoryJSON(w, 200, map[string]any{"access_token": renewed, "refresh_token": "r-A"})
		case "/api/llm/a/v1/messages", "/api/billing/limits":
			ok, known := allowed(r)
			if !known {
				w.WriteHeader(401)
				return
			}
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				io.WriteString(w, refused)
				return
			}
			if r.URL.Path == "/api/billing/limits" {
				io.WriteString(w, `{"limits":{"standard":{"fiveHour":{"usedPercent":7}}}}`)
				return
			}
			io.WriteString(w, `{"type":"message"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), into...)
	}
}

// factorySend signs a Claude request as the account and sends it, asking
// the account once more after a 403 it can mend, as the gateway's forward
// does.
func factorySend(t *testing.T, p Provider) (int, string) {
	t.Helper()
	body := []byte(`{"model":"claude-opus-5-5"}`)
	send := func() (int, []byte) {
		req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", strings.NewReader(string(body)))
		if err := p.Sign(context.Background(), req, Anthropic, body); err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	code, b := send()
	if code == 403 && p.Retry(context.Background(), nil, code, b) {
		code, b = send()
	}
	return code, string(b)
}

func factoryKept(t *testing.T, user string) factoryCreds {
	t.Helper()
	l, _ := factoryLookup(user)
	c, _ := factorySaved(l)
	return c
}

// #242: a Factory account signed in and put in its org was refused every
// request with "Requested active organization is not accessible by this
// user", magpie having sent WorkOS's org id as X-Factory-Org-Id. droid sends
// Factory's own id, from whoami, or none; so does magpie now, and a login
// kept with the WorkOS id sends none.
func TestFactoryActiveOrg(t *testing.T) {
	signIn(t)
	tokA := factoryToken(map[string]any{"sub": "user_1", "org_id": "org_A"})
	factoryOrgSite(t, map[string]string{tokA: "org_A"}, "")

	// signed in with a token WorkOS put in org_A
	user, err := factorySignedInWith(context.Background(), factoryTokens{Access: tokA, Refresh: "r1", Org: "org_A",
		User: struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}{ID: "user_1", Email: "tassel@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if c := factoryKept(t, user); c.Active != "fac_A" || c.Org != "org_A" {
		t.Fatalf("kept active %q org %q", c.Active, c.Org)
	}
	p, _ := find(Accounts(), "factory")
	if code, b := factorySend(t, p); code != 200 {
		t.Fatalf("signed in: %d %s", code, b)
	}

	// a login kept by v0.1.432: the WorkOS org, no active org
	auth, _ := json.Marshal(map[string]any{"accessToken": tokA, "refreshToken": "r1",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "orgId": "org_A", "email": "old@example.com"})
	if err := addSideLogin(savedLogin{Agent: "factory", User: "old@example.com", Auth: auth}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("factory", "old@example.com"); err != nil {
		t.Fatal(err)
	}
	p, _ = find(Accounts(), "factory")
	if p.Account.User != "old@example.com" {
		t.Fatalf("in use: %s", p.Account.User)
	}
	if code, b := factorySend(t, p); code != 200 {
		t.Fatalf("an older login: %d %s", code, b)
	}
	// it asked whoami, as droid does for a token it holds, and keeps the org
	if c := factoryKept(t, "old@example.com"); c.Active != "fac_A" {
		t.Fatalf("older login kept active %q", c.Active)
	}
}

// Factory refusing the active org magpie sends: it is dropped and the
// request goes again without it, as droid's org picker retries "without the
// active-org header"; a token in an org the user has left is put in the
// first org /api/cli/org lists, as droid does for a token with none. The
// usage read mends it the same way; any other 403 is left alone.
func TestFactoryOrgRefused(t *testing.T) {
	signIn(t)
	tokA := factoryToken(map[string]any{"sub": "user_1", "org_id": "org_A"})
	tokGone := factoryToken(map[string]any{"sub": "user_1", "org_id": "org_gone"})
	tokIn := factoryToken(map[string]any{"sub": "user_1", "org_id": "org_A", "n": 2})
	renewed := factoryOrgSite(t, map[string]string{tokA: "org_A", tokGone: "org_gone", tokIn: "org_A"}, tokIn)
	save := func(user, access, active string) {
		c := factoryCreds{Access: access, Refresh: "r-" + user, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
			Org: "org_A", Active: active, Email: user}
		auth, _ := json.Marshal(c)
		if err := addSideLogin(savedLogin{Agent: "factory", User: user, Auth: auth}, "", func(savedLogin) {}); err != nil {
			t.Fatal(err)
		}
		if err := SwitchLogin("factory", user); err != nil {
			t.Fatal(err)
		}
	}

	// an active org the user is no longer in
	save("ada", tokA, "fac_gone")
	p, _ := find(Accounts(), "factory")
	if code, b := factorySend(t, p); code != 200 {
		t.Fatalf("active org gone: %d %s", code, b)
	}
	// whoami, asked again without it, names the org the token is in
	if c := factoryKept(t, "ada"); c.Active != "fac_A" || c.Access != tokA {
		t.Fatalf("ada kept active %q", c.Active)
	}
	if got := renewed(); len(got) != 0 {
		t.Fatalf("renewed %v for a header alone", got)
	}

	// no header, but the token's org is one the user has left
	save("bo", tokGone, "")
	p, _ = find(Accounts(), "factory")
	if code, b := factorySend(t, p); code != 200 {
		t.Fatalf("token's org gone: %d %s", code, b)
	}
	if c := factoryKept(t, "bo"); c.Access != tokIn || c.Refresh != "r-A" || c.Org != "org_A" {
		t.Fatalf("bo kept %+v", c)
	}
	if got := strings.Join(renewed(), ","); got != "org_A" {
		t.Fatalf("renewed into %s", got)
	}

	// the usage read, the same
	save("cy", tokA, "fac_gone")
	if q := factoryLoginQuota(context.Background(), Login{User: "cy"}); q.Error != "" || len(q.Windows) != 1 {
		t.Fatalf("usage: %+v", q)
	}

	// any other refusal to an account that sent its org is not retried
	if err := SwitchLogin("factory", "cy"); err != nil {
		t.Fatal(err)
	}
	p, _ = find(Accounts(), "factory")
	if c := factoryKept(t, "cy"); c.Active != "fac_A" {
		t.Fatalf("cy kept active %q", c.Active)
	}
	if p.Retry(context.Background(), nil, 403, []byte(`{"error":{"message":"model not allowed"}}`)) ||
		p.Retry(context.Background(), nil, 401, []byte(`Requested active organization is not accessible`)) {
		t.Fatal("retried an unrelated refusal")
	}
}

// #242 after v0.1.438: a login kept with no active org sent no
// X-Factory-Org-Id, and Factory answered GLM-5.2 on chat completions with a
// bare 403 {"detail":"Forbidden"}. droid never sends a request without it:
// it asks whoami for each token it holds (droid's auth Do → _r → Ar) and
// keeps the orgId. magpie asks too, with droid's whoami headers alone, and
// sends what droid sends; whoami failing at first, the 403 asks it again and
// the request goes once more. An org served from a host of its own gets its
// model requests there, and a 403 left over says what to do.
func TestFactoryForbiddenWithoutOrg(t *testing.T) {
	signIn(t)
	tok := factoryToken(map[string]any{"sub": "user_t", "org_id": "org_T"})
	forbidden := `{"detail":"Forbidden","status":403,"title":"Forbidden"}`
	var mu sync.Mutex
	whoamis, whoamiDown := 0, 0
	prem := ""
	var sent []http.Header
	var paths []string
	srv := factorySite(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/cli/whoami":
			whoamis++
			h := r.Header
			if h.Get("Authorization") != "Bearer "+tok || h.Get("X-Factory-Whoami-Extended") != "true" || h.Get("X-Factory-Org-Id") != "" ||
				h.Get("X-Factory-Client") != "" || h.Get("X-Client-Version") != "" || strings.HasPrefix(h.Get("User-Agent"), "factory-cli") {
				t.Errorf("whoami headers: %v", h)
			}
			if whoamiDown > 0 {
				whoamiDown--
				w.WriteHeader(502)
				return
			}
			factoryJSON(w, 200, map[string]any{"userId": "user_t", "orgId": "fac_T", "email": "tassel@example.com",
				"region": "us", "premBaseHostV2": prem})
		case "/api/llm/o/v1/chat/completions", "/api/llm/a/v1/messages", "/prem/api/llm/a/v1/messages":
			if r.Header.Get("X-Factory-Org-Id") != "fac_T" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				io.WriteString(w, forbidden)
				return
			}
			sent = append(sent, r.Header.Clone())
			paths = append(paths, r.URL.Path)
			io.WriteString(w, `{"id":"ok"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	// kept by v0.1.432: the WorkOS org and no active org
	keep := func(user string) Provider {
		t.Helper()
		auth, _ := json.Marshal(map[string]any{"accessToken": tok, "refreshToken": "r1",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "orgId": "org_T", "email": user})
		if err := addSideLogin(savedLogin{Agent: "factory", User: user, Auth: auth}, "", func(savedLogin) {}); err != nil {
			t.Fatal(err)
		}
		if err := SwitchLogin("factory", user); err != nil {
			t.Fatal(err)
		}
		p, _ := find(Accounts(), "factory")
		return p
	}
	send := func(p Provider, url string, proto Protocol, body string) (int, string) {
		t.Helper()
		do := func() (int, []byte) {
			req, _ := http.NewRequest("POST", url, strings.NewReader(body))
			if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return res.StatusCode, b
		}
		code, b := do()
		if code == 403 && p.Retry(context.Background(), nil, code, b) {
			code, b = do()
		}
		return code, string(b)
	}
	glm := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`

	// whoami answers: the first request carries the org, as droid's would
	p := keep("tassel@example.com")
	if code, b := send(p, p.Chat+"/chat/completions", Chat, glm); code != 200 {
		t.Fatalf("glm-5.2: %d %s", code, b)
	}
	if whoamis != 1 || len(sent) != 1 {
		t.Fatalf("whoami asked %d times, %d requests through", whoamis, len(sent))
	}
	h := sent[0]
	for k, want := range map[string]string{
		"Authorization": "Bearer " + tok, "X-Factory-Client": "cli", "X-Client-Version": factoryVersion,
		"User-Agent": "factory-cli/" + factoryVersion, "X-Factory-Org-Id": "fac_T", "x-api-provider": "baseten",
		"x-session-id": factorySession, "x-provider-routing-source": "registry_default",
		"OpenAI-Platform": "", "X-Api-Key": "",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if h.Get("x-assistant-message-id") == "" {
		t.Error("no x-assistant-message-id")
	}
	if c := factoryKept(t, "tassel@example.com"); c.Active != "fac_T" {
		t.Fatalf("kept active %q", c.Active)
	}
	// kept: not asked again
	if code, _ := send(p, p.Chat+"/chat/completions", Chat, glm); code != 200 || whoamis != 1 {
		t.Fatalf("second request: %d, whoami %d", code, whoamis)
	}

	// Claude goes on Anthropic's Messages with droid's placeholder key
	if code, b := send(p, p.Anthropic+"/v1/messages", Anthropic, `{"model":"claude-opus-5-5"}`); code != 200 {
		t.Fatalf("claude: %d %s", code, b)
	}
	if got := sent[len(sent)-1]; got.Get("X-Api-Key") != "placeholder" || got.Get("x-api-provider") != "anthropic" {
		t.Errorf("claude headers: %v", got)
	}

	// whoami down at first: the request is refused, whoami asked again, and
	// it goes once more with the org
	whoamiDown = 1
	p = keep("down@example.com")
	if code, b := send(p, p.Chat+"/chat/completions", Chat, glm); code != 200 {
		t.Fatalf("after a failed whoami: %d %s", code, b)
	}
	if c := factoryKept(t, "down@example.com"); c.Active != "fac_T" || whoamis != 3 {
		t.Fatalf("kept active %q, whoami %d", c.Active, whoamis)
	}

	// an org Factory serves from a host of its own
	prem = srv.URL + "/prem"
	p = keep("prem@example.com")
	if code, b := send(p, p.Anthropic+"/v1/messages", Anthropic, `{"model":"claude-opus-5-5"}`); code != 200 {
		t.Fatalf("prem: %d %s", code, b)
	}
	if got := paths[len(paths)-1]; got != "/prem/api/llm/a/v1/messages" {
		t.Errorf("prem request went to %s", got)
	}

	// a 403 still answered says what to do: the request already opens as
	// Droid (#242, #506), so signing in again changes nothing
	msg := p.Explain("Factory: Forbidden", 403, []byte(forbidden))
	if !strings.HasPrefix(msg, "Factory: Forbidden — ") || !strings.Contains(msg, "only from Droid") || !strings.Contains(msg, "Droid's line") ||
		strings.Contains(msg, "sign in to it again") {
		t.Errorf("explained: %s", msg)
	}
	if got := p.Explain("Factory: overloaded", 529, nil); got != "Factory: overloaded" {
		t.Errorf("a 529 explained: %s", got)
	}
}
