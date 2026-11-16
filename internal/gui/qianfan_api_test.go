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

// A Baidu Qianfan Token Plan Personal provider is told where and how to
// bring its console sign-in, and whether one is kept (#5860); the
// Enterprise plan and pay as you go aren't, and no other provider can be
// posted a session or signed out here. A paste the console can't be asked
// with keeps nothing.
func TestQianfanSignInRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "baidu-qianfan", Name: "Baidu Qianfan", Key: "bce-v3/k1", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal"},
		{ID: "qianfan-team", Name: "Qianfan Team", Key: "bce-v3/k2", Chat: "https://qianfan.baidubce.com/v2/tokenplan/team"},
		{ID: "relay", Name: "Relay", Key: "k3", Chat: "http://127.0.0.1:1/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	auth := filepath.Join(filepath.Dir(provider.Path()), "qianfan-auth.json")
	if err := os.WriteFile(auth, []byte(`{"baidu-qianfan":{"cookie":"BDUSS=x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	plans := func() map[string]struct {
		SignedIn, Ended  bool
		URL, Bookmarklet string
	} {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID          string
				QianfanPlan *struct {
					SignedIn, Ended  bool
					URL, Bookmarklet string
				}
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string]struct {
			SignedIn, Ended  bool
			URL, Bookmarklet string
		}{}
		for _, p := range got.Providers {
			if p.QianfanPlan != nil {
				out[p.ID] = *p.QianfanPlan
			}
		}
		return out
	}
	got := plans()
	if len(got) != 1 || !got["baidu-qianfan"].SignedIn || !strings.HasPrefix(got["baidu-qianfan"].Bookmarklet, "javascript:") ||
		!strings.HasPrefix(got["baidu-qianfan"].URL, "https://console.bce.baidu.com/") {
		t.Fatalf("before: %+v", got)
	}
	if got["baidu-qianfan"].Ended {
		t.Fatalf("a sign-in the console takes reads as ended: %+v", got)
	}
	// one the console has ended is told, so the editor asks for a new one
	if err := os.WriteFile(auth, []byte(`{"baidu-qianfan":{"cookie":"BDUSS=x","ended":"2026-11-16T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := plans(); !got["baidu-qianfan"].SignedIn || !got["baidu-qianfan"].Ended {
		t.Fatalf("ended: %+v", got)
	}
	for _, id := range []string{"relay", "qianfan-team", "nobody"} {
		for _, path := range []string{"session", "signout"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/qianfan/"+id+"/"+path, strings.NewReader(`{"text":"BDUSS=y"}`)))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s for %s: %d, want 404", path, id, w.Code)
			}
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/qianfan/baidu-qianfan/session", strings.NewReader(`{"text":"hello"}`)))
	if w.Code == 200 {
		t.Fatalf("a paste with no cookie was taken: %s", w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/qianfan/baidu-qianfan/signout", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatalf("signout: %d %s", w.Code, w.Body)
	}
	if got := plans(); got["baidu-qianfan"].SignedIn {
		t.Fatalf("after signing out: %+v", got)
	}
}
