package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// AcMilan on Discord: beside the ChatGPT sign-in, Codex 0.166's app
// refused to reopen a chat ("This chat uses cc-switch-official, but your
// current provider is openai. Switch to cc-switch-official to
// continue"). CC Switch 3.17 to 3.20 sent the official account through
// its own proxy under that id; CC Switch 4 moves those threads back to
// OpenAI itself. magpie's sync moves them to the provider Codex is on,
// and leaves config.toml's table as it is; none moves while a profile
// picks that id, and a thread of a provider of the user's stays.
func TestCodexMovesCCSwitchOfficialThreads(t *testing.T) {
	const (
		official = "019a0000-0000-7000-8000-0000000000f1"
		theirs   = "019a0000-0000-7000-8000-0000000000f2"
	)
	table := "\n[model_providers.cc-switch-official]\nname = \"OpenAI\"\nbase_url = \"http://127.0.0.1:15721/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n"
	signedIn := `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`
	t.Run("beside the sign-in", func(t *testing.T) {
		home, read := codexHome(t, signedIn, "model = \"gpt-5.4\"\n"+table)
		dir := filepath.Join(home, ".codex")
		path := codexThreadOn(t, dir, official, "cc-switch-official")
		codexThreadOn(t, dir, theirs, "custom")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		cfg := filepath.Join(dir, "config.toml")
		if p, _ := edit.GetTOMLTop(cfg, "model_provider"); p != "" {
			t.Fatalf("provider %q, want Codex's own beside the sign-in:\n%s", p, read())
		}
		for id, want := range map[string]string{official: "openai", theirs: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"model_provider":"openai"`) {
			t.Fatalf("rollout:\n%s", b)
		}
		if tb, err := edit.GetTOMLTable(cfg, "model_providers.cc-switch-official"); err != nil || tb["name"] != "OpenAI" {
			t.Fatalf("CC Switch's table changed: %v %v\n%s", tb, err, read())
		}
	})
	t.Run("a profile picks it", func(t *testing.T) {
		home, _ := codexHome(t, signedIn, "model = \"gpt-5.4\"\n"+table+"\n[profiles.ccs]\nmodel_provider = \"cc-switch-official\"\n")
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, official, "cc-switch-official")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		cx.Sync()
		if p := codexRowOn(t, dir, official); p != "cc-switch-official" {
			t.Fatalf("moved while a profile picks it: %q", p)
		}
	})
	t.Run("not connected", func(t *testing.T) {
		home, _ := codexHome(t, signedIn, "model = \"gpt-5.4\"\n"+table)
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, official, "cc-switch-official")
		codex(home).Sync()
		if p := codexRowOn(t, dir, official); p != "cc-switch-official" {
			t.Fatalf("moved with Codex not on magpie: %q", p)
		}
	})
}
