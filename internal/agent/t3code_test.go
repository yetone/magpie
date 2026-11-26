package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// t3User is ~/.t3/userdata/settings.json as T3 Code writes it sparse: its
// Claude given a binary path (the legacy key), a Codex account of the
// user's own as an instance with a custom model, and a key of T3's.
const t3User = `{
  "providers": {
    "claudeAgent": {"binaryPath": "/opt/claude/bin/claude"}
  },
  "providerInstances": {
    "codex_work": {
      "driver": "codex",
      "displayName": "Codex (work)",
      "config": {"homePath": "~/.codex-work", "customModels": [{"slug": "gpt-6-astra", "name": "gpt-6-astra"}]}
    }
  },
  "textGenerationModelSelection": {"instanceId": "codex", "model": "gpt-6-luna"}
}
`

// t3Envelope is T3 Code's ProviderInstanceConfig, with the claudeAgent
// config magpie writes; nothing else taken.
type t3Envelope struct {
	Driver      string `json:"driver"`
	DisplayName string `json:"displayName"`
	Enabled     *bool  `json:"enabled"`
	Environment []struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		Sensitive *bool  `json:"sensitive"`
	} `json:"environment"`
	Config struct {
		BinaryPath   string `json:"binaryPath"`
		HomePath     string `json:"homePath"`
		LaunchArgs   string `json:"launchArgs"`
		CustomModels []struct {
			Slug         string `json:"slug"`
			Name         string `json:"name"`
			Capabilities *struct {
				OptionDescriptors []struct {
					ID           string `json:"id"`
					Label        string `json:"label"`
					Type         string `json:"type"`
					CurrentValue *bool  `json:"currentValue"`
					Options      []struct {
						ID        string `json:"id"`
						Label     string `json:"label"`
						IsDefault *bool  `json:"isDefault"`
					} `json:"options"`
				} `json:"optionDescriptors"`
			} `json:"capabilities"`
		} `json:"customModels"`
	} `json:"config"`
}

