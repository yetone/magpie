package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudeOffHome is a sandbox HOME with a provider serving Claude models,
// as the user's Claude subscription serves claude/claude-opus-5-5, and
// Claude Code's settings.json holding before.
func claudeOffHome(t *testing.T, before string) (*Agent, string) {
	t.Helper()
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k",
		Models: []string{"claude-opus-5-5", "claude-haiku-4-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, before)
	return claude(home), path
}

// claudeStartsOn is every model a fresh Claude Code started on settings.json
// at path asks its endpoint for, as Claude Code 2.1.316 reads them: its
// main model (ANTHROPIC_MODEL over settings.json's model, pG/Wg in its
// bundle), each alias's tier, the small fast and subagent models and the
// auto mode classifier's. With no ANTHROPIC_BASE_URL that endpoint is
// Anthropic's, which answers a provider/model name with a 404 Claude Code
// shows as "There's an issue with the selected model (…)".
func claudeStartsOn(t *testing.T, path string) (base string, models map[string]string) {
	t.Helper()
	var s struct {
		Model string            `json:"model"`
		Env   map[string]string `json:"env"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	models = map[string]string{"main": s.Env["ANTHROPIC_MODEL"]}
	if models["main"] == "" {
		models["main"] = s.Model
	}
	for _, k := range claudeModelEnv {
		if k != "ANTHROPIC_MODEL" {
			models[k] = s.Env[k]
		}
	}
	return s.Env["ANTHROPIC_BASE_URL"], models
}

// noMagpieOnAnthropic fails when Claude Code, on Anthropic's own endpoint,
// would ask for one of magpie's provider/model names.
func noMagpieOnAnthropic(t *testing.T, path, when string) {
	t.Helper()
	base, models := claudeStartsOn(t, path)
	if base != "" {
		t.Fatalf("%s: ANTHROPIC_BASE_URL is %q, want Anthropic's own\n%s", when, base, readFile(path))
	}
	for k, v := range models {
		if strings.Contains(v, "/") {
			t.Fatalf("%s: Claude Code on Anthropic's endpoint starts with %s = %q\n%s", when, k, v, readFile(path))
		}
	}
}

// roswellcsy on X (v0.1.2846): Claude Code switched off magpie, on its own
// sign-in, and every session said "There's an issue with the selected
// model (claude/claude-opus-5-5)" while settings.json's model was the
// bare claude-opus-5-5. A model name of magpie's left in Claude Code's env
// outranks or stands in for that model, and Anthropic has no such model.
// None is ever left there, given back from the stash, or kept as the
// user's own; one already there goes when magpie next looks.
func TestClaudeOffLeavesNoMagpieModel(t *testing.T) {
	// what the user's settings.json held: the bare model, and magpie's
	// names in the env with no endpoint of magpie's beside them
	const stray = `{"model": "claude-opus-5-5", "env": {
		"ANTHROPIC_MODEL": "relay/claude-opus-5-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "relay/claude-opus-5-5[1m]",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "relay/claude-haiku-4-5:low",
		"ANTHROPIC_SMALL_FAST_MODEL": "relay/claude-haiku-4-5",
		"CLAUDE_CODE_SUBAGENT_MODEL": "relay/claude-opus-5-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-6",
		"MY_OWN": "x"}}`

	t.Run("magpie's start takes them out", func(t *testing.T) {
		a, path := claudeOffHome(t, stray)
		SyncCatalog()
		noMagpieOnAnthropic(t, path, "after a start")
		get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
		// the user's own stay as they are
		if get("model") != "claude-opus-5-5" || get("env.ANTHROPIC_DEFAULT_SONNET_MODEL") != "claude-sonnet-4-6" || get("env.MY_OWN") != "x" {
			t.Fatalf("the user's own values went:\n%s", readFile(path))
		}
		if a.Wired() {
			t.Fatalf("taking magpie's names out connected Claude Code:\n%s", readFile(path))
		}
	})

	t.Run("connected and switched off again", func(t *testing.T) {
		a, path := claudeOffHome(t, stray)
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		if !a.Wired() {
			t.Fatalf("not connected:\n%s", readFile(path))
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		noMagpieOnAnthropic(t, path, "after Disconnect")
		// and again: nothing of magpie's was kept to come back
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		noMagpieOnAnthropic(t, path, "after a second Disconnect")
	})

	t.Run("kept by an older magpie", func(t *testing.T) {
		// a stash an older magpie wrote, holding magpie's names as the
		// user's own: never given back
		a, path := claudeOffHome(t, `{"model": "claude-opus-5-5"}`)
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
		at := here(os.Getenv("HOME"))
		stash(map[string]string{
			at.key("claude.env.ANTHROPIC_MODEL"):              "relay/claude-opus-5-5",
			at.key("claude.env.ANTHROPIC_DEFAULT_OPUS_MODEL"): "relay/claude-opus-5-5[1m]",
			at.key("claude.env.CLAUDE_CODE_SUBAGENT_MODEL"):   "relay/claude-opus-5-5:high",
		})
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		noMagpieOnAnthropic(t, path, "after Disconnect")
	})

	t.Run("the user's own model comes back", func(t *testing.T) {
		a, path := claudeOffHome(t, `{"model": "claude-opus-5-5", "env": {"ANTHROPIC_MODEL": "claude-opus-5-5", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5"}}`)
		if err := a.Field("model").Set("relay/claude-opus-5-5"); err != nil {
			t.Fatal(err)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
		if get("env.ANTHROPIC_MODEL") != "claude-opus-5-5" || get("env.ANTHROPIC_DEFAULT_HAIKU_MODEL") != "claude-haiku-4-5" {
			t.Fatalf("the user's own models not back:\n%s", readFile(path))
		}
	})

	t.Run("an older gateway address", func(t *testing.T) {
		// magpie's wiring at an address it no longer serves (a port
		// changed): its key, so magpie's, and all of it goes
		a, path := claudeOffHome(t, `{"model": "relay/claude-opus-5-5", "env": {
			"ANTHROPIC_BASE_URL": "http://127.0.0.1:1",
			"ANTHROPIC_AUTH_TOKEN": "`+gateway.Token+`",
			"ANTHROPIC_DEFAULT_OPUS_MODEL": "relay/claude-opus-5-5",
			"ANTHROPIC_SMALL_FAST_MODEL": "relay/claude-haiku-4-5"}}`)
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		noMagpieOnAnthropic(t, path, "after Disconnect")
		if v, has := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); has {
			t.Fatalf("magpie's key left: %q\n%s", v, readFile(path))
		}
	})

	t.Run("another endpoint's names are its own", func(t *testing.T) {
		// a relay of the user's own may name its models so: magpie
		// leaves them
		const own = `{"env": {"ANTHROPIC_BASE_URL": "https://mine.example", "ANTHROPIC_AUTH_TOKEN": "sk-mine", "ANTHROPIC_MODEL": "relay/claude-opus-5-5"}}`
		_, path := claudeOffHome(t, own)
		SyncCatalog()
		if v, _ := edit.GetJSON(path, "env.ANTHROPIC_MODEL"); v != "relay/claude-opus-5-5" {
			t.Fatalf("a model of the user's own endpoint went: %q\n%s", v, readFile(path))
		}
	})
}
