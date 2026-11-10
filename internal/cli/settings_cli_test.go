package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/settingsapi"
)

// settingsHome is a home of its own whose settings.json has the user's
// own choices in it, and gives that file's bytes.
func settingsHome(t *testing.T) []byte {
	t.Helper()
	helpHome(t)
	s := settings.Defaults()
	s.Theme, s.NoBird, s.Currency, s.Proxy = "dark", true, "cny", "http://127.0.0.1:7890"
	s.RedactWords = []string{"acme"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSettingsSetGetReset(t *testing.T) {
	settingsHome(t)
	if _, err := runOut(t, "settings", "set", "lang", "ja"); err != nil {
		t.Fatal(err)
	}
	if out, err := runOut(t, "settings", "get", "lang"); err != nil || strings.TrimSpace(out) != "ja" {
		t.Fatalf("get lang = %q, %v", out, err)
	}
	s := settings.Load()
	if s.Lang != "ja" || s.Theme != "dark" || !s.NoBird || s.Currency != "cny" || s.Proxy != "http://127.0.0.1:7890" || len(s.RedactWords) != 1 {
		t.Fatalf("setting lang changed what the user set: %+v", s)
	}
	// a switch to off is sent, not left out as a zero value
	if _, err := runOut(t, "settings", "set", "noBird", "off"); err != nil {
		t.Fatal(err)
	}
	if settings.Load().NoBird {
		t.Error("noBird off stayed on")
	}
	// numbers, lists, and a value the save puts in its own form
	for _, c := range []struct{ key, value, want string }{
		{"usageAlert", "80", "80"},
		{"trayUsageEvery", "10", "10"},
		{"redactWords", "acme, globex", "acme,globex"},
		{"redactWords", `["a","b"]`, "a,b"},
		{"redactWords", "", "[]"},
		{"codexWarmup", "off", `""`},
		// the check-in's time of day (times_ang on X), as 09:00
		{"checkinAt", "9:30", "09:30"},
		{"checkinAt", "", `""`},
		{"refusalRetries", "2", "2"},
		{"otel", `{"enabled":true,"endpoint":"http://127.0.0.1:4318"}`, ""},
	} {
		if _, err := runOut(t, "settings", "set", c.key, c.value); err != nil {
			t.Fatalf("set %s %q: %v", c.key, c.value, err)
		}
		out, _ := runOut(t, "settings", "get", c.key)
		if c.want != "" && strings.TrimSpace(out) != c.want {
			t.Errorf("set %s %q, then get = %q, want %q", c.key, c.value, out, c.want)
		}
	}
	if _, err := runOut(t, "settings", "reset", "theme"); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.Theme != "system" || s.Currency != "cny" {
		t.Errorf("reset theme: theme %q currency %q", s.Theme, s.Currency)
	}
	if _, err := runOut(t, "settings", "reset", "refusalRetries"); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.RefusalRetries != nil {
		t.Errorf("reset refusalRetries left %d", *s.RefusalRetries)
	}
}

// A key or value that isn't one, and a flag where a value goes, write
// nothing and say why.
func TestSettingsRefusesAndWritesNothing(t *testing.T) {
	before := settingsHome(t)
	for _, args := range [][]string{
		{"settings", "set", "nope", "1"},
		{"settings", "set", "port", "9999"},  // a route of its own
		{"settings", "set", "theme", "neon"}, // not one of its choices
		{"settings", "set", "noBird", "maybe"},
		{"settings", "set", "usageAlert", "lots"},
		{"settings", "set", "usageAlert", "200"}, // the save's own range
		{"settings", "set", "trayUsageEvery", "4"},
		{"settings", "set", "checkinAt", "25:00"},
		{"settings", "set", "vision", "nobody/nothing"},
		{"settings", "set", "otel", `{"bogus":1}`},
		{"settings", "set", "theme", "--help-me"},
		{"settings", "set", "theme"},
		{"settings", "reset", "nope"},
		{"settings", "get", "nope"},
		{"settings", "frob"},
	} {
		out, err := runOut(t, args...)
		if err == nil {
			t.Errorf("magpie %s: no error (%q)", strings.Join(args, " "), out)
		}
		after, _ := os.ReadFile(settings.Path())
		if string(after) != string(before) {
			t.Fatalf("magpie %s changed settings.json", strings.Join(args, " "))
		}
	}
	_, err := runOut(t, "settings", "set", "them", "dark")
	if err == nil || !strings.Contains(err.Error(), "theme") {
		t.Errorf("a near miss doesn't name the key: %v", err)
	}
	// a negative number is a value, not a flag
	_, err = runOut(t, "settings", "set", "usageAlert", "-1")
	if err == nil || strings.Contains(err.Error(), "flag") {
		t.Errorf("-1 read as a flag or saved: %v", err)
	}
}

func TestSettingsListAndJSON(t *testing.T) {
	settingsHome(t)
	out, err := runOut(t, "settings", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []settingRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	if len(rows) != len(settingsapi.Keys) {
		t.Fatalf("list --json has %d rows, Keys %d", len(rows), len(settingsapi.Keys))
	}
	for _, r := range rows {
		if r.Key == "theme" && (r.Value != "dark" || r.Default != "system") {
			t.Errorf("theme row: %+v", r)
		}
	}
	out, err = runOut(t, "settings")
	if err != nil || !strings.Contains(out, "theme") || !strings.Contains(out, "default system") {
		t.Errorf("settings list: %v\n%s", err, out)
	}
	out, err = runOut(t, "settings", "get", "redactWords", "--json")
	if err != nil || strings.TrimSpace(strings.Join(strings.Fields(out), "")) != `["acme"]` {
		t.Errorf("get redactWords --json = %q, %v", out, err)
	}
}
