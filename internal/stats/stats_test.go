package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// One event a day, under the same id, with nothing but the id, the version
// and the system, and what magpie is used with by built-in ids; none once
// the user turns it off.
func TestSend(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	usage = func() Usage {
		return Usage{Agents: []string{"claude", "codex"}, Providers: []string{"deepseek", "custom"}, Models: []string{"deepseek/deepseek-v4"}, Groups: 2}
	}
	defer func() { usage = readUsage }()
	var got [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Key   string           `json:"api_key"`
			Batch []map[string]any `json:"batch"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if r.URL.Path != "/batch/" || b.Key != Key {
			t.Errorf("sent %s %v", r.URL.Path, b)
		}
		got = append(got, b.Batch)
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_STATS_HOST", srv.URL)
	day := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{day, day.Add(3 * time.Hour), day.Add(24 * time.Hour)} {
		if err := Send(context.Background(), "0.1.300", "app", at); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("%d sends for two days", len(got))
	}
	if got[0][0]["distinct_id"] != got[1][0]["distinct_id"] || len(got[0][0]["distinct_id"].(string)) != 32 {
		t.Fatalf("ids: %v %v", got[0][0]["distinct_id"], got[1][0]["distinct_id"])
	}
	a := got[0][0]
	p := a["properties"].(map[string]any)
	if a["event"] != "magpie active" || p["version"] != "0.1.300" || p["kind"] != "app" || p["agents"] != 2.0 || p["providers"] != 2.0 || p["models"] != 1.0 || p["groups"] != 2.0 || len(p) != 10 {
		t.Fatalf("active: %v", a)
	}
	var uses []string
	for _, e := range got[0][1:] {
		p := e["properties"].(map[string]any)
		if e["event"] != "magpie uses" || e["distinct_id"] != a["distinct_id"] || p["$process_person_profile"] != false || len(p) != 4 {
			t.Fatalf("use: %v", e)
		}
		uses = append(uses, p["kind"].(string)+" "+p["id"].(string))
	}
	if want := "agent claude,agent codex,provider deepseek,provider custom,model deepseek/deepseek-v4"; strings.Join(uses, ",") != want {
		t.Fatalf("uses %q, want %q", uses, want)
	}

	// what it is used with turned off: the count of the user only
	settings.Save(settings.Settings{NoUsageStats: true})
	Send(context.Background(), "0.1.300", "app", day.Add(48*time.Hour))
	if len(got) != 3 || len(got[2]) != 1 || len(got[2][0]["properties"].(map[string]any)) != 6 {
		t.Fatalf("sent with usage off: %v", got[2:])
	}

	settings.Save(settings.Settings{NoStats: true})
	Send(context.Background(), "0.1.300", "app", day.Add(72*time.Hour))
	t.Setenv("MAGPIE_NO_STATS", "1")
	settings.Save(settings.Settings{})
	Send(context.Background(), "0.1.300", "app", day.Add(96*time.Hour))
	if len(got) != 3 {
		t.Fatalf("sent while off: %d", len(got))
	}
}

// A provider is named by what magpie built in, never by what the user
// typed: a preset's id, a subscription's agent, a known plugin's id;
// anything else is "custom" or "plugin".
func TestProviderLabel(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want string
	}{
		{provider.Provider{ID: "my-deepseek", Name: "My DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1"}, "deepseek"},
		{provider.Provider{ID: "secret-relay", Name: "Secret Relay", Chat: "https://relay.example/v1"}, "custom"},
		{provider.Provider{ID: "odd", Name: "Odd", Preset: "no-such-preset"}, "custom"},
		{provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "someone@example.com"}}, "codex"},
	} {
		if got := providerLabel(c.p); got != c.want {
			t.Errorf("%s: %q, want %q", c.p.ID, got, c.want)
		}
	}
}

// Nothing the user named goes as an agent: an omp profile, a WSL distro.
func TestAgentLabel(t *testing.T) {
	for id, want := range map[string]string{
		"claude":                   "claude",
		"claude@wsl:Ubuntu":        "claude",
		"omp#acme-secret":          "omp",
		"omp#acme-secret@wsl:Work": "omp",
	} {
		if got := agentLabel(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

// A model on a key's provider goes by its id only when models.dev knows
// it: one the user named (an Ollama model, a fine-tune naming their
// organisation) is "other".
func TestModelLabelKeepsTheUsersOwnModelIDs(t *testing.T) {
	cache := map[string]any{"deepseek": map[string]any{"id": "deepseek", "models": map[string]any{
		"deepseek-v4": map[string]any{"id": "deepseek-v4", "limit": map[string]any{"context": 128000}},
	}}}
	b, _ := json.Marshal(cache)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(func() { os.Remove(catalog.CachePath()); catalog.Reset() })
	for _, p := range []provider.Provider{
		{ID: "my-deepseek", Name: "DS", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk-x", Models: []string{"deepseek-v4"}},
		{ID: "my-openai", Name: "OA", Preset: "openai", Chat: "https://api.openai.com/v1", Key: "sk-y", Models: []string{"ft:gpt-4o:acme-corp::abc123"}},
		{ID: "my-ollama", Name: "Local", Preset: "ollama", Chat: "http://127.0.0.1:11434/v1", Models: []string{"acme-internal-llm"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for ref, want := range map[string]string{
		"my-deepseek/deepseek-v4":               "deepseek/deepseek-v4",
		"my-openai/ft:gpt-4o:acme-corp::abc123": "openai/other",
		"my-ollama/acme-internal-llm":           "ollama/other",
	} {
		if got := modelLabel(ref); got != want {
			t.Errorf("%s: %q, want %q", ref, got, want)
		}
	}
}

// The partners' counts of the days ended go with the day's event, each as
// one "magpie partner" at its own day, and are sent once; with what magpie
// is used with turned off they don't go.
func TestSendPartnerCounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	usage = func() Usage { return Usage{} }
	defer func() { usage = readUsage }()
	counts := `{"days":{"2026-09-27":{"acme":{"shown":12,"opened":2}},"2026-09-28":{"acme":{"shown":3}}}}`
	write := func() {
		f := filepath.Join(home, ".cache", "magpie", "partner-counts.json")
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(counts), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write()
	var got [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Batch []map[string]any `json:"batch"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		got = append(got, b.Batch)
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_STATS_HOST", srv.URL)
	day := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	partners := func(batch []map[string]any) []string {
		var out []string
		for _, e := range batch {
			if e["event"] != "magpie partner" {
				continue
			}
			p := e["properties"].(map[string]any)
			out = append(out, fmt.Sprintf("%s %s %s %v %v", e["timestamp"], p["id"], p["what"], p["count"], p["$process_person_profile"]))
		}
		return out
	}
	Send(context.Background(), "0.1.300", "app", day)
	if want := "2026-09-27T12:00:00Z acme opened 2 false,2026-09-27T12:00:00Z acme shown 12 false"; strings.Join(partners(got[0]), ",") != want {
		t.Fatalf("partner events %q, want %q", partners(got[0]), want)
	}
	Send(context.Background(), "0.1.300", "app", day.Add(24*time.Hour))
	if want := "2026-09-28T12:00:00Z acme shown 3 false"; strings.Join(partners(got[1]), ",") != want {
		t.Fatalf("the next day sent %q, want %q", partners(got[1]), want)
	}

	write()
	settings.Save(settings.Settings{NoUsageStats: true})
	Send(context.Background(), "0.1.300", "app", day.Add(48*time.Hour))
	if len(got) != 3 || len(partners(got[2])) != 0 {
		t.Fatalf("sent with usage off: %v", got[2:])
	}
}
