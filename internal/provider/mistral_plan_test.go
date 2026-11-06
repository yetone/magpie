package provider

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A Le Chat plan's Vibe key (dhanyqaz on Discord, a Student plan) is told
// its month of Vibe at Mistral's billing API, as Vibe reads it: the share
// used in euros, the reset, and the plan's name; a key with no Vibe budget
// has no card, and one refused says what Mistral said. The bodies are
// Vibe's own fixtures (tests/app_server/test_billing.py, test_account.py).
func TestMistralVibeBudget(t *testing.T) {
	type reply struct {
		status int
		body   string
	}
	var budget, sub reply
	who := reply{200, `{"plan_type":"CHAT","plan_name":"EDU","prompt_switching_to_pro_plan":false}`}
	var auths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Host+" "+r.Header.Get("Authorization"))
		var rep reply
		switch r.URL.Path {
		case "/api/vibe/whoami":
			rep = who
		case "/v1/billing/budget":
			rep = budget
		case "/v1/billing/subscription":
			rep = sub
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		w.Write([]byte(rep.body))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	p := Provider{ID: "mistral", Chat: "https://api.mistral.ai/v1", Key: "vibe-key"}
	src, ok := planQuotaSourceOf(p)
	if !ok || src.url != mistralBudgetURL || src.sure {
		t.Fatalf("source: %+v %v, want the budget, not sure", src, ok)
	}
	read := func() (string, []QuotaWindow, error) {
		auths = nil
		plan, ws, _, err := planKeyWindows(context.Background(), p, src, p.Key)
		return plan, ws, err
	}
	mistralKeyKinds.Clear()
	t.Cleanup(mistralKeyKinds.Clear)
	const vibe = `{"vibe_budget":{"usage_percentage":42.5,"initial_budget":300.0,"currency":"EUR","reset_at":"2026-11-01T00:00:00Z","payg_enabled":false},"api_budget":{"usage_percentage":3.0,"initial_budget":50.0,"currency":"EUR","reset_at":"2026-11-01T00:00:00Z"}}`

	budget = reply{200, vibe}
	sub = reply{200, `{"plan_name":"EDU","payg_enabled":true,"vibe_payg_enabled":true,"enterprise":false}`}
	plan, ws, err := read()
	if err != nil || plan != "Student" || len(ws) != 1 {
		t.Fatalf("student: %q %+v %v", plan, ws, err)
	}
	w := ws[0]
	reset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if w.Name != "Month" || w.Used != 42.5 || w.Limit != 300 || w.Amount != 127.5 || w.Unit != "EUR" || w.ResetsAt == nil || !w.ResetsAt.Equal(reset) || w.Span != 31*24*time.Hour {
		t.Errorf("window: %+v", w)
	}
	if w.Aside {
		t.Error("the budget's own payg_enabled false is the window's, over the subscription's")
	}
	want := []string{"console.mistral.ai Bearer vibe-key", "api.mistral.ai Bearer vibe-key", "api.mistral.ai Bearer vibe-key"}
	if strings.Join(auths, "|") != strings.Join(want, "|") {
		t.Errorf("asked %q, want whoami at the console, then the budget and the subscription, the key as a Bearer", auths)
	}

	// pay-as-you-go the subscription's alone: billed past the budget
	budget = reply{200, `{"vibe_budget":{"usage_percentage":120,"initial_budget":300.0,"currency":"EUR","reset_at":"2026-11-01T00:00:00Z"}}`}
	sub = reply{200, `{"plan_name":"INDIVIDUAL","vibe_payg_enabled":true}`}
	if plan, ws, err := read(); err != nil || plan != "Pro" || len(ws) != 1 || !ws[0].Aside || ws[0].Used != 100 || ws[0].Amount != 360 {
		t.Errorf("pro, payg: %q %+v %v", plan, ws, err)
	}

	// the subscription unread: the window stands, with no plan name
	budget, sub = reply{200, vibe}, reply{500, `{"message":"boom"}`}
	if plan, ws, err := read(); err != nil || plan != "" || len(ws) != 1 || ws[0].Used != 42.5 {
		t.Errorf("no subscription: %q %+v %v", plan, ws, err)
	}

	// a key with no Vibe budget: no windows, so no card, and nothing more asked
	for _, body := range []string{`{"vibe_budget":null,"api_budget":{"usage_percentage":3,"initial_budget":50}}`, `{"vibe_budget":{"usage_percentage":0,"initial_budget":0}}`, `{}`} {
		budget = reply{200, body}
		// whoami is asked once a key
		if _, ws, err := read(); err != nil || len(ws) != 0 || len(auths) != 1 {
			t.Errorf("%s: %+v %v, asked %d", body, ws, err, len(auths))
		}
	}

	// refused: Mistral's words
	budget = reply{401, `{"message":"Invalid API Key"}`}
	if _, _, err := read(); err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("refused: %v", err)
	}
	budget = reply{403, `{"message":"Forbidden"}`}
	if _, ws, err := read(); err == nil || len(ws) != 0 {
		t.Errorf("a member without the permission: %+v %v, want an error, and no card from it unless one was read", ws, err)
	}
}