// t3Format decodes magpie's instance strictly as T3 Code's schema has it:
// an instance id of its slug pattern, a claudeAgent envelope, environment
// variable names of its pattern, custom models with a slug.
func t3Format(t *testing.T, raw []byte) t3Envelope {
	t.Helper()
	var e t3Envelope
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		t.Fatalf("not T3 Code's instance: %v\n%s", err, raw)
	}
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`).MatchString(magpieID) {
		t.Errorf("instance id %q", magpieID)
	}
	if e.Driver != "claudeAgent" || e.DisplayName == "" || e.Enabled == nil || !*e.Enabled {
		t.Errorf("envelope: %s", raw)
	}
	for _, v := range e.Environment {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(v.Name) || v.Sensitive == nil || *v.Sensitive {
			t.Errorf("environment: %s", raw)
		}
	}
	for _, m := range e.Config.CustomModels {
		if m.Slug == "" || m.Name == "" {
			t.Errorf("custom model: %s", raw)
		}
		if m.Capabilities == nil {
			continue
		}
		for _, d := range m.Capabilities.OptionDescriptors {
			defaults := 0
			for _, o := range d.Options {
				if o.ID == "" || o.Label == "" {
					t.Errorf("option: %s", raw)
				}
				if o.IsDefault != nil && *o.IsDefault {
					defaults++
				}
			}
			select_ := d.Type == "select" && len(d.Options) > 0 && defaults == 1 && d.CurrentValue == nil
			boolean := d.Type == "boolean" && len(d.Options) == 0 && d.CurrentValue != nil
			if d.ID == "" || d.Label == "" || !select_ && !boolean {
				t.Errorf("descriptor: %s", raw)
			}
		}
	}
	return e
}

// T3 Code (KevinXC on Discord) gets magpie as a provider of its own: a
// claudeAgent instance on the gateway with every magpie model as a custom
// model, the user's instances, their Claude's binary path and T3's keys
// kept; off takes only magpie's out, and the catalog sync leaves it off.
func TestT3CodeInstance(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(home, ".t3")
	path := filepath.Join(base, "userdata", "settings.json")

	a := t3code(home)
	if a.Path != path {
		t.Fatalf("path %s", a.Path)
	}
	if a.Detected() && !t3App(home) {
		t.Fatal("found with no ~/.t3")
	}
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "t3code" && x.Name == "T3 Code" }) {
		t.Fatal("T3 Code is not among the agents")
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(t3User), 0o600)
	if !a.Detected() {
		t.Fatal("not found with ~/.t3")
	}
	f := a.Field("provider")
	if f == nil || f.Get() != "" {
		t.Fatalf("provider field: %+v", f)
	}
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if f.Get() != magpieID {
		t.Fatalf("get: %q", f.Get())
	}

	var file, user map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	json.Unmarshal([]byte(t3User), &user)
	for _, k := range []string{"providers", "textGenerationModelSelection"} {
		if !sameJSON(string(file[k]), user[k]) {
			t.Errorf("T3's %s changed: %s", k, file[k])
		}
	}
	var insts, userInsts map[string]json.RawMessage
	json.Unmarshal(file["providerInstances"], &insts)
	json.Unmarshal(user["providerInstances"], &userInsts)
	if !sameJSON(string(insts["codex_work"]), userInsts["codex_work"]) {
		t.Errorf("the user's instance changed: %s", insts["codex_work"])
	}

	e := t3Format(t, insts[magpieID])
	env := map[string]string{}
	for _, v := range e.Environment {
		env[v.Name] = v.Value
	}
	if env["ANTHROPIC_BASE_URL"] != gateway.URL() || env["ANTHROPIC_AUTH_TOKEN"] != gateway.Token {
		t.Errorf("environment: %v", env)
	}
	if e.Config.BinaryPath != "/opt/claude/bin/claude" || e.Config.HomePath != "" {
		t.Errorf("T3's Claude's binary path not carried over: %+v", e.Config)
	}
	slugs := []string{}
	for _, m := range e.Config.CustomModels {
		slugs = append(slugs, m.Slug)
	}
	for _, id := range []string{"relay/glm-4.6", "resp/gpt-5.5"} {
		if !slices.Contains(slugs, id) {
			t.Errorf("%s not listed: %v", id, slugs)
		}
	}

	// the catalog sync keeps it, and the user's
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(readFile(path)), &file)
	insts = nil
	json.Unmarshal(file["providerInstances"], &insts)
	t3Format(t, insts[magpieID])
	if _, ok := insts["codex_work"]; !ok {
		t.Fatal("the user's instance is gone after a sync")
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("get after off: %q", f.Get())
	}
	file, insts = nil, nil
	json.Unmarshal([]byte(readFile(path)), &file)
	json.Unmarshal(file["providerInstances"], &insts)
	if _, ok := insts[magpieID]; ok || !sameJSON(string(insts["codex_work"]), userInsts["codex_work"]) ||
		!sameJSON(string(file["providers"]), user["providers"]) {
		t.Errorf("after off: %s", readFile(path))
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" {
		t.Fatalf("sync put it back: %s", readFile(path))
	}
}

// T3 Code's Claude already an instance (T3 moves it there once edited in
// its settings) has its Claude home carried over from there; $T3CODE_HOME
// moves the file, and a settings.json not there yet is made.
func TestT3CodeHomeAndInstance(t *testing.T) {
	home := syncHome(t)
	base := filepath.Join(home, "elsewhere")
	t.Setenv("T3CODE_HOME", base)
	a := t3code(home)
	path := filepath.Join(base, "userdata", "settings.json")
	if a.Path != path {
		t.Fatalf("path %s, want %s", a.Path, path)
	}
	f := a.Field("provider")
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	var file map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	t3Format(t, file["providerInstances"][magpieID])

	os.WriteFile(path, []byte(`{"providerInstances":{"claudeAgent":{"driver":"claudeAgent","config":{"homePath":"~/.claude-t3"}}}}`), 0o600)
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(readFile(path)), &file)
	if e := t3Format(t, file["providerInstances"][magpieID]); e.Config.HomePath != "~/.claude-t3" || e.Config.BinaryPath != "" {
		t.Errorf("config: %+v", e.Config)
	}
	if _, ok := file["providerInstances"]["claudeAgent"]; !ok {
		t.Error("the user's Claude instance is gone")
	}
}

// Each magpie model in T3 Code has a Reasoning pick of the levels it takes
// that Claude Code can send, medium chosen first, else the lowest (KevinXC
// on Discord: every model ran at medium with no way to change it), and a
// model that thinks at some level a Thinking switch, on at first, as T3's
// own Claude Haiku has; neither for a model with no such level.
func TestT3CodeEfforts(t *testing.T) {
	type opt struct {
		id  string
		def bool
	}
	pick := func(id string, efforts []string) ([]opt, bool) {
		c := t3Capabilities(id, efforts)
		if c == nil {
			return nil, false
		}
		raw, _ := json.Marshal(c)
		var caps struct {
			OptionDescriptors []struct {
				ID, Label, Type string
				CurrentValue    *bool
				Options         []struct {
					ID, Label string
					IsDefault bool
				}
			}
		}
		json.Unmarshal(raw, &caps)
		var out []opt
		thinking := false
		for _, d := range caps.OptionDescriptors {
			switch {
			case d.ID == "effort" && d.Type == "select" && out == nil:
				for _, o := range d.Options {
					if o.Label == "" {
						t.Errorf("%s: no label: %s", id, raw)
					}
					out = append(out, opt{o.ID, o.IsDefault})
				}
			case d.ID == "thinking" && d.Type == "boolean" && d.Label == "Thinking" && d.CurrentValue != nil && *d.CurrentValue && !thinking:
				thinking = true
			default:
				t.Fatalf("%s: %s", id, raw)
			}
		}
		return out, thinking
	}
	for _, c := range []struct {
		id       string
		efforts  []string
		want     []opt
		thinking bool
	}{
		{"codex/gpt-6", []string{"none", "minimal", "low", "medium", "high", "xhigh"}, []opt{{"low", false}, {"medium", true}, {"high", false}, {"xhigh", false}}, true},
		{"ds/deepseek-v4", []string{"high", "max"}, []opt{{"high", true}, {"max", false}}, true},
		{"claude/claude-opus-4-6", []string{"low", "medium", "high", "xhigh", "max"}, []opt{{"low", false}, {"medium", true}, {"high", false}, {"max", false}}, true},
		{"claude/claude-haiku-4-5", []string{"low", "medium", "high"}, nil, true},
		{"glm/glm-4.6", []string{"none", "minimal"}, nil, true},
		{"glm/glm-4.6", []string{"none"}, nil, false},
		{"glm/glm-4.6", nil, nil, false},
	} {
		if got, thinking := pick(c.id, c.efforts); !slices.Equal(got, c.want) || thinking != c.thinking {
			t.Errorf("%s %v: %v thinking %v, want %v thinking %v", c.id, c.efforts, got, thinking, c.want, c.thinking)
		}
	}
}

// What the user set on magpie's instance in T3 Code's own settings stays
// through every sync (#2030, Kanscape): its name, Auto-compact after,
// launch arguments, a variable of their own and whether it is on, while
// the models and the gateway's variables stay magpie's. T3's own Claude,
// an instance beside it with a name and window of its own, neither changes
// nor lends magpie's instance anything.
func TestT3CodeKeepsTheUsersSettings(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	a := t3code(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	const claude = `{"driver":"claudeAgent","displayName":"Claude (sub)","enabled":true,"config":{"autoCompactWindow":"200000","launchArgs":"--chrome"}}`
	os.WriteFile(path, []byte(`{"providerInstances":{"claudeAgent":`+claude+`}}`), 0o600)
	f := a.Field("provider")
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]json.RawMessage {
		var file struct {
			ProviderInstances map[string]json.RawMessage `json:"providerInstances"`
		}
		if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
			t.Fatalf("%v\n%s", err, readFile(path))
		}
		return file.ProviderInstances
	}
	var mine map[string]any
	json.Unmarshal(read()[magpieID], &mine)
	if mine["displayName"] != "magpie" {
		t.Fatalf("first written as %v", mine["displayName"])
	}
	cfg := mine["config"].(map[string]any)
	if _, ok := cfg["autoCompactWindow"]; ok {
		t.Fatalf("the Claude instance's window lent to magpie's: %v", cfg)
	}

	// the user edits magpie's instance in T3 Code, as T3 saves it
	mine["displayName"] = "Magpie GW"
	mine["enabled"] = false
	mine["accentColor"] = "#ff8800"
	cfg["autoCompactWindow"] = "300000"
	cfg["launchArgs"] = "--verbose"
	cfg["customModels"] = []any{map[string]any{"slug": "gone/old", "name": "old"}}
	env := mine["environment"].([]any)
	for _, v := range env {
		if m := v.(map[string]any); m["name"] == "ANTHROPIC_BASE_URL" {
			m["value"] = "http://127.0.0.1:1"
		}
	}
	mine["environment"] = append(env, map[string]any{"name": "HTTPS_PROXY", "value": "http://proxy:8080", "sensitive": false})
	if err := edit.SetJSON(path, edit.KV{Path: t3Instance, Value: mine}); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	insts := read()
	if !sameJSON(string(insts["claudeAgent"]), json.RawMessage(claude)) {
		t.Errorf("T3's own Claude changed: %s", insts["claudeAgent"])
	}
	var got struct {
		DisplayName string `json:"displayName"`
		Enabled     *bool  `json:"enabled"`
		AccentColor string `json:"accentColor"`
		Environment []struct {
			Name, Value string
		} `json:"environment"`
		Config struct {
			AutoCompactWindow string `json:"autoCompactWindow"`
			LaunchArgs        string `json:"launchArgs"`
			CustomModels      []struct {
				Slug string `json:"slug"`
			} `json:"customModels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(insts[magpieID], &got); err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Magpie GW" || got.Enabled == nil || *got.Enabled || got.AccentColor != "#ff8800" {
		t.Errorf("the user's name, switch or colour not kept: %s", insts[magpieID])
	}
	if got.Config.AutoCompactWindow != "300000" || !strings.HasPrefix(got.Config.LaunchArgs, "--verbose --settings ") {
		t.Errorf("the user's config not kept: %+v", got.Config)
	}
	if env, _ := t3SettingsOf(got.Config.LaunchArgs).inline["env"].(map[string]any); env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3425" {
		t.Errorf("magpie's --settings not beside the user's args: %q", got.Config.LaunchArgs)
	}
	envs := map[string]string{}
	for _, v := range got.Environment {
		envs[v.Name] = v.Value
	}
	if envs["HTTPS_PROXY"] != "http://proxy:8080" || envs["ANTHROPIC_BASE_URL"] != gateway.URL() || len(got.Environment) != len(env)+1 {
		t.Errorf("environment: %v", got.Environment)
	}
	if slices.ContainsFunc(got.Config.CustomModels, func(m struct {
		Slug string `json:"slug"`
	}) bool {
		return m.Slug == "gone/old"
	}) || len(got.Config.CustomModels) == 0 {
		t.Errorf("custom models not magpie's: %+v", got.Config.CustomModels)
	}
}

