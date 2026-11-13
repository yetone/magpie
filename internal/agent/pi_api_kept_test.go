package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// xiaomanweiman on X: providers.magpie.api in Pi's models.json was always
// openai-completions, and openai-responses set there by hand (T3 Code's Pi
// keeps 3 tools in context on Responses, all 71 on Chat) was written back
// by the next sync. An api set by hand for the provider or one model stays,
// with the model's baseUrl and compat; one magpie wrote follows magpie.
func TestPiKeepsAnAPISetByHand(t *testing.T) {
	home := syncHome(t)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, filepath.Join(home, ".pi", "agent", "settings.json"), `{"defaultProvider":"magpie","defaultModel":"relay/glm-4.6"}`)
	// set by hand before this magpie kept any record
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","api":"openai-responses","models":[]}}}`)
	setUpInMagpie("pi")
	get := func(k string) string { v, _ := edit.GetJSON(piModels, "providers.magpie."+k); return v }
	SyncCatalog()
	if got := get("api"); got != "openai-responses" {
		t.Fatalf("provider api = %s, want the user's openai-responses\n%s", got, readFile(piModels))
	}
	// a Chat model left on it is asked on Responses, strict said
	if got := get("models.0.compat.supportsStrictMode"); got != "true" {
		t.Fatalf("a model on the user's Responses has compat.supportsStrictMode %q, want true\n%s", got, readFile(piModels))
	}
	if _, has := edit.GetJSON(piModels, "providers.magpie.models.0.api"); has {
		t.Fatalf("a Chat model got an api of its own\n%s", readFile(piModels))
	}

	// put back by hand: magpie's again, and its compat goes with it
	if err := edit.SetJSON(piModels, edit.KV{Path: "providers.magpie.api", Value: "openai-completions"}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got := get("api"); got != "openai-completions" {
		t.Fatalf("provider api = %s, want openai-completions", got)
	}
	if _, has := edit.GetJSON(piModels, "providers.magpie.models.0.compat.supportsStrictMode"); has {
		t.Fatalf("supportsStrictMode stayed on Chat\n%s", readFile(piModels))
	}

	// one model's, with its baseUrl, set by hand: stays through syncs
	if err := edit.SetJSON(piModels, edit.KV{Path: "providers.magpie.models.0.api", Value: "anthropic-messages"},
		edit.KV{Path: "providers.magpie.models.0.baseUrl", Value: "http://127.0.0.1:9/"}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	SyncCatalog()
	if got, url := get("models.0.api"), get("models.0.baseUrl"); got != "anthropic-messages" || url != "http://127.0.0.1:9/" {
		t.Fatalf("a model's api set by hand = %s at %s, want anthropic-messages at the user's baseUrl\n%s", got, url, readFile(piModels))
	}
	// and taken away by hand: magpie's (none) again
	if err := edit.DelJSON(piModels, "providers.magpie.models.0.api", "providers.magpie.models.0.baseUrl"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if _, has := edit.GetJSON(piModels, "providers.magpie.models.0.api"); has {
		t.Fatalf("a model's api came back\n%s", readFile(piModels))
	}
}
