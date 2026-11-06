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

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// DeepSeek Harness's own DeepSeek models, beside magpie's in its picker,
// can be left out (MizuIro-H, #1642): the agent's model list has a switch
// for them, its pickers then offer magpie's alone, the model it is set to
// stays, and another agent's pickers are as they were.
func TestAgentOwnModelsHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".dsh"), 0o755)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentModelsAPI(mux)
	call := func(method, body string) (out struct {
		Own, OwnHidden bool
	}) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/agent-models/dsh", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %d %s", method, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	dsh, err := agent.Find("dsh")
	if err != nil {
		t.Fatal(err)
	}
	values := func(a *agent.Agent, vals map[string]string) (own, magpie []string) {
		for _, f := range agentFields(a, vals) {
			if f.Key != "model" {
				continue
			}
			for _, o := range f.Options {
				if o.Ref != "" {
					magpie = append(magpie, o.Value)
				} else {
					own = append(own, o.Value)
				}
			}
		}
		return own, magpie
	}
	all := []string{"deepseek-flash", "deepseek-v4-pro"}

	if got := call("GET", ""); !got.Own || got.OwnHidden {
		t.Fatalf("dsh's list: %+v, want its own models there and shown", got)
	}
	if own, magpie := values(dsh, map[string]string{}); !slices.Equal(own, all) || len(magpie) == 0 {
		t.Fatalf("before: own %v, magpie's %v", own, magpie)
	}

	if got := call("POST", `{"ownHidden":true}`); !got.OwnHidden {
		t.Fatalf("answered %+v", got)
	}
	if s := settings.Load().OwnModelsHidden; !slices.Equal(s, []string{"dsh"}) {
		t.Fatalf("saved %v", s)
	}
	if got := call("GET", ""); !got.Own || !got.OwnHidden {
		t.Fatalf("dsh's list after: %+v", got)
	}
	own, magpie := values(dsh, map[string]string{})
	if len(own) != 0 || !slices.Contains(magpie, "magpie/relay/m1") {
		t.Fatalf("hidden: own %v left, magpie's %v", own, magpie)
	}
	// the one it is set to stays, the others go
	if own, _ := values(dsh, map[string]string{"model": "deepseek-v4-pro"}); !slices.Equal(own, []string{"deepseek-v4-pro"}) {
		t.Fatalf("set to deepseek-v4-pro: own %v", own)
	}
	// another agent's pickers are as they were
	if provider.OwnModelsHidden("codex") {
		t.Fatal("codex hides its own too")
	}
	// the effort field, not a picker of magpie's models, keeps every level
	for _, f := range agentFields(dsh, map[string]string{}) {
		if f.Key == "effort" && len(f.Options) == 0 {
			t.Fatal("effort lost its levels")
		}
	}

	if got := call("POST", `{"ownHidden":false}`); got.OwnHidden {
		t.Fatalf("answered %+v", got)
	}
	if own, _ := values(dsh, map[string]string{}); !slices.Equal(own, all) {
		t.Fatalf("shown again: own %v", own)
	}
	if s := settings.Load().OwnModelsHidden; len(s) != 0 {
		t.Fatalf("saved %v", s)
	}
}