// T3 Code runs Claude Code with magpie's instance env, and Claude Code puts
// its own settings' env over that, so an ANTHROPIC_BASE_URL in
// ~/.claude/settings.json (CC Switch's proxy, Billion Context's
// 127.0.0.1:18787: karissasel_ on X) took T3's requests there. Check said
// so, and Reconnect, which wrote the instance again, couldn't change it:
// the row stayed "no longer goes through magpie" while Reconnect said it
// did (jimlee0824v2 on X). Now the instance's launch arguments carry the
// gateway as flag settings, which win over the user's settings files, so
// connecting (and reconnecting an instance an older magpie wrote) leaves
// no drift, and T3 hands Claude Code the gateway.
func TestT3CodeForeignBaseURLInClaudeSettings(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	writeFile(t, path, t3User)
	settings := filepath.Join(home, ".claude", "settings.json")
	// CC Switch's own proxy, and its key
	writeFile(t, settings, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:15721","ANTHROPIC_AUTH_TOKEN":"sk-ccswitch"}}`)

	a := t3code(home)
	if err := a.Apply("provider", magpieID); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("connected over a foreign base URL, still drift: %+v", d)
	}
	// what T3 hands Claude Code as flag settings: the gateway and its key
	args, _ := edit.GetJSON(path, t3Instance+".config.launchArgs")
	s := t3SettingsOf(args)
	env, _ := s.inline["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != gateway.URL() || env["ANTHROPIC_AUTH_TOKEN"] != keyAt(gateway.URL()) {
		t.Fatalf("launch arguments %q give Claude Code %v", args, env)
	}

	// an instance an older magpie wrote, with no launch arguments: the
	// foreign base URL wins, Reconnect says it can mend it, and does
	if err := edit.DelJSON(path, t3Instance+".config.launchArgs"); err != nil {
		t.Fatal(err)
	}
	d := a.Drift()
	if d == nil || d.Kind != "unwired" || !strings.Contains(d.Detail, "127.0.0.1:15721") {
		t.Fatalf("an older instance under a foreign base URL: %+v", d)
	}
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("still drifted after Reconnect: %+v", d)
	}

	// so does a start's sync
	edit.DelJSON(path, t3Instance+".config.launchArgs")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("still drifted after a sync: %+v", d)
	}

	// the gateway itself in Claude Code's settings is magpie's
	writeFile(t, settings, `{"env":{"ANTHROPIC_BASE_URL":"`+gateway.URL()+`"}}`)
	edit.DelJSON(path, t3Instance+".config.launchArgs")
	if d := a.Drift(); d != nil {
		t.Fatalf("the gateway taken for a foreign base URL: %+v", d)
	}
}

