package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// providerAt is the object at providers.<id> (or provider.<id>) of a JSON
// file, decoded.
func providerAt(t *testing.T, path, top string) map[string]any {
	t.Helper()
	var f map[string]json.RawMessage
	var ps map[string]map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil || json.Unmarshal(f[top], &ps) != nil {
		t.Fatalf("%s: %v\n%s", path, err, readFile(path))
	}
	return ps[magpieID]
}

// #1103: pi-cache-optimizer's /cache-optimizer fix adds
// providers.magpie.compat.sendSessionAffinityHeaders to Pi's models.json.
// A catalog sync, and a pick of a magpie model in Pi, keep it; the model
// list is still magpie's, so a model gone from the catalog leaves it, and
// a sync with nothing new writes nothing.
func TestPiKeepsTheirKeysInMagpiesProvider(t *testing.T) {
	home := syncHome(t)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	// the block as magpie wrote it, with the optimizer's key added
	writeFile(t, piModels, `{
  "providers": {
    "magpie": {
      "api": "openai-completions",
      "apiKey": "magpie",
      "baseUrl": "http://127.0.0.1:3425/v1",
      "compat": {
        "sendSessionAffinityHeaders": true
      },
      "models": [
        {
          "id": "gone/old-model",
          "name": "Old"
        }
      ],
      "name": "magpie"
    }
  }
}
`)
	SyncCatalog()
	p := providerAt(t, piModels, "providers")
	compat, _ := p["compat"].(map[string]any)
	if compat["sendSessionAffinityHeaders"] != true {
		t.Fatalf("compat.sendSessionAffinityHeaders dropped by the sync:\n%s", readFile(piModels))
	}
	ms, _ := json.Marshal(p["models"])
	if strings.Contains(string(ms), "gone/old-model") || !strings.Contains(string(ms), "relay/glm-4.6") {
		t.Fatalf("models not magpie's list: %s", ms)
	}

	// synced already: the next sync writes nothing
	st, _ := os.Stat(piModels)
	old := st.ModTime().Add(-1e12)
	os.Chtimes(piModels, old, old)
	SyncCatalog()
	if now, _ := os.Stat(piModels); !now.ModTime().Equal(old) {
		t.Errorf("models.json rewritten though nothing changed:\n%s", readFile(piModels))
	}

	// picking a magpie model in Pi writes the block too
	if err := pi(home).Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	compat, _ = providerAt(t, piModels, "providers")["compat"].(map[string]any)
	if compat["sendSessionAffinityHeaders"] != true {
		t.Fatalf("compat.sendSessionAffinityHeaders dropped by a pick:\n%s", readFile(piModels))
	}
}

// The same for OpenCode's provider.magpie (an options key of the user's
// own) and Crush's providers.magpie.
func TestOpenCodeAndCrushKeepTheirKeysInMagpiesProvider(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","provider":{"magpie":{"name":"magpie","npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie","setCacheKey":true},"models":{"gone/old-model":{"name":"Old"}}}}}`)
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	p := providerAt(t, cfg, "provider")
	opts, _ := p["options"].(map[string]any)
	ms, _ := p["models"].(map[string]any)
	if opts["setCacheKey"] != true || ms["gone/old-model"] != nil || ms["relay/glm-4.6"] == nil {
		t.Fatalf("opencode sync:\n%s", readFile(cfg))
	}
	if err := oc.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if opts, _ := providerAt(t, cfg, "provider")["options"].(map[string]any); opts["setCacheKey"] != true {
		t.Fatalf("opencode pick:\n%s", readFile(cfg))
	}

	cr := filepath.Join(home, ".config", "crush", "crush.json")
	writeFile(t, cr, `{"providers":{"magpie":{"type":"openai","name":"magpie","base_url":"http://127.0.0.1:3425/v1","api_key":"magpie","extra_headers":{"X-Mine":"1"},"models":[]}}}`)
	c := crushAt(here(home), cr, filepath.Join(home, ".local", "share", "crush", "crush.json"))
	if err := c.Sync(); err != nil {
		t.Fatal(err)
	}
	if h, _ := providerAt(t, cr, "providers")["extra_headers"].(map[string]any); h["X-Mine"] != "1" {
		t.Fatalf("crush sync:\n%s", readFile(cr))
	}
	if err := c.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if h, _ := providerAt(t, cr, "providers")["extra_headers"].(map[string]any); h["X-Mine"] != "1" {
		t.Fatalf("crush pick:\n%s", readFile(cr))
	}
}
