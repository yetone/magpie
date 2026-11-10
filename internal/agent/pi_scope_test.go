package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestPiTakes(t *testing.T) {
	for _, c := range []struct {
		pattern, ref string
		want         bool
	}{
		{"xai/grok-4.6", "xai/grok-4.6", true},
		{"XAI/Grok-4.6", "xai/grok-4.6", true},
		{"xai/grok-4.6:high", "xai/grok-4.6", true},
		{"xai/*", "xai/grok-4.6", true},
		{"*grok*", "xai/grok-4.6", true},
		{"magpie/*", "magpie/deepseek/deepseek-v4", false}, // Pi's * stops at a /
		{"magpie/*/*", "magpie/deepseek/deepseek-v4", true},
		{"deepseek/*:low", "magpie/deepseek/deepseek-v4", true}, // the model id fits
		{"grok-4.6", "xai/grok-4.6", false},                     // a bare id: added beside
		{"xai/grok-4.6", "xai/grok-4.7", false},
		{"openai-codex/gpt-5.6-sol", "opencode-go/deepseek-v4-flash", false},
	} {
		if got := piTakes(c.pattern, c.ref); got != c.want {
			t.Errorf("piTakes(%q, %q) = %v", c.pattern, c.ref, got)
		}
	}
}

// Pi starts a new session on defaultModel only when enabledModels takes it
// in: the model magpie picks is added to the list, the user's entries
// kept; there is no list made where there was none, and none left empty
// (#160)
func TestPiModelJoinsEnabledModels(t *testing.T) {
	home := syncHome(t)
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{
  // the user's own
  "enabledModels": ["xai/grok-4.6:high", "openai-codex/gpt-5.6-sol", "deepseek/*"],
  "defaultProvider": "xai",
  "defaultModel": "grok-4.6"
}
`), 0o644)
	f := pi(home).Field("model")
	if got := f.Get(); got != "xai/grok-4.6" {
		t.Fatalf("Get = %q", got)
	}
	if err := f.Set("opencode-go/deepseek-v4-flash-vision-exp"); err != nil {
		t.Fatal(err)
	}
	want := `["xai/grok-4.6:high","openai-codex/gpt-5.6-sol","deepseek/*","opencode-go/deepseek-v4-flash-vision-exp"]`
	if got := strings.Join(strings.Fields(mustGet(t, settings, "enabledModels")), ""); got != want {
		t.Errorf("enabledModels = %s, want %s", got, want)
	}
	if !strings.Contains(readFile(settings), "// the user's own") {
		t.Errorf("comment lost: %s", readFile(settings))
	}
	if got := f.Get(); got != "opencode-go/deepseek-v4-flash-vision-exp" {
		t.Errorf("Get = %q", got)
	}
	// a model the list takes in already isn't added again
	before := mustGet(t, settings, "enabledModels")
	for _, v := range []string{"openai-codex/gpt-5.6-sol", "deepseek/deepseek-v4-pro", "xai/grok-4.6"} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustGet(t, settings, "enabledModels"); got != before {
		t.Errorf("enabledModels grew: %s", got)
	}

	// set outside magpie: the model shown is the one Pi starts on
	edit.SetJSON(settings, edit.KV{Path: "defaultModel", Value: "grok-3"})
	if got := f.Get(); got != "xai/grok-4.6" {
		t.Errorf("Get with a default outside the list = %q, want the list's first", got)
	}

	// no list: none is made
	os.WriteFile(settings, []byte(`{"defaultProvider":"xai","defaultModel":"grok-4.6"}`), 0o644)
	if err := f.Set("opencode-go/deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(settings, "enabledModels"); ok {
		t.Errorf("enabledModels made: %s", readFile(settings))
	}
	// an empty one means no list to Pi, and stays as it is
	os.WriteFile(settings, []byte(`{"enabledModels":[]}`), 0o644)
	if err := f.Set("opencode-go/deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, settings, "enabledModels"); got != "[]" {
		t.Errorf("empty enabledModels = %s", got)
	}
}

// Turning Pi's model back to its own takes magpie's models out of the list
// with magpie's provider; a list left with nothing goes, not left empty
func TestPiModelOffLeavesEnabledModels(t *testing.T) {
	home := syncHome(t)
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	f := pi(home).Field("model")

	os.WriteFile(settings, []byte(`{"enabledModels":["xai/grok-4.6","magpie/*/*"]}`), 0o644)
	if err := f.Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("magpie/deepseek/deepseek-v4"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	// the user's glob is theirs
	if got := strings.Join(strings.Fields(mustGet(t, settings, "enabledModels")), ""); got != `["xai/grok-4.6","magpie/*/*"]` {
		t.Errorf("enabledModels = %s", got)
	}

	os.WriteFile(settings, []byte(`{"enabledModels":["magpie/relay/glm-4.6"],"defaultProvider":"magpie","defaultModel":"relay/glm-4.6"}`), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(settings, "enabledModels"); ok {
		t.Errorf("enabledModels left: %s", readFile(settings))
	}
}

func mustGet(t *testing.T, path, key string) string {
	t.Helper()
	v, ok := edit.GetJSON(path, key)
	if !ok {
		t.Fatalf("%s: no %s in %s", path, key, readFile(path))
	}
	return v
}

// A provider renamed moves the magpie/<id>/ entries in Pi's enabledModels
// onto the new id, in Pi and in its forks that keep the same settings.json
// (OmO, Prime Agent): the model it is on, kept at its place and not listed
// twice, another of that provider's with a thinking level, and a glob. The
// user's own entries stay, and so does a name that only shares a prefix
// (#1919, #1828).
func TestPiEnabledModelsFollowRename(t *testing.T) {
	for _, c := range []struct {
		id  string
		dir []string
		a   func(string) *Agent
	}{
		{"pi", []string{".pi", "agent"}, pi},
		{"omo", []string{".omo", "agent"}, omo},
		{"prime-agent", []string{".prime", "agent"}, primeAgent},
		{"mu", []string{".mu", "agent"}, mu},
	} {
		t.Run(c.id, func(t *testing.T) {
			home := syncHome(t)
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
				t.Fatal(err)
			}
			// a provider whose id only shares a prefix; its model is offered,
			// so the sync after the rename keeps its entry (#2055)
			if err := provider.Save(provider.Provider{ID: "deepseekx", Name: "DeepSeekX", Chat: "https://deepseekx.invalid/v1", Key: "k", Models: []string{"flash"}}); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(append([]string{home}, append(c.dir, "settings.json")...)...)
			os.MkdirAll(filepath.Dir(settings), 0o755)
			os.WriteFile(settings, []byte(`{
  // the user's own
  "enabledModels": ["xai/grok-4.6:high", "magpie/deepseek/flash:low", "magpie/deepseek/*", "magpie/deepseekx/flash", "magpie/deepseek/pro"],
  "defaultProvider": "xai",
  "defaultModel": "grok-4.6"
}
`), 0o644)
			a := c.a(home)
			if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
				t.Fatal(err)
			}
			if _, err := RenameProvider("deepseek", "ds"); err != nil {
				t.Fatal(err)
			}
			want := `["xai/grok-4.6:high","magpie/ds/flash:low","magpie/ds/*","magpie/deepseekx/flash","magpie/ds/pro"]`
			if got := strings.Join(strings.Fields(mustGet(t, settings, "enabledModels")), ""); got != want {
				t.Errorf("enabledModels = %s, want %s", got, want)
			}
			if !strings.Contains(readFile(settings), "// the user's own") {
				t.Errorf("comment lost: %s", readFile(settings))
			}
			if got := a.Field("model").Get(); got != "magpie/ds/pro" {
				t.Errorf("model = %q", got)
			}

			// a list naming none of the provider's models is left byte for byte
			const mine = `{"enabledModels": ["xai/grok-4.6", "xai/grok-4.6"]}` + "\n"
			os.WriteFile(settings, []byte(mine), 0o644)
			if _, err := RenameProvider("ds", "deepseek"); err != nil {
				t.Fatal(err)
			}
			if got := readFile(settings); got != mine {
				t.Errorf("user list rewritten: %s", got)
			}
		})
	}
}
