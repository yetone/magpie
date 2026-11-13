package agent

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// xiaomanweiman on X: Pi's row picks the protocol magpie's models are
// asked on, in place of typing providers.magpie.api into models.json.
// Unpicked it is as before (Chat for the provider, each model on its
// native API); a pick puts every model on it; an api typed by hand still
// shows and wins until a pick is made after it.
func TestPiProtocolPicked(t *testing.T) {
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "oa", Name: "OA", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "an", Name: "An", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5"}}); err != nil {
		t.Fatal(err)
	}
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, filepath.Join(home, ".pi", "agent", "settings.json"), `{"defaultProvider":"magpie","defaultModel":"relay/glm-4.6"}`)
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","api":"openai-completions","models":[]}}}`)
	setUpInMagpie("pi")
	SyncCatalog()

	pi, err := Find("pi")
	if err != nil {
		t.Fatal(err)
	}
	var f *Field
	for i := range pi.Fields {
		if pi.Fields[i].Key == "protocol" {
			f = &pi.Fields[i]
		}
	}
	if f == nil {
		t.Fatal("Pi has no protocol field")
	}
	get := func(k string) string {
		v, _ := edit.GetJSON(piModels, "providers.magpie."+k)
		return strings.Trim(v, `"`)
	}
	model := func(id string) (api, base string) {
		for i := 0; ; i++ {
			got := get("models." + strconv.Itoa(i) + ".id")
			if got == "" {
				t.Fatalf("no model %s\n%s", id, readFile(piModels))
			}
			if got == id {
				return get("models." + strconv.Itoa(i) + ".api"), get("models." + strconv.Itoa(i) + ".baseUrl")
			}
		}
	}
	gw := strings.TrimSuffix(get("baseUrl"), "/v1")

	// unpicked: as before
	if v := f.Get(); v != "" {
		t.Fatalf("unpicked protocol = %q, want each model's own", v)
	}
	if got := get("api"); got != "openai-completions" {
		t.Fatalf("provider api = %s, want openai-completions", got)
	}
	if a, _ := model("oa/gpt-6.1-sol"); a != "openai-responses" {
		t.Fatalf("a Responses model unpicked is on %q\n%s", a, readFile(piModels))
	}
	opts := f.Options(nil)
	var vals []string
	for _, o := range opts {
		vals = append(vals, o.Value)
	}
	if strings.Join(vals, ",") != ",openai-completions,openai-responses,anthropic-messages" {
		t.Fatalf("options = %v", vals)
	}

	// Responses picked: the provider's, every model on it
	if err := f.Set("openai-responses"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got := get("api"); got != "openai-responses" {
		t.Fatalf("provider api = %s, want openai-responses\n%s", got, readFile(piModels))
	}
	for _, id := range []string{"relay/glm-4.6", "oa/gpt-6.1-sol", "an/claude-opus-5"} {
		if a, b := model(id); a != "" || b != "" {
			t.Fatalf("%s on Responses has api %q at %q of its own\n%s", id, a, b, readFile(piModels))
		}
	}
	if v := f.Get(); v != "openai-responses" {
		t.Fatalf("protocol = %q", v)
	}

	// Messages: each model at the gateway's root
	if err := f.Set("anthropic-messages"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"relay/glm-4.6", "oa/gpt-6.1-sol", "an/claude-opus-5"} {
		if a, b := model(id); a != "anthropic-messages" || b != gw {
			t.Fatalf("%s on Messages = %q at %q, want anthropic-messages at %s\n%s", id, a, b, gw, readFile(piModels))
		}
	}
	if got := get("baseUrl"); got != gw+"/v1" {
		t.Fatalf("provider baseUrl = %s, want %s/v1", got, gw)
	}

	// Chat: every model on Chat, the Responses one too
	if err := f.Set("openai-completions"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if a, b := model("oa/gpt-6.1-sol"); a != "" || b != "" {
		t.Fatalf("a Responses model on Chat has %q at %q\n%s", a, b, readFile(piModels))
	}
	if v := f.Get(); v != "openai-completions" {
		t.Fatalf("protocol = %q", v)
	}

	// back to each model's own
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if a, _ := model("oa/gpt-6.1-sol"); a != "openai-responses" || get("api") != "openai-completions" {
		t.Fatalf("each model's own: %q, provider %s\n%s", a, get("api"), readFile(piModels))
	}

	// typed by hand: it shows, and stays
	if err := edit.SetJSON(piModels, edit.KV{Path: "providers.magpie.api", Value: "openai-responses"}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if v := f.Get(); v != "openai-responses" || get("api") != "openai-responses" {
		t.Fatalf("an api typed by hand shows %q, file %s", v, get("api"))
	}
	// a pick after it is the newer word
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if v := f.Get(); v != "" || get("api") != "openai-completions" {
		t.Fatalf("picked after a hand value: shows %q, file %s", v, get("api"))
	}

	if err := f.Set("google-generative-ai"); err == nil {
		t.Fatal("a protocol the gateway doesn't serve to Pi was taken")
	}
}
