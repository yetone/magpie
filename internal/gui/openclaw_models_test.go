package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The UI saves a complete chain, and state reads back exactly that order.
func TestOpenClawModelChainAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	dir := filepath.Join(home, ".openclaw")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "openclaw.json"), []byte(`{"agents":{"defaults":{"model":"mine/original"}}}`), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	payload := `{"primary":"magpie/group/auto","fallbacks":["magpie/group/fast","anthropic/claude-opus-5"]}`
	for _, method := range []string{"POST", "GET"} {
		url := "/api/state"
		if method == "POST" {
			url = "/api/agents/model-chain/openclaw"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, url, strings.NewReader(payload)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var s stateJSON
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range s.Agents {
			if a.ID == "openclaw" {
				found = true
				b, _ := json.Marshal(a.ModelChain)
				if string(b) != payload {
					t.Fatalf("chain %s", b)
				}
				if !a.Wired {
					t.Fatal("not wired")
				}
			}
		}
		if !found {
			t.Fatal("OpenClaw missing")
		}
	}
}
