package provider

import (
	"context"
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
	var auths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Host+" "+r.Header.Get("Authorization"))
		var rep reply
		switch r.URL.Path {
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
	for _, a := range auths {
		if a != "api.mistral.ai Bearer vibe-key" {
			t.Errorf("asked %q, want api.mistral.ai with the key as a Bearer", a)
		}
	}
	if len(auths) != 2 {
		t.Errorf("asked %d times, want the budget and the subscription", len(auths))
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
