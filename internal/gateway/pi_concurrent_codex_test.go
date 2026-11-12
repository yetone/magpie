package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Two Pi turns at once on one Codex (ChatGPT) sign-in are both out at the
// vendor at once: the second is not held in "routing" until the first has
// been answered (tmr11235 on X, v0.1.3852).
func TestPiTwoTurnsOnCodexAreOutTogether(t *testing.T) {
	for _, socket := range []bool{false, true} {
		t.Run(fmt.Sprint("socket=", socket), func(t *testing.T) { piTwoTurns(t, socket) })
	}
}

func piTwoTurns(t *testing.T, socket bool) {
	if socket {
		socketOn(t)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)

	var out, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	hold := func() {
		n := out.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			once.Do(func() { close(release) })
		}
		select { // a slow answer: held until the other one is out too
		case <-release:
		case <-time.After(5 * time.Second):
		}
		out.Add(-1)
	}
	var h0 http.Handler
	if socket {
		f := &fakeSocketBackend{keep: false, frames: func(socketTurn) []string {
			hold()
			return wholeReply(socketTurn{})
		}}
		h0 = f
	} else {
		h0 = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header()["Content-Type"] = nil
			io.WriteString(w, strings.SplitAfter(piReply, "\n\n")[0])
			w.(http.Flusher).Flush()
			hold() // mid-reply: the first has begun answering
			io.WriteString(w, strings.TrimPrefix(piReply, strings.SplitAfter(piReply, "\n\n")[0]))
		})
	}
	up := httptest.NewServer(h0)
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	h := New().Handler()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(piResponses, "codex/gpt-6.1-sol")))
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("reply: %d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	if peak.Load() < 2 {
		t.Fatalf("at most %d turn out at the vendor at once: the second waited for the first", peak.Load())
	}
}
