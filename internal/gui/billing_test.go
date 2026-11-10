package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// EV_John_Liu on X: whether a provider is on a plan or pay as you go is
// marked on Providers and on a routing group's members. /api/providers
// gives magpie's reading (billingKind) and the user's word (billing),
// the editor's save keeps the word on a preset and on a typed-in
// provider, refuses a value that is neither, and a group's member says
// its provider's billing.
func TestProviderBilling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	do := func(method, path, body string, code int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != code {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	do("POST", "/api/provider/save", `{"id":"openrouter","name":"OpenRouter","preset":"openrouter","key":"k","new":true}`, 200)
	if !strings.Contains(do("GET", "/api/providers", "", 200), `"billingKind":"payg"`) {
		t.Fatal("/api/providers doesn't say OpenRouter is pay as you go")
	}
	do("POST", "/api/provider/save", `{"id":"openrouter","from":"openrouter","name":"OpenRouter","preset":"openrouter","key":"","billing":"plan"}`, 200)
	if p, err := provider.Find("openrouter"); err != nil || p.Billing != provider.BillingPlan || p.BillingKind() != provider.BillingPlan {
		t.Fatalf("the preset's word wasn't kept: %+v %v", p.Billing, err)
	}
	if out := do("GET", "/api/providers", "", 200); !strings.Contains(out, `"billing":"plan"`) || !strings.Contains(out, `"billingKind":"plan"`) {
		t.Fatal("/api/providers doesn't give the word back")
	}
	do("POST", "/api/provider/save", `{"id":"openrouter","from":"openrouter","name":"OpenRouter","preset":"openrouter","key":"","billing":"mixed"}`, 400)
	if p, _ := provider.Find("openrouter"); p.Billing != provider.BillingPlan {
		t.Fatalf("a refused value changed it: %q", p.Billing)
	}
	do("POST", "/api/provider/save", `{"id":"openrouter","from":"openrouter","name":"OpenRouter","preset":"openrouter","key":"","billing":""}`, 200)
	if p, _ := provider.Find("openrouter"); p.Billing != "" {
		t.Fatalf("Auto left the word: %q", p.Billing)
	}

	do("POST", "/api/provider/save", `{"id":"box","name":"Box","chat":"http://127.0.0.1:9/v1","key":"k","models":["m"],"billing":"payg","new":true}`, 200)
	if p, err := provider.Find("box"); err != nil || p.Billing != provider.BillingPAYG {
		t.Fatalf("a typed-in provider's word wasn't kept: %+v %v", p.Billing, err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"box/m"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupsState().Groups {
		if g.ID != "g" {
			continue
		}
		if len(g.Info) != 1 || g.Info[0].Billing != provider.BillingPAYG {
			t.Fatalf("the member doesn't say its provider's billing: %+v", g.Info)
		}
		return
	}
	t.Fatal("group g not listed")
}
