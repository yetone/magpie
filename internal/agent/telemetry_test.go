package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// telemetryHome is a sandbox HOME with file (under it) holding body; "" is
// no file.
func telemetryHome(t *testing.T, file, body string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("QWEN_HOME", "")
	path = filepath.Join(home, file)
	if body != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, path
}

// jsonAt is the value at a dotted path of the JSON file at path, and
// whether it is there.
func jsonAt(t *testing.T, path, key string) (any, bool) {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(readFile(path)), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, readFile(path))
	}
	for _, part := range strings.Split(key, ".") {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil, false
		}
		if doc, ok = m[part]; !ok {
			return nil, false
		}
	}
	return doc, true
}

func telemetryOf(t *testing.T, a *Agent) TelemetryState {
	t.Helper()
	if a.Telemetry == nil {
		t.Fatalf("%s has no telemetry switch", a.ID)
	}
	st, err := a.Telemetry.Get()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// OipsAnthony on X: Codex's telemetry off from its page. Off writes
// [analytics] enabled = false beside what is there; on takes out only that,
// and the file is as it was.
func TestCodexTelemetrySwitch(t *testing.T) {
	const before = `model = "gpt-5.5"

[features]
multi_agent = true
`
	home, path := telemetryHome(t, ".codex/config.toml", before)
	a := codex(home)
	if st := telemetryOf(t, a); st.Off || len(st.Own) != 0 || !reflect.DeepEqual(st.Keys, []string{"analytics.enabled"}) {
		t.Fatalf("before: %+v", st)
	}
	if err := a.SetTelemetry(true); err != nil {
		t.Fatal(err)
	}
	got := readFile(path)
	if !strings.HasPrefix(got, before) || !strings.Contains(got, "[analytics]\nenabled = false\n") {
		t.Fatalf("off:\n%s", got)
	}
	if st := telemetryOf(t, codex(home)); !st.Off || len(st.Own) != 0 {
		t.Fatalf("after off: %+v", st)
	}
	// off again writes nothing new
	if err := a.SetTelemetry(true); err != nil || readFile(path) != got {
		t.Fatalf("off again: %v\n%s", err, readFile(path))
	}
	if err := codex(home).SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != before {
		t.Fatalf("on:\n%s", readFile(path))
	}
	if st := telemetryOf(t, a); st.Off {
		t.Fatalf("after on: %+v", st)
	}
}

// A Codex with no config.toml yet gets one with the table alone, and on
// leaves no table behind.
func TestCodexTelemetryNoConfig(t *testing.T) {
	home, path := telemetryHome(t, ".codex/config.toml", "")
	a := codex(home)
	if err := a.SetTelemetry(true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); strings.TrimSpace(got) != "[analytics]\nenabled = false" {
		t.Fatalf("off:\n%s", got)
	}
	if err := a.SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); strings.Contains(got, "analytics") {
		t.Fatalf("on:\n%s", got)
	}
}

// The user's own analytics setting is theirs: on is never written over,
// their own off is left in on "on", and a table written inline isn't
// added to. Nothing in the file moves.
func TestCodexTelemetryLeavesTheUsers(t *testing.T) {
	for _, c := range []struct {
		name, body string
		off        bool // the file keeps it off already
	}{
		{"on", "[analytics]\nenabled = true # mine\n", false},
		{"inline", "analytics = { }\n", false},
		{"dotted on", "analytics.enabled = true\n", false},
		{"their off", "[analytics]\nenabled = false # mine\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, ".codex/config.toml", c.body)
			a := codex(home)
			st := telemetryOf(t, a)
			if st.Off != c.off {
				t.Fatalf("state: %+v", st)
			}
			if c.off {
				if !reflect.DeepEqual(st.Own, []string{"analytics.enabled"}) {
					t.Fatalf("own: %+v", st)
				}
				if err := a.SetTelemetry(true); err != nil {
					t.Fatal(err)
				}
				// on: theirs stays, and says why it is still off
				if err := a.SetTelemetry(false); err == nil || !strings.Contains(err.Error(), "analytics.enabled") {
					t.Fatalf("on over the user's off: %v", err)
				}
			} else if err := a.SetTelemetry(true); err == nil {
				t.Fatal("off was written over the user's own value")
			}
			if readFile(path) != c.body {
				t.Fatalf("the user's file changed:\n%s", readFile(path))
			}
		})
	}
}

// A config that doesn't parse is unknown: no state is made up for it and
// nothing is written into it.
func TestTelemetryUnreadableConfig(t *testing.T) {
	for _, c := range []struct {
		name, file, body string
		of               func(string) *Agent
	}{
		{"codex", ".codex/config.toml", "[analytics\nenabled = ", codex},
		{"claude", ".claude/settings.json", `{"env": {`, claude},
		{"gemini", ".gemini/settings.json", `{"privacy": [`, gemini},
		{"qwen", ".qwen/settings.json", `{"privacy": `, qwen},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, c.file, c.body)
			a := c.of(home)
			if a.Telemetry == nil {
				t.Fatal("no telemetry switch")
			}
			if _, err := a.Telemetry.Get(); err == nil {
				t.Fatal("an unreadable config read as a state")
			}
			if err := a.SetTelemetry(true); err == nil {
				t.Fatal("off written into an unreadable config")
			}
			if readFile(path) != c.body {
				t.Fatalf("written:\n%s", readFile(path))
			}
		})
	}
}