// What setting magpie's instance again can't change is drift of its own
// kind, told how to fix, not a Reconnect that says it worked: an
// administrator's base URL wins over flag settings too, a provider switch
// takes Claude Code to its cloud whatever the base URL, and a --settings
// of the user's that names a file is left alone.
func TestT3CodeOutside(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	shell := claudeShellEnv
	claudeShellEnv = func(string) string { return "" }
	t.Cleanup(func() { claudeShellEnv = shell })
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	writeFile(t, path, t3User)
	settings := filepath.Join(home, ".claude", "settings.json")
	a := t3code(home)
	if err := a.Apply("provider", magpieID); err != nil {
		t.Fatal(err)
	}
	outside := func(what string, want ...string) {
		t.Helper()
		d := a.Drift()
		if d == nil || d.Kind != "outside" {
			t.Fatalf("%s: %+v", what, d)
		}
		for _, w := range want {
			if !strings.Contains(d.Detail, w) {
				t.Fatalf("%s: %q doesn't say %q", what, d.Detail, w)
			}
		}
		if err := a.Reapply(); err != nil {
			t.Fatal(err)
		}
		if d2 := a.Drift(); d2 == nil || d2.Kind != "outside" {
			t.Fatalf("%s: Reconnect changed it: %+v", what, d2)
		}
	}

	writeFile(t, claudeManaged(), `{"env":{"ANTHROPIC_BASE_URL":"https://corp.example"}}`)
	outside("managed base URL", "corp.example", "managed")
	os.Remove(claudeManaged())

	writeFile(t, settings, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`)
	outside("a provider switch", "CLAUDE_CODE_USE_BEDROCK", "Amazon Bedrock")

	// the user's own --settings, a file: magpie's can't go there, so a
	// foreign base URL in Claude Code's settings is theirs to take out
	writeFile(t, settings, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:15721"}}`)
	if err := edit.SetJSON(path, edit.KV{Path: t3Instance + ".config.launchArgs", Value: "--settings ~/my-claude.json --add-dir /work"}); err != nil {
		t.Fatal(err)
	}
	outside("own --settings file", "127.0.0.1:15721")
	if args, _ := edit.GetJSON(path, t3Instance+".config.launchArgs"); args != "--settings ~/my-claude.json --add-dir /work" {
		t.Fatalf("the user's own --settings rewritten: %q", args)
	}
	// with Claude Code's settings routed nowhere, nothing is off
	os.Remove(settings)
	if d := a.Drift(); d != nil {
		t.Fatalf("own --settings, nothing routed: %+v", d)
	}
}

