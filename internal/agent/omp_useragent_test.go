package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"gopkg.in/yaml.v3"
)

// An omp that names itself (omp/<version>, from 18.1.6) is left to it: a
// User-Agent of magpie's would put its version out of sight (Zyvaletta on
// X). An older one, which asks with Bun's, is named with its version, and
// one whose version isn't known plain omp; the gateway takes each for omp.
func TestOmpUserAgentByVersion(t *testing.T) {
	for _, c := range []struct{ version, want string }{
		{"16.3.5", "omp/16.3.5"},
		{"18.1.5", "omp/18.1.5"},
		{"18.1.6", ""},
		{"18.4.4", ""},
		{"18.22.2", ""},
		{"", "omp"},
	} {
		e := ompProviderAt("http://127.0.0.1:3425", c.version)
		got := e.Headers["User-Agent"]
		if got != c.want || c.want == "" && len(e.Headers) > 0 {
			t.Errorf("omp %q: headers %v, want User-Agent %q", c.version, e.Headers, c.want)
		}
		if c.want == "" {
			continue
		}
		if usage.AgentOf(got) != "omp" {
			t.Errorf("omp %q: %q is taken for %q", c.version, got, usage.AgentOf(got))
		}
		if !ompOwnUserAgent(got) {
			t.Errorf("omp %q: magpie's own %q read back as the user's", c.version, got)
		}
	}
	// what omp sends itself is omp's to the gateway too
	if a := usage.AgentOf("omp/18.4.4"); a != "omp" {
		t.Errorf("omp/18.4.4 is taken for %q", a)
	}
}

// magpie's provider entry in a sandbox's models.yml after a sync, with the
// omp on PATH at version.
func ompSyncedHeaders(t *testing.T, version string) (map[string]string, string) {
	t.Helper()
	was := ompVersion
	ompVersion = func() string { return version }
	defer func() { ompVersion = was }()
	home := os.Getenv("HOME")
	if err := omp(home).Sync(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".omp", "agent", "models.yml"))
	var doc struct {
		Providers map[string]struct {
			Headers map[string]string `yaml:"headers"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return doc.Providers[magpieID].Headers, string(raw)
}

func ompUASandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	was := ompVersion
	ompVersion = func() string { return "" }
	defer func() { ompVersion = was }()
	if err := omp(home).Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".omp", "agent", "models.yml")
}

// The User-Agent: omp an older magpie wrote is magpie's: the next sync puts
// the one it writes now in its place, none for an omp that names itself,
// and a header of the user's own beside it stays.
func TestOmpOldMagpieUserAgentSynced(t *testing.T) {
	models := ompUASandbox(t)
	if err := edit.SetYAML(models,
		edit.KV{Path: "providers.magpie.headers.User-Agent", Value: "omp"},
		edit.KV{Path: "providers.magpie.headers.X-Mine", Value: "1"},
	); err != nil {
		t.Fatal(err)
	}
	h, raw := ompSyncedHeaders(t, "18.9.1")
	if _, ok := h["User-Agent"]; ok || h["X-Mine"] != "1" {
		t.Fatalf("omp 18.9.1 names itself, yet:\n%s", raw)
	}
	h, raw = ompSyncedHeaders(t, "16.3.5")
	if h["User-Agent"] != "omp/16.3.5" || h["X-Mine"] != "1" {
		t.Fatalf("omp 16.3.5:\n%s", raw)
	}
	// magpie's omp/<version> is magpie's too: an omp updated takes its own
	h, raw = ompSyncedHeaders(t, "18.1.5")
	if h["User-Agent"] != "omp/18.1.5" {
		t.Fatalf("omp 18.1.5 after 16.3.5:\n%s", raw)
	}
	h, raw = ompSyncedHeaders(t, "18.1.6")
	if _, ok := h["User-Agent"]; ok || h["X-Mine"] != "1" {
		t.Fatalf("omp 18.1.6 after 18.1.5:\n%s", raw)
	}
	h, raw = ompSyncedHeaders(t, "")
	if h["User-Agent"] != "omp" {
		t.Fatalf("an omp of no known version:\n%s", raw)
	}
}

// A User-Agent the user set on magpie's provider is theirs, under any
// casing: no sync writes magpie's over it or beside it, for an omp of any
// version.
func TestOmpUsersOwnUserAgentStays(t *testing.T) {
	for _, key := range []string{"User-Agent", "user-agent"} {
		t.Run(key, func(t *testing.T) {
			models := ompUASandbox(t)
			if err := edit.SetYAML(models, edit.KV{Path: "providers.magpie.headers." + key, Value: "my-proxy/2"}); err != nil {
				t.Fatal(err)
			}
			for _, v := range []string{"16.3.5", "18.9.1", ""} {
				h, raw := ompSyncedHeaders(t, v)
				if h[key] != "my-proxy/2" || len(h) != 1 {
					t.Fatalf("omp %q: the user's User-Agent not kept alone:\n%s", v, raw)
				}
			}
		})
	}
}
