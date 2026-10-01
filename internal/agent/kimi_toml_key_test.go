package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestKimiLiteralQuotedModelKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("default_model = \"kimi k2.5\"\n\n[models.'kimi k2.5']\nprovider = \"moonshot\"\nmodel = \"kimi-k2.5\"\n\n[models.kimi-k2]\nprovider = \"moonshot\"\nmodel = \"kimi-k2\"\n"), 0o600)
	opts := kimiOwnOptions(p, "")
	var vals []string
	for _, o := range opts {
		vals = append(vals, o.Value)
	}
	t.Logf("options: %q", vals)
	found := false
	for _, v := range vals {
		if v == "kimi k2.5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("single-quoted literal model key missing from options: %q", vals)
	}
}

func TestKimiNestedLiteralKeyIsNotAModel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[models.'a'.'b']\nmodel = 'nested'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opts := kimiOwnOptions(p, ""); len(opts) != 0 {
		t.Fatalf("nested table listed as model: %v", opts)
	}
}

func TestKimiRestoresDecodedModelKey(t *testing.T) {
	for _, tc := range []struct{ name, header, key string }{
		{"literal", "models.'kimi k2.5'", "kimi k2.5"},
		{"bare", "models.kimi-k2", "kimi-k2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			dir := filepath.Join(home, ".kimi-code")
			t.Setenv("KIMI_CODE_HOME", dir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.toml")
			raw := "default_model = \"" + tc.key + "\"\n[" + tc.header + "]\nprovider = 'moonshot'\nmodel = 'kimi-k2'\n"
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			field := kimi(home).Field("model")
			if err := field.Set("magpie/deepseek/pro"); err != nil {
				t.Fatal(err)
			}
			if err := field.Set(""); err != nil {
				t.Fatal(err)
			}
			if got := field.Get(); got != tc.key {
				t.Fatalf("restored %q, want %q", got, tc.key)
			}
		})
	}
}
