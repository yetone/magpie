package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestGrok(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GROK_HOME", filepath.Join(home, "grok"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "grok", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# mine\n[ui]\ntheme = \"dark\"\n\n[model.my-own]\nmodel = \"x\"\nbase_url = \"https://x/v1\"\n\n[models]\ndefault = \"grok-4.6\"\n"), 0o644)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	table := func(name string) map[string]string {
		t.Helper()
		got, err := edit.GetTOMLTable(path, name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a := grok(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "grok-4.6" {
		t.Fatalf("get: %q", f.Get())
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	raw := read()
	m := table(`model."magpie/deepseek/pro"`)
	if m["model"] != "deepseek/pro" || m["api_key"] != "magpie" || !strings.HasSuffix(m["base_url"], "/v1") ||
		m["api_backend"] != "chat_completions" || table(`model."magpie/deepseek/flash"`) == nil {
		t.Fatalf("tables:\n%s", raw)
	}
	// a campaign of xAI's would set the default over it
	if table("features")["campaigns"] != "false" {
		t.Fatalf("campaigns:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || !strings.Contains(raw, "# mine") || !strings.Contains(raw, "[model.my-own]") ||
		table("ui")["theme"] != "dark" {
		t.Fatalf("config:\n%s", raw)
	}

	// picked again, and synced: the tables are replaced, never repeated
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if n := strings.Count(raw, `[model."magpie/deepseek/pro"]`); n != 1 {
		t.Fatalf("%d tables:\n%s", n, raw)
	}

	// a provider removed leaves Grok's list on the next sync
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"flash"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), `"magpie/deepseek/pro"]`) {
		t.Fatalf("stale:\n%s", read())
	}

	// its own model: magpie steps out, the user's own model stays
	if err := f.Set("grok-4.6"); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if f.Get() != "grok-4.6" || strings.Contains(raw, "magpie") || !strings.Contains(raw, "[model.my-own]") {
		t.Fatalf("own:\n%s", raw)
	}
	// nothing through magpie: sync leaves the file alone
	if err := a.Sync(); err != nil || read() != raw {
		t.Fatalf("sync touched it:\n%s", read())
	}

	e := a.Field("effort")
	f.Set("magpie/deepseek/flash")
	if err := e.Set("high"); err != nil || e.Get() != "high" {
		t.Fatalf("effort: %v %q", err, e.Get())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	raw = read()
	if f.Get() != "" || strings.Contains(raw, "magpie") || strings.Contains(raw, "campaigns") || e.Get() != "high" {
		t.Fatalf("reset:\n%s", raw)
	}
}

func TestGrokReadErrorStopsChanges(t *testing.T) {
	const wired = "[model.\"magpie/keep\"]\nmodel = \"keep\"\n\n"
	for _, tc := range []struct {
		name, input string
	}{
		{"models", wired + "[models]\ndefault = [\n"},
		{"features", "[models]\ndefault = \"magpie/keep\"\n\n" + wired + "[features]\ncampaigns = [\n"},
	} {
		for _, action := range []string{"set", "clear", "sync"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("GROK_HOME", home)
				path := filepath.Join(home, "config.toml")
				if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
					t.Fatal(err)
				}
				a := grok(home)
				var err error
				switch action {
				case "set":
					err = a.Field("model").Set("grok-native")
				case "clear":
					err = a.Field("model").Set("")
				case "sync":
					err = a.Sync()
				}
				if err == nil || !strings.HasPrefix(err.Error(), path+": ") {
					t.Fatalf("expected the config path in the error, got %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.input {
					t.Fatalf("changed config after a read error:\n%s", got)
				}
			})
		}
	}
}

func TestGrokSyncRequiresOrdinaryModelTable(t *testing.T) {
	for _, body := range []string{
		"note = '''\n[model.\"magpie/fake\"]\n'''\n",
		"[[model.\"magpie/array\"]]\nmodel = \"array\"\n",
	} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GROK_HOME", home)
			path := filepath.Join(home, "config.toml")
			input := "[models]\ndefault = \"grok-native\"\n" + body
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := grok(home).Sync(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != input {
				t.Fatalf("synced a config without a magpie model table:\n%s", got)
			}
		})
	}
}

func TestGrokCheckReportsTOMLParseErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	input := "[models]\ndefault = \"magpie/fake\"\n\n[model.\"magpie/fake\"]\ninvalid = [\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	message := grok(home).Check()
	if !strings.HasPrefix(message, path+": line 5, column 11:") || !strings.Contains(message, "array is incomplete") {
		t.Fatalf("check did not report the parse location: %q", message)
	}
}
