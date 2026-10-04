package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// piNativeHome is a home with Pi signed in to both openai and openai-codex
// (test metadata only), and models.dev knowing openai alone.
func piNativeHome(t *testing.T) (home, dir string) {
	t.Helper()
	home = syncHome(t)
	dir = filepath.Join(home, "pi-agent")
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{
	  "openai": {"type": "oauth", "access": "test-access", "refresh": "test-refresh", "expires": 1},
	  "openai-codex": {"type": "oauth", "access": "test-access", "refresh": "test-refresh", "expires": 1, "accountId": "test-account"}
	}`), 0o600)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"name":"OpenAI","models":{
	  "gpt-6-astra":{"id":"gpt-6-astra","name":"GPT-6 Astra"},
	  "gpt-4.1":{"id":"gpt-4.1","name":"GPT-4.1"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return home, dir
}

// fakePiInstall lays out Pi 1.0's package as npm installs it globally, its
// openai-codex registry in pi-ai's data folder, and returns the command
// npm links onto PATH.
func fakePiInstall(t *testing.T, root string) string {
	t.Helper()
	pkg := filepath.Join(root, "lib", "node_modules", "@earendil-works", "pi-coding-agent")
	cli := filepath.Join(pkg, "dist", "bundle", "cli.js")
	writeFile(t, cli, "#!/usr/bin/env node\n")
	writeFile(t, filepath.Join(pkg, "node_modules", "@earendil-works", "pi-ai", "dist", "providers", "data", "openai-codex.json"), `{
	  "openai-codex-responses": {
	    "chat:gpt-6-astra": {"id":"gpt-6-astra","name":"GPT-6 Astra","provider":"openai-codex","input":["text","image"],"contextWindow":272000,"maxTokens":128000,"type":"chat"},
	    "chat:gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","provider":"openai-codex","input":["text"],"type":"chat"},
	    "image:gpt-image-2": {"id":"gpt-image-2","name":"GPT Image 2","provider":"openai-codex","type":"image"}
	  }
	}`)
	bin := filepath.Join(root, "bin", "pi")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.Symlink(cli, bin); err != nil {
		t.Skip("symlinks:", err)
	}
	os.Chmod(cli, 0o755)
	return bin
}

type pickRow struct{ value, note, group string }

func pickRows(opts []Option, prefix string) []pickRow {
	var out []pickRow
	for _, o := range opts {
		if len(o.Value) > len(prefix) && o.Value[:len(prefix)] == prefix {
			out = append(out, pickRow{o.Value, o.Note, o.Group})
		}
	}
	return out
}

// #709: Pi signed in to openai-codex ("OpenAI Codex (legacy)", the ChatGPT
// backend) was offered none of its models: they came from models.dev, which
// has no openai-codex. They now come from Pi's own registry, in the package
// Pi was installed from, in its order and without its image models, as
// openai-codex/<model> under their own provider; openai/<model> of the same
// name stays a row of its own, and picking one writes Pi's default pair.
func TestPiOffersItsOwnOpenAICodexModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npm links the command on a Mac or Linux")
	}
	home, dir := piNativeHome(t)
	bin := fakePiInstall(t, filepath.Join(home, "npm"))
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	nodeModulesOf = findNodeModules
	t.Cleanup(func() { nodeModulesOf = func(string, []string) []string { return nil } })

	a := pi(home)
	opts := a.Field("model").Options(map[string]string{})
	codex := pickRows(opts, "openai-codex/")
	want := []pickRow{{"openai-codex/gpt-6-astra", "GPT-6 Astra", "OpenAI Codex"}, {"openai-codex/gpt-5.5", "GPT-5.5", "OpenAI Codex"}}
	if !slices.Equal(codex, want) {
		t.Fatalf("openai-codex rows: %v", codex)
	}
	if !slices.Contains(pickRows(opts, "openai/"), pickRow{"openai/gpt-6-astra", "GPT-6 Astra", "OpenAI"}) {
		t.Fatalf("openai rows: %v", pickRows(opts, "openai/"))
	}
	for _, o := range opts {
		if o.Value == "openai-codex/gpt-6-astra" && o.GroupIcon == "" {
			t.Fatalf("no provider logo: %+v", o)
		}
	}

	if err := a.Field("model").Set("openai-codex/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.json")
	p, _ := edit.GetJSON(settings, "defaultProvider")
	m, _ := edit.GetJSON(settings, "defaultModel")
	if p != "openai-codex" || m != "gpt-6-astra" {
		t.Fatalf("default: %q %q", p, m)
	}
}

// Without Pi's registry in reach (Pi installed where magpie doesn't look),
// openai-codex still lists what the ChatGPT backend serves a Codex client,
// from Codex CLI's models cache.
func TestPiOpenAICodexWithoutItsRegistry(t *testing.T) {
	home, _ := piNativeHome(t)
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	writeFile(t, filepath.Join(codexHome, "models_cache.json"), `{"models":[
	  {"slug":"gpt-6-astra","display_name":"GPT-6 Astra","priority":1},
	  {"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide","priority":2}]}`)
	got := pickRows(pi(home).Field("model").Options(map[string]string{}), "openai-codex/")
	if !slices.Equal(got, []pickRow{{"openai-codex/gpt-6-astra", "GPT-6 Astra", "OpenAI Codex"}}) {
		t.Fatalf("openai-codex rows: %v", got)
	}
}

// omp shares the gap for the provider of its current model: its own
// catalog (@oh-my-pi/pi-catalog) lists openai-codex's models, without the
// ones that aren't chat models.
func TestOmpOffersItsOwnOpenAICodexModels(t *testing.T) {
	home := syncHome(t)
	modules := filepath.Join(home, ".bun", "install", "global", "node_modules")
	writeFile(t, filepath.Join(modules, "@oh-my-pi", "pi-catalog", "src", "models.json"), `{
	  "openai": {"gpt-6-astra": {"id":"gpt-6-astra","name":"GPT-6 Astra"}},
	  "openai-codex": {
	    "gpt-6-astra": {"id":"gpt-6-astra","name":"GPT-6 Astra"},
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5"},
	    "gpt-image-2": {"id":"gpt-image-2","name":"gpt-image-2","kind":"image"}
	  }
	}`)
	nodeModulesOf = func(bin string, pkgs []string) []string {
		if bin == "omp" && slices.Contains(pkgs, "@oh-my-pi/pi-coding-agent") {
			return []string{filepath.Join(modules, "@oh-my-pi", "pi-coding-agent", "node_modules"), modules}
		}
		return nil
	}
	t.Cleanup(func() { nodeModulesOf = func(string, []string) []string { return nil } })
	got := pickRows(ompOwnOptions(filepath.Join(home, "none.yml"), "openai-codex/gpt-6-astra"), "openai-codex/")
	want := []pickRow{{"openai-codex/gpt-6-astra", "GPT-6 Astra", "OpenAI Codex"}, {"openai-codex/gpt-5.5", "GPT-5.5", "OpenAI Codex"}}
	if !slices.Equal(got, want) {
		t.Fatalf("openai-codex rows: %v", got)
	}
}