// A Studio key (made in console.mistral.ai, whoami's plan_type "API") of
// the same organization is told the same budget body as its Vibe key, and
// spends the api_budget, not the Vibe month (dhanyqaz on Discord: the
// Studio card showed 149.36 / 150 USD, the Vibe key's, on a Student plan
// whose Studio month is 15). Its window is "API · Month", in the
// currency billing names, and never adds up with a Vibe month; a key
// whoami can't place has no card read, and Mistral Code's key none.
func TestMistralStudioBudget(t *testing.T) {
	type reply struct {
		status int
		body   string
	}
	// one organization's budget, as dhanyqaz's two keys are told it
	const budget = `{"vibe_budget":{"usage_percentage":0.43,"initial_budget":150.0,"currency":"USD","reset_at":"2026-12-01T00:00:00Z","payg_enabled":false},"api_budget":{"usage_percentage":20.0,"initial_budget":15.0,"currency":"USD","reset_at":"2026-12-01T00:00:00Z"}}`
	whos := map[string]reply{
		"Bearer vibe-key":   {200, `{"plan_type":"CHAT","plan_name":"EDU"}`},
		"Bearer studio-key": {200, `{"plan_type":"API","plan_name":"FREE","primitive_access_scope":"personal_and_shared"}`},
		"Bearer code-key":   {200, `{"plan_type":"MISTRAL_CODE","plan_name":"E"}`},
		"Bearer odd-key":    {200, `{"plan_name":"EDU"}`},
		"Bearer down-key":   {502, `{"message":"Bad Gateway"}`},
	}
	sub := reply{200, `{"plan_name":"EDU","payg_enabled":true,"vibe_payg_enabled":false,"enterprise":false}`}
	var asked []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		var rep reply
		switch r.URL.Path {
		case "/api/vibe/whoami":
			rep = whos[r.Header.Get("Authorization")]
		case "/v1/billing/budget":
			rep = reply{200, budget}
		case "/v1/billing/subscription":
			rep = sub
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		w.Write([]byte(rep.body))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	mistralKeyKinds.Clear()
	t.Cleanup(mistralKeyKinds.Clear)

	read := func(key string) (string, []QuotaWindow, error) {
		asked = nil
		p := Provider{ID: "mistral", Chat: "https://api.mistral.ai/v1", Key: key}
		src, _ := planQuotaSourceOf(p)
		plan, ws, _, err := planKeyWindows(context.Background(), p, src, key)
		return plan, ws, err
	}

	plan, vibe, err := read("vibe-key")
	if err != nil || plan != "Student" || len(vibe) != 1 {
		t.Fatalf("vibe key: %q %+v %v", plan, vibe, err)
	}
	if w := vibe[0]; w.Name != "Month" || w.Limit != 150 || w.Unit != "USD" || w.Aside {
		t.Errorf("vibe key's window: %+v", w)
	}

	plan, studio, err := read("studio-key")
	if err != nil || plan != "Student" || len(studio) != 1 {
		t.Fatalf("studio key: %q %+v %v", plan, studio, err)
	}
	w := studio[0]
	if w.Name != "API · Month" || w.Limit != 15 || w.Used != 20 || w.Amount != 3 || w.Unit != "USD" || w.Count(false) != "3 / 15 USD" {
		t.Errorf("studio key's window: %+v (%s), want the api_budget, 3 / 15 USD", w, w.Count(false))
	}
	if !w.Aside {
		t.Error("the organization's payg_enabled is the Studio month's, not vibe_payg_enabled")
	}
	if w.Name == vibe[0].Name {
		t.Error("a Studio month named as the Vibe month adds up with it on the accounts' strip")
	}
	// whoami asked once a key
	if _, _, _ = read("studio-key"); strings.Join(asked, " ") != "/v1/billing/budget /v1/billing/subscription" {
		t.Errorf("read again, asked %q, want no second whoami", asked)
	}

	// the currency billing names, else none: never one magpie picked
	for _, c := range []struct{ body, unit string }{
		{`"currency":"EUR"`, "EUR"}, {`"currency":" eur "`, "EUR"}, {`"currency":""`, ""}, {`"x":1`, ""},
	} {
		b := `{"api_budget":{"usage_percentage":10,"initial_budget":15,` + c.body + `}}`
		ws, err := readMistralBudget([]byte(b), mistralAPIKey, nil)
		if err != nil || len(ws) != 1 || ws[0].Unit != c.unit {
			t.Errorf("%s: %+v %v, want unit %q", b, ws, err, c.unit)
		}
	}

	// no api_budget, or none of it: no card, as a Vibe key with none
	for _, b := range []string{`{"vibe_budget":{"usage_percentage":1,"initial_budget":150}}`, `{"vibe_budget":{"usage_percentage":1,"initial_budget":150},"api_budget":{"usage_percentage":0,"initial_budget":0}}`} {
		if ws, err := readMistralBudget([]byte(b), mistralAPIKey, nil); err != nil || len(ws) != 0 {
			t.Errorf("%s: %+v %v, want no windows", b, ws, err)
		}
	}

	// Mistral Code's key, and a reply with no plan_type: no card
	for _, key := range []string{"code-key", "odd-key"} {
		if _, ws, _ := read(key); len(ws) != 0 {
			t.Errorf("%s: %+v, want no windows", key, ws)
		}
	}
	if _, _, err := read("odd-key"); err == nil {
		t.Error("whoami with no plan_type: want an error, not a kind")
	}
	// whoami down: unknown, so an error and no budget asked
	if _, ws, err := read("down-key"); err == nil || len(ws) != 0 || strings.Join(asked, " ") != "/api/vibe/whoami" {
		t.Errorf("whoami down: %+v %v, asked %q", ws, err, asked)
	}
	if _, ok := mistralKeyKinds.Load(sha256.Sum256([]byte("down-key"))); ok {
		t.Error("a failed whoami was kept as the key's kind")
	}
}
