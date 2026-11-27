package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An account signed in from another process after the gateway started
// serving has its models listed without a restart (#7945; adapted from
// kkkzbh's patch for #1805).
func TestServingFetchesAnAccountSignedInAfterStartup(t *testing.T) {
	codexSignedIn(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, ".codex", "auth.json")
	auth, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	var asked atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","display_name":"Luna","visibility":"list","priority":1}]}`)
	}))
	defer up.Close()
	wasBase := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = wasBase }()
	wasEvery := provider.NewListedEvery
	provider.NewListedEvery = 20 * time.Millisecond
	defer func() { provider.NewListedEvery = wasEvery }()
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	s := New()
	stop, done := listening(t, s)
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if provider.FetchNew(time.Second) {
		t.Fatal("a list is missing with no signed-in account")
	}
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		p, err := provider.Find("codex")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.Listed(); ok {
			for _, m := range p.Available() {
				if m.ID == "gpt-6-luna" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway never fetched the new account's models: asked %d times", asked.Load())
		}
	}
}
