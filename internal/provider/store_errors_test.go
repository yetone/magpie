package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func providerFileHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", home)
}

const originalProviderFile = `{"providers":[{"id":"original","name":"Original","chat":"https://original.example.invalid","key":"synthetic-original","models":["model"]}],"groups":[{"id":"original-group","members":["original/model"]}]}`

// Read failures must not turn the existing file into an empty catalog, or
// a partially decoded catalog that a subsequent edit writes over it.
func TestProviderWritesKeepInvalidFile(t *testing.T) {
	p := Provider{ID: "new", Name: "New", Chat: "https://new.example.invalid/v1", Key: "synthetic-new", Models: []string{"model"}}
	g := Group{ID: "new-group", Members: []string{"new/model"}}
	writes := []struct {
		name string
		run  func() error
	}{
		{"save", func() error { return Save(p) }},
		{"add", func() error { _, err := Add(p); return err }},
		{"save-group", func() error { return SaveGroup(g) }},
		{"restore", func() error { _, _, err := Restore([]Provider{p}); return err }},
		{"restore-groups", func() error { return RestoreGroups([]Group{g}) }},
		{"mirror", func() error { return Mirror([]Provider{p}, []Group{g}) }},
		{"quiet-account", func() error { return QuietAccount("codex") }},
		{"show-account", func() error { return ShowAccount("codex") }},
		{"delete", func() error { return Delete("original") }},
		{"delete-group", func() error { return DeleteGroup("original-group") }},
		{"show-group", func() error { return ShowGroup("original-group") }},
		{"rename", func() error { return Rename("original", "renamed") }},
		{"rename-group", func() error { return RenameGroup("original-group", "renamed-group") }},
		{"kiro-key", func() error { return setKiroKey("synthetic-kiro") }},
	}
	files := []struct{ name, data string }{
		{"truncated", originalProviderFile[:len(originalProviderFile)-1]},
		{"wrong-groups-type", strings.Replace(originalProviderFile, `[{"id":"original-group","members":["original/model"]}]`, `"not a list"`, 1)},
		{"wrong-provider-type", strings.Replace(originalProviderFile, `"key":"synthetic-original"`, `"key":123`, 1)},
		{"trailing-json", originalProviderFile + `{}`},
		{"empty", ""},
	}
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			for _, write := range writes {
				t.Run(write.name, func(t *testing.T) {
					providerFileHome(t)
					path := Path()
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					before := []byte(f.data)
					if err := os.WriteFile(path, before, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := write.run(); err == nil || !strings.Contains(err.Error(), path) {
						t.Errorf("want the providers.json read error, got %v", err)
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Error("invalid providers.json was overwritten")
					}
				})
			}
		})
	}
	// A real I/O error, rather than a JSON error, must also be reported.
	t.Run("read-error", func(t *testing.T) {
		for _, write := range writes {
			t.Run(write.name, func(t *testing.T) {
				providerFileHome(t)
				if err := os.MkdirAll(Path(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := write.run(); err == nil || !strings.Contains(err.Error(), Path()) {
					t.Errorf("want the providers.json read error, got %v", err)
				}
				if info, err := os.Stat(Path()); err != nil || !info.IsDir() {
					t.Fatalf("unreadable path changed: %v", err)
				}
			})
		}
	})
}

// The automatic /v1 correction has no error result: leave both the URL
// and the saved file alone when its read fails.
func TestFixV1KeepsInvalidFile(t *testing.T) {
	providerFileHome(t)
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte(strings.Replace(originalProviderFile, `[{"id":"original-group","members":["original/model"]}]`, `"not a list"`, 1))
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	base := "https://original.example.invalid"
	p := Provider{ID: "original", Chat: base}
	if got := p.fixV1(base, base+"/v1/models"); got != base {
		t.Errorf("failed correction returned %q, want %q", got, base)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("invalid providers.json was overwritten: %v", err)
	}
}

// A missing file remains a first use; a valid file keeps its providers,
// keys and routing groups when another provider is added or edited.
func TestProviderWritesKeepValidFile(t *testing.T) {
	providerFileHome(t)
	first := Provider{ID: "first", Chat: "https://first.example.invalid/v1", Key: "synthetic-first"}
	if err := Save(first); err != nil {
		t.Fatalf("first use: %v", err)
	}
	g := Group{ID: "kept", Members: []string{"first/model"}}
	if err := RestoreGroups([]Group{g}); err != nil {
		t.Fatal(err)
	}
	second := Provider{ID: "second", Chat: "https://second.example.invalid/v1", Key: "synthetic-second"}
	if id, err := Add(second); err != nil || id != second.ID {
		t.Fatalf("add to valid file: %q %v", id, err)
	}
	second.Name = "Edited"
	if err := Save(second); err != nil {
		t.Fatal(err)
	}
	// A keyless restore still keeps the existing key and unrelated entries.
	second.Key = ""
	if added, replaced, err := Restore([]Provider{second}); err != nil || added != 0 || replaced != 1 {
		t.Fatalf("restore into valid file: %d %d %v", added, replaced, err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var got file
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 2 || got.Providers[0].ID != "first" || got.Providers[0].Key != first.Key ||
		got.Providers[1].ID != "second" || got.Providers[1].Name != "Edited" || got.Providers[1].Key != "synthetic-second" ||
		len(got.Groups) != 1 || got.Groups[0].ID != "kept" {
		t.Fatal("a valid update lost existing providers, keys or groups")
	}
}
