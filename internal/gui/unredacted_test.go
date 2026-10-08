package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// lc on Discord: the editor's "Send requests unmasked" is saved on a
// preset added afresh (Ollama) and on one saved again, and /api/providers
// gives it back for the editor to open with.
func TestProviderSaveKeepsUnredacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	do := func(method, path, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	do("POST", "/api/provider/save", `{"id":"ollama","name":"Ollama","preset":"ollama","unredacted":true,"new":true}`)
	if p, err := provider.Find("ollama"); err != nil || !p.Unredacted || !p.SkipsRedaction() {
		t.Fatalf("a new Ollama: %+v %v", p, err)
	}
	if !strings.Contains(do("GET", "/api/providers", ""), `"unredacted":true`) {
		t.Fatal("the editor isn't told it is unmasked")
	}
	do("POST", "/api/provider/save", `{"id":"box","name":"Box","chat":"http://192.168.1.20:8000/v1","key":"k","unredacted":true,"new":true}`)
	do("POST", "/api/provider/save", `{"id":"box","from":"box","name":"Box","chat":"http://192.168.1.20:8000/v1","unredacted":false}`)
	if p, err := provider.Find("box"); err != nil || p.Unredacted {
		t.Fatalf("saved masked again: %+v %v", p, err)
	}
}
