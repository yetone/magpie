package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's model list goes in the order dragged on the Agents page (#855):
// the list says it can be ordered, an order comes back in it and stays in
// the settings, the hidden kept as they were; none puts magpie's back.
// Another agent's list takes an order of its own (#1052).
func TestAgentModelOrderAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetHiddenModels("codex", []string{"relay/m2"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentModelsAPI(mux)
	type reply struct {
		Models  []agentModelJSON
		Ordered bool
	}
	call := func(method, agent, body string) (int, reply) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/agent-models/"+agent, strings.NewReader(body)))
		var out reply
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	ids := func(ms []agentModelJSON) (out []string) {
		for _, m := range ms {
			if strings.HasPrefix(m.ID, "relay/") {
				out = append(out, m.ID)
			}
		}
		return out
	}
	_, got := call("GET", "codex", "")
	if got.Ordered {
		t.Fatalf("codex: ordered %v", got.Ordered)
	}
	if want := []string{"relay/m1", "relay/m2", "relay/m3"}; !slices.Equal(ids(got.Models), want) {
		t.Fatalf("%v, want %v", ids(got.Models), want)
	}
	code, got := call("POST", "codex", `{"order":["relay/m3","relay/m1"]}`)
	if code != 200 || !got.Ordered {
		t.Fatalf("%d %+v", code, got)
	}
	if want := []string{"relay/m3", "relay/m1", "relay/m2"}; !slices.Equal(ids(got.Models), want) {
		t.Errorf("ordered %v, want %v", ids(got.Models), want)
	}
	if !slices.Equal(provider.ModelOrder("codex"), []string{"relay/m3", "relay/m1"}) {
		t.Errorf("saved %v", provider.ModelOrder("codex"))
	}
	if !provider.HiddenModels("codex")["relay/m2"] {
		t.Error("ordering showed the hidden m2")
	}
	if _, got = call("GET", "codex", ""); !got.Ordered || ids(got.Models)[0] != "relay/m3" {
		t.Errorf("read back %+v", got)
	}
	// a model added later goes after those ordered
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3", "m4"}}); err != nil {
		t.Fatal(err)
	}
	if _, got = call("GET", "codex", ""); !slices.Equal(ids(got.Models), []string{"relay/m3", "relay/m1", "relay/m2", "relay/m4"}) {
		t.Errorf("with a new model %v", ids(got.Models))
	}
	// hiding keeps the order
	call("POST", "codex", `{"hidden":["relay/m1"]}`)
	if !slices.Equal(provider.ModelOrder("codex"), []string{"relay/m3", "relay/m1"}) {
		t.Errorf("hiding lost the order: %v", provider.ModelOrder("codex"))
	}
	// none puts magpie's back
	if code, got = call("POST", "codex", `{"order":[]}`); code != 200 || got.Ordered || ids(got.Models)[0] != "relay/m1" {
		t.Errorf("%d %+v", code, got)
	}
	if len(provider.ModelOrder("codex")) != 0 {
		t.Errorf("still saved %v", provider.ModelOrder("codex"))
	}
	// every agent's list takes an order of its own (#1052), and Codex's
	// stays as it was
	if code, got = call("POST", "opencode", `{"order":["relay/m3"]}`); code != 200 || !got.Ordered {
		t.Fatalf("opencode: %d %+v", code, got)
	}
	if want := []string{"relay/m3", "relay/m1", "relay/m2", "relay/m4"}; !slices.Equal(ids(got.Models), want) {
		t.Errorf("opencode ordered %v, want %v", ids(got.Models), want)
	}
	if _, got = call("GET", "opencode", ""); !got.Ordered || ids(got.Models)[0] != "relay/m3" {
		t.Errorf("opencode read back %+v", got)
	}
	if len(provider.ModelOrder("codex")) != 0 {
		t.Errorf("opencode's order went to codex: %v", provider.ModelOrder("codex"))
	}
}
