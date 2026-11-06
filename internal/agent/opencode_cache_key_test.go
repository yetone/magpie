package agent

import (
	"path/filepath"
	"testing"
)

// #5468: magpie's provider in OpenCode's config sets setCacheKey, or
// OpenCode sends no prompt_cache_key and an upstream that caches by it
// misses every turn. A real OpenCode 2.2.5 sent prompt_cache_key with it
// and none without; 1.19.5 sent promptCacheKey (the gateway's
// cacheKeyCamel); 2.0.25 ignored it. Under providers, in OpenCode 2's
// words, it is compatibility.supportsPromptCacheKey, no setting. A value
// the user set themselves, false too, stays, through a sync and a pick.
func TestOpenCodeSetsCacheKey(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	opts, _ := providerAt(t, cfg, "provider")["options"].(map[string]any)
	if opts["setCacheKey"] != true {
		t.Fatalf("no setCacheKey:\n%s", readFile(cfg))
	}

	// the user's own false, under provider and under providers
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","provider":{"magpie":{"name":"magpie","npm":"@ai-sdk/openai-compatible",`+
		`"options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie","setCacheKey":false},"models":{}}},`+
		`"providers":{"magpie":{"name":"magpie","package":"aisdk:@ai-sdk/openai-compatible","compatibility":{"supportsPromptCacheKey":false},`+
		`"settings":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie"},"models":{}}}}`)
	for _, step := range []func() error{oc.Sync, func() error { return oc.Fields[0].Set("magpie/relay/glm-4.6") }} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
		opts, _ := providerAt(t, cfg, "provider")["options"].(map[string]any)
		native := providerAt(t, cfg, "providers")
		compat, _ := native["compatibility"].(map[string]any)
		settings, _ := native["settings"].(map[string]any)
		ms, _ := native["models"].(map[string]any)
		if opts["setCacheKey"] != false || compat["supportsPromptCacheKey"] != false || ms["relay/glm-4.6"] == nil {
			t.Fatalf("the user's false not kept:\n%s", readFile(cfg))
		}
		if _, in := settings["setCacheKey"]; in {
			t.Fatalf("setCacheKey in OpenCode 2's settings:\n%s", readFile(cfg))
		}
	}

	// under providers with no word of the user's: magpie's
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","providers":{"magpie":{"name":"magpie","package":"aisdk:@ai-sdk/openai-compatible",`+
		`"settings":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie"},"models":{}}}}`)
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	native := providerAt(t, cfg, "providers")
	compat, _ := native["compatibility"].(map[string]any)
	settings, _ := native["settings"].(map[string]any)
	if compat["supportsPromptCacheKey"] != true || settings["setCacheKey"] != nil || settings["baseURL"] == nil {
		t.Fatalf("providers.magpie:\n%s", readFile(cfg))
	}
}
