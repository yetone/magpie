package gui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// What a failed install left in plugins/ that no plugin is installed as
// reaches the page (leftovers), and its Remove (/api/plugins/leftover)
// asks for that name: nothing showed agency-agents, so nothing could take
// it out (CHENGJIAHEGOXIS on X).
func TestPluginLeftoversReachThePage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(plugin.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin.Dir(), "package.json"), []byte(`{"dependencies":{"agency-agents":"github:msitarzewski/agency-agents"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), []byte(`{"plugins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := dropLeftover
	t.Cleanup(func() { dropLeftover = old })
	var asked []string
	dropLeftover = func(ctx context.Context, name string) error { asked = append(asked, name); return nil }

	s := pluginsState(context.Background(), nil)
	if len(s.Leftovers) != 1 || s.Leftovers[0].Name != "agency-agents" || s.Leftovers[0].Spec != "github:msitarzewski/agency-agents" {
		t.Fatalf("leftovers = %+v", s.Leftovers)
	}
	mux := http.NewServeMux()
	pluginRoutes(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/plugins/leftover", strings.NewReader(`{"spec":"agency-agents"}`)))
	if rec.Code != 200 || strings.Join(asked, ",") != "agency-agents" {
		t.Fatalf("POST leftover: %d %s, asked %v", rec.Code, rec.Body, asked)
	}
}
