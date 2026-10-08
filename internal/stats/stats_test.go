package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// One event a day, under the same id, with nothing but the id, the version
// and the system, and what magpie is used with by built-in ids; none once
// the user turns it off.
func TestSend(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
