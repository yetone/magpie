package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Qwen AI platform Token Plan's sign-in (@imthe99galaxy on X: no quota
// for the plan) is its provider's own: the editor is told each one's and
// whose, and signing one out leaves the other signed in. A provider that
// isn't on the plan has no sign-in to start, ask or end.
func TestQianwenSignInRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const base = "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"
	for _, p := range []provider.Provider{
		{ID: "qwen-token-plan", Name: "Qwen Token Plan", Key: "sk-sp-1", Chat: base},
		{ID: "qwen-work", Name: "Qwen Work", Key: "sk-sp-2", Chat: base},
		{ID: "bailian", Name: "Bailian", Key: "sk-sp-3", Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	auth := filepath.Join(filepath.Dir(provider.Path()), "qianwen-auth.json")
	if err := os.WriteFile(auth, []byte(`{"qwen-token-plan":{"access":"a1","email":"a@example.com"},"qwen-work":{"access":"a2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	plans := func() map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID          string
				QianwenPlan *struct {
					SignedIn bool
					Account  string
				}
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, p := range got.Providers {
			if q := p.QianwenPlan; q != nil {
				out[p.ID] = "out"
				if q.SignedIn {
					out[p.ID] = "in " + q.Account
				}
			}
		}
		return out
	}
	if got := plans(); got["qwen-token-plan"] != "in a@example.com" || got["qwen-work"] != "in " || len(got) != 2 {
		t.Fatalf("before: %v", got)
	}
	for _, id := range []string{"bailian", "nobody"} {
		for _, c := range []struct{ method, path string }{{"POST", "/signin"}, {"GET", "/signin"}, {"POST", "/signin/cancel"}, {"POST", "/signout"}} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(c.method, "/api/qianwen/"+id+c.path, strings.NewReader("{}")))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s for %s: %d, want 404", c.method, c.path, id, w.Code)
			}
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/qianwen/qwen-work/signin", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":""`) {
		t.Errorf("no sign-in under way: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/qianwen/qwen-work/signin/cancel", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("cancel: %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/qianwen/qwen-token-plan/signout", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatalf("signout: %d %s", w.Code, w.Body)
	}
	if got := plans(); got["qwen-token-plan"] != "out" || got["qwen-work"] != "in " {
		t.Fatalf("after signing one out: %v", got)
	}
}