// The launch arguments the user gave magpie's instance stay theirs: the
// gateway goes into an inline --settings of theirs beside what it holds,
// or after their arguments, and T3 Code's tokenizer reads magpie's as
// written.
func TestT3ClaudeArgs(t *testing.T) {
	syncHome(t)
	gw := "http://127.0.0.1:3425"
	key := keyAt(gw)
	for _, c := range []struct{ in, keep string }{
		{"", ""},
		{"--add-dir /work", "--add-dir /work"},
		{`--append-system-prompt "be brief" --model x`, `--append-system-prompt "be brief" --model x`},
		{`--settings "{\"permissions\":{\"allow\":[\"Bash(ls)\"]},\"env\":{\"FOO\":\"it's\"}}" --add-dir /w`, "--add-dir /w"},
		{`--settings='{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9"}}'`, ""},
		{`--model x --settings '{}' --`, "--model x"},
		// the value of --append-system-prompt, not a flag
		{`--append-system-prompt --settings`, `--append-system-prompt --settings`},
	} {
		out, ok := t3ClaudeArgs(c.in, gw)
		if !ok {
			t.Fatalf("%q: magpie's not added", c.in)
		}
		if !strings.Contains(out, c.keep) {
			t.Fatalf("%q -> %q lost %q", c.in, out, c.keep)
		}
		s := t3SettingsOf(out)
		env, _ := s.inline["env"].(map[string]any)
		if env["ANTHROPIC_BASE_URL"] != gw || env["ANTHROPIC_AUTH_TOKEN"] != key {
			t.Fatalf("%q -> %q reads as %v", c.in, out, s.inline)
		}
		if strings.Contains(c.in, "permissions") && (env["FOO"] != "it's" || s.inline["permissions"] == nil) {
			t.Fatalf("%q -> %q lost the user's settings: %v", c.in, out, s.inline)
		}
		// written again, it stays as it is
		if again, _ := t3ClaudeArgs(out, gw); again != out {
			t.Fatalf("%q then %q", out, again)
		}
	}
	// a --settings naming a file is the user's
	if out, ok := t3ClaudeArgs("--settings ./s.json", gw); ok || out != "--settings ./s.json" {
		t.Fatalf("a settings file: %q %v", out, ok)
	}
}
