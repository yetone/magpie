package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Jev preset saved with the endpoint its gateway's docs give keeps it —
// Cloudflare's names the account — and one saved without keeps the preset's.
func TestProviderSaveDecideEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	const own = "http://127.0.0.1:1/client/v4/accounts/abc123/ai/run"
	post(`{"id":"cloudflare-jev","preset":"cloudflare-jev","key":"k","decide":"` + own + `/ ","new":true}`)
	p, err := provider.Find("cloudflare-jev")
	if err != nil || p.Decide != own {
		t.Fatalf("%v %q", err, p.Decide)
	}
	// saved again without one, as a key-only edit from before: the preset's
	post(`{"id":"cloudflare-jev","from":"cloudflare-jev","preset":"cloudflare-jev"}`)
	pr, _ := provider.FromPreset("cloudflare-jev")
	if p, _ := provider.Find("cloudflare-jev"); p.Decide != pr.Decide {
		t.Fatalf("%q, want %q", p.Decide, pr.Decide)
	}
	// a preset with no decision API isn't given one
	post(`{"id":"deepseek","preset":"deepseek","key":"k","decide":"` + own + `","new":true}`)
	if p, _ := provider.Find("deepseek"); p.Decide != "" {
		t.Fatalf("deepseek got %q", p.Decide)
	}
}