// Claude Code's telemetry and error reports go off in settings.json's env
// beside the user's variables; on takes out only magpie's, and a variable
// the user set keeps its value.
func TestClaudeTelemetrySwitch(t *testing.T) {
	home, path := telemetryHome(t, ".claude/settings.json", `{
  "model": "opus",
  "env": {"FOO": "1", "DISABLE_TELEMETRY": "true"}
}
`)
	a := claude(home)
	st := telemetryOf(t, a)
	if st.Off || !reflect.DeepEqual(st.Keys, []string{"env.DISABLE_TELEMETRY", "env.DISABLE_ERROR_REPORTING"}) {
		t.Fatalf("before: %+v", st)
	}
	if err := a.SetTelemetry(true); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"env.DISABLE_TELEMETRY": "true", "env.DISABLE_ERROR_REPORTING": "1", "env.FOO": "1", "model": "opus"} {
		if v, _ := jsonAt(t, path, k); v != want {
			t.Fatalf("off: %s = %v:\n%s", k, v, readFile(path))
		}
	}
	if st := telemetryOf(t, a); !st.Off || !reflect.DeepEqual(st.Own, []string{"env.DISABLE_TELEMETRY"}) {
		t.Fatalf("after off: %+v", st)
	}
	// on: magpie's goes; the user's DISABLE_TELEMETRY stays, and with
	// error reports back on, telemetry reads as not all off (no error)
	if err := a.SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
	if _, ok := jsonAt(t, path, "env.DISABLE_ERROR_REPORTING"); ok {
		t.Fatalf("on kept magpie's:\n%s", readFile(path))
	}
	if v, _ := jsonAt(t, path, "env.DISABLE_TELEMETRY"); v != "true" {
		t.Fatalf("on took the user's:\n%s", readFile(path))
	}
}

// A value magpie wrote that the user changed since is theirs: on leaves it.
func TestClaudeTelemetryChangedSince(t *testing.T) {
	home, path := telemetryHome(t, ".claude/settings.json", `{"env": {}}`)
	a := claude(home)
	if err := a.SetTelemetry(true); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.DISABLE_TELEMETRY", Value: "yes"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
	if v, _ := jsonAt(t, path, "env.DISABLE_TELEMETRY"); v != "yes" {
		t.Fatalf("on took the user's changed value:\n%s", readFile(path))
	}
	if _, ok := jsonAt(t, path, "env.DISABLE_ERROR_REPORTING"); ok {
		t.Fatalf("on kept magpie's:\n%s", readFile(path))
	}
}

// Gemini CLI's and Qwen Code's usage statistics go off in settings.json's
// privacy, beside what is there; their own true is never written over.
func TestUsageStatsTelemetrySwitch(t *testing.T) {
	for _, c := range []struct {
		name, file string
		of         func(string) *Agent
	}{
		{"gemini", ".gemini/settings.json", gemini},
		{"qwen", ".qwen/settings.json", qwen},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, c.file, `{"privacy": {"other": 1}, "ui": {"theme": "x"}}`)
			a := c.of(home)
			if st := telemetryOf(t, a); st.Off {
				t.Fatalf("before: %+v", st)
			}
			if err := a.SetTelemetry(true); err != nil {
				t.Fatal(err)
			}
			if v, ok := jsonAt(t, path, "privacy.usageStatisticsEnabled"); !ok || v != false {
				t.Fatalf("off:\n%s", readFile(path))
			}
			if st := telemetryOf(t, a); !st.Off || len(st.Own) != 0 {
				t.Fatalf("after off: %+v", st)
			}
			if err := a.SetTelemetry(false); err != nil {
				t.Fatal(err)
			}
			if _, ok := jsonAt(t, path, "privacy.usageStatisticsEnabled"); ok {
				t.Fatalf("on:\n%s", readFile(path))
			}
			if v, _ := jsonAt(t, path, "privacy.other"); v != float64(1) {
				t.Fatalf("on moved the user's:\n%s", readFile(path))
			}
			if v, _ := jsonAt(t, path, "ui.theme"); v != "x" {
				t.Fatalf("on moved the user's:\n%s", readFile(path))
			}

			const theirs = `{"privacy": {"usageStatisticsEnabled": true}}`
			if err := os.WriteFile(path, []byte(theirs), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := a.SetTelemetry(true); err == nil || readFile(path) != theirs {
				t.Fatalf("off over the user's true: %v\n%s", err, readFile(path))
			}
		})
	}
}

// An agent in a WSL distro has no switch: its file isn't read on each
// look (#3896).
func TestTelemetryOnlyHere(t *testing.T) {
	home, _ := telemetryHome(t, ".codex/config.toml", "")
	at := place{home: home, spell: func(p string) string { return p }}
	for _, a := range []*Agent{codexIn(at), claudeIn(at), geminiIn(at), qwenIn(at)} {
		if a.Telemetry != nil {
			t.Errorf("%s in a distro has a telemetry switch", a.ID)
		}
	}
	if codex(home).Telemetry == nil {
		t.Error("this machine's Codex has none")
	}
}
