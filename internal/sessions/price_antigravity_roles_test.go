package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// The Sessions list and its overview price an Antigravity local session's
// Orchestrator role ids (guyng35955 on X) as the Gemini model each is.
func TestPriceOfAntigravityOrchestratorRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"google":{"id":"google","models":{
	    "gemini-4-argon":{"id":"gemini-4-argon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2.5,"output":12,"cache_read":0.25}},
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for m, in := range map[string]float64{
		"gemini-pro-orchestrator-argon": 2, "gemini-pro-orchestrator-carbon": 2.5, "gemini-flash-worker-agy": 1.5,
		"gemini-pro-orchestrator-g1-pro": 2,
	} {
		if pr, ok := priceOf(settings.Load(), m); !ok || pr.Input != in {
			t.Errorf("priceOf(%q) = %+v, %v; want input $%v", m, pr, ok, in)
		}
	}
	if pr, ok := priceOf(settings.Load(), "gemini-pro-orchestrator-neon"); ok {
		t.Errorf("priceOf(gemini-pro-orchestrator-neon) = %+v; want unpriced", pr)
	}
}
