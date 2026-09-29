package provider

import (
	"path/filepath"
	"testing"
)

func TestPresetKeepsHeaders(t *testing.T) {
	p, _ := FromPreset("openrouter")
	p.Headers = map[string]string{" HTTP-Referer ": " https://example.com ", "": "x"}
	p = normalize(p)
	if p.Preset != "openrouter" || len(p.Headers) != 1 || p.Headers["HTTP-Referer"] != "https://example.com" {
		t.Fatalf("preset headers: %+v", p.Headers)
	}
}

func TestPresetHeaderHints(t *testing.T) {
	for id, want := range map[string]string{"anthropic": "anthropic-workspace-id", "openrouter": "X-OpenRouter-Title"} {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("no preset %q", id)
		}
		found := false
		for _, h := range pr.HeaderHints {
			found = found || h == want
		}
		if !found {
			t.Fatalf("%s hints %v, want %s among them", id, pr.HeaderHints, want)
		}
	}
}

// A provider named in Chinese, whose name slugs to nothing, is added under
// its host's name, and a second one beside it (mamba on Discord: 第二个会提示
// id重复).
func TestAddNameWithoutLatin(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, c := range []struct{ name, url, key, want string }{
		{"中转站", "https://api.relay-one.com/v1", "sk-1", "relay-one"},
		{"中转站", "https://api.relay-one.com/v1", "sk-2", "relay-one-2"},
		{"另一个", "https://gw.example.cn:8443/v1", "sk-3", "gw-example"},
		{"本地", "http://127.0.0.1:8080/v1", "sk-4", "custom"},
		{"本地二", "http://[::1]:8080/v1", "sk-5", "custom-2"},
	} {
		id, err := Add(Provider{Name: c.name, Chat: c.url, Key: c.key, Models: []string{"m"}})
		if err != nil || id != c.want {
			t.Fatalf("%s at %s: %q %v, want %q", c.name, c.url, id, err, c.want)
		}
	}
	if p, err := Find("relay-one-2"); err != nil || p.Name != "中转站 2" {
		t.Fatalf("second: %+v %v", p, err)
	}
}

// Adding a preset a second time adds another provider of it, the preset
// kept, beside the first rather than over it.
func TestAddSecondOfPreset(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	add := func(ws string) string {
		p, _ := FromPreset("anthropic")
		p.Key, p.Headers = "sk-ant", map[string]string{"anthropic-workspace-id": ws}
		id, err := Add(p)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if id := add("ws1"); id != "anthropic" {
		t.Fatalf("first add took %q, want the preset's id", id)
	}
	if id := add("ws2"); id != "anthropic-2" {
		t.Fatalf("second add took %q", id)
	}
	// the same key, host and headers again is the one already here
	dup, _ := FromPreset("anthropic")
	dup.Key, dup.Headers = "sk-ant", map[string]string{"anthropic-workspace-id": "ws2"}
	if id, err := Add(dup); err == nil {
		t.Fatalf("a duplicate was added as %q", id)
	}
	a, _ := Find("anthropic")
	b, err := Find("anthropic-2")
	if err != nil || b.Preset != "anthropic" || b.Name != "Anthropic 2" || b.Headers["anthropic-workspace-id"] != "ws2" || b.Icon != a.Icon || b.Catalog != "anthropic" {
		t.Fatalf("second: %+v %v", b, err)
	}
	if a.Headers["anthropic-workspace-id"] != "ws1" || a.Name != "Anthropic" {
		t.Fatalf("first changed: %+v", a)
	}
	// a name of the user's own gives the id, taken or not
	p, _ := FromPreset("anthropic")
	p.ID, p.Name, p.Key = "", "Anthropic Work", "sk-work"
	if id, err := Add(p); err != nil || id != "anthropic-work" {
		t.Fatalf("named: %q %v", id, err)
	}
	// saving by id still edits in place
	a.Headers = nil
	if err := Save(*a); err != nil {
		t.Fatal(err)
	}
	if a, _ := Find("anthropic"); len(a.Headers) != 0 || len(All()) != 3 {
		t.Fatalf("edit: %+v, %d providers", a, len(All()))
	}
}
