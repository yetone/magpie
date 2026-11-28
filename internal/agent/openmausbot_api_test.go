package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// ombHasClaude says whether OpenMausBot has a claude CLI to run, for the
// rest of the test.
func ombHasClaude(t *testing.T, yes bool) {
	t.Helper()
	was := ombClaudeCLI
	ombClaudeCLI = func(string, string) bool { return yes }
	t.Cleanup(func() { ombClaudeCLI = was })
}

// ombAPIEngine is OpenMausBot's instanceConfigSchema (server/config.ts)
// with the config its OpenAI-compatible driver decodes
// (server/drivers/openai-compat.ts's decodeConfig) that magpie writes.
type ombAPIEngine struct {
	Driver      string `json:"driver"`
	DisplayName string `json:"displayName"`
	Access      string `json:"access"`
	Config      struct {
		URL           string   `json:"url"`
		Key           string   `json:"key"`
		ManagedModels []string `json:"managedModels"`
	} `json:"config"`
}

// ombAPIFormat decodes magpie's engine strictly as OpenMausBot reads an
// OpenAI-compatible one: its key, the gateway's /v1, and a list of models
// decodeConfig takes (none blank, at least one).
func ombAPIFormat(t *testing.T, raw []byte) ombAPIEngine {
	t.Helper()
	var e ombAPIEngine
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		t.Fatalf("not OpenMausBot's OpenAI-compatible engine: %v\n%s", err, raw)
	}
	if e.Driver != "openai-compat" || e.Access != "custom" {
		t.Errorf("envelope: %s", raw)
	}
	if e.Config.URL != gateway.URL()+"/v1" || e.Config.Key != gateway.TokenFor("openmausbot") {
		t.Errorf("not on magpie's gateway under OpenMausBot's key: %s", raw)
	}
	if len(e.Config.ManagedModels) == 0 || slices.ContainsFunc(e.Config.ManagedModels, func(m string) bool { return strings.TrimSpace(m) == "" }) {
		t.Errorf("models OpenMausBot refuses: %q", e.Config.ManagedModels)
	}
	return e
}

// With no claude CLI on the computer, magpie's engine in OpenMausBot was
// a Claude endpoint that listed magpie's models and answered "claude CLI
// not found", with an install button that needs Node's npm (KorwinTeo on
// X). It is OpenMausBot's own OpenAI-compatible engine now, which calls
// the gateway itself.
func TestOpenMausBotWithoutClaudeCode(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	ombHasClaude(t, false)
	dir := filepath.Join(home, ".openmausbot")
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"language":"zh"}`)
	a := openMausBot(home)
	f := a.Field("provider")
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	e := ombAPIFormat(t, ombRead(t, path)[magpieID])
	if !slices.Contains(e.Config.ManagedModels, "relay/glm-4.6") {
		t.Errorf("models: %q", e.Config.ManagedModels)
	}
	if e.DisplayName != "magpie" {
		t.Errorf("name %q", e.DisplayName)
	}
	if why := a.Check(); why != "" {
		t.Errorf("check: %s", why)
	}
	if !strings.Contains(ombPart(path), gateway.URL()+"/v1") || !strings.Contains(ombPart(path), "relay/glm-4.6") {
		t.Errorf("what a Restart compares: %q", ombPart(path))
	}

	// a sync with nothing to change writes nothing
	before := readFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != before {
		t.Errorf("a sync with nothing to change wrote %s", readFile(path))
	}

	// moved off the gateway, Check says so, and a sync brings it back
	edit.SetJSON(path, edit.KV{Path: ombInstance + ".config.url", Value: "http://127.0.0.1:1/v1"})
	if why := a.Check(); !strings.Contains(why, "url") {
		t.Errorf("check: %q", why)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if why := a.Check(); why != "" {
		t.Errorf("check after sync: %s", why)
	}

	// OpenMausBot's own settings of the engine stay; the list is magpie's
	edit.SetJSON(path, edit.KV{Path: ombInstance + ".config.replyWaitMinutes", Value: 20})
	edit.SetJSON(path, edit.KV{Path: ombInstance + ".config.managedModels", Value: []string{"gone/old"}})
	edit.SetJSON(path, edit.KV{Path: ombInstance + ".displayName", Value: "Gateway"})
	if !ombListStale(path, dir) {
		t.Error("a list OpenMausBot wrote over isn't stale")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, ombInstance+".config.replyWaitMinutes"); v != "20" {
		t.Errorf("the user's wait: %q", v)
	}
	if v, _ := edit.GetJSON(path, ombInstance+".displayName"); v != "Gateway" {
		t.Errorf("the user's name: %q", v)
	}
	if got := ombSaved(path); slices.Contains(got, "gone/old") || !slices.Contains(got, "relay/glm-4.6") {
		t.Errorf("models: %q", got)
	}

	// Claude Code installed since: the engine stays as it is
	ombHasClaude(t, true)
	before = readFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != before || ombListStale(path, dir) {
		t.Errorf("moved back to Claude Code: %s", readFile(path))
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(`{"language":"zh"}`)) {
		t.Errorf("off left %s", readFile(path))
	}
}

// The reporter's engine, a Claude endpoint an older magpie wrote, is
// written over as OpenAI-compatible by the round that runs while magpie
// serves, with no change of models to set it off; what the user named,
// coloured and hid stays, and so do their other engines.
func TestOpenMausBotClaudeEndpointWithoutClaudeCode(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	dir := filepath.Join(home, ".openmausbot")
	path := filepath.Join(dir, "config.json")
	t.Setenv("OMB_DATA_DIR", dir) // the round reads $HOME, stashHome's now
	writeFile(t, path, `{"instances":{"codex":{"driver":"codex"}}}`)
	ombHasClaude(t, true)
	a := openMausBot(home)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	old := ombRead(t, path)[magpieID]
	ombFormat(t, old) // a Claude endpoint, as magpie wrote it before
	var mine map[string]any
	json.Unmarshal(old, &mine)
	mine["displayName"] = "Gateway"
	mine["hidden"] = true
	mine["accentColor"] = "#ff8800"
	edit.SetJSON(path, edit.KV{Path: ombInstance, Value: mine})
	if err := ombListedOnce(); err != nil {
		t.Fatal(err)
	}
	if ombDriver(path) != "claudeAgent" {
		t.Fatal("a Claude endpoint OpenMausBot can run was written over")
	}

	ombHasClaude(t, false)
	if err := ombListedOnce(); err != nil {
		t.Fatal(err)
	}
	insts := ombRead(t, path)
	var got struct {
		ombAPIEngine
		Hidden      bool   `json:"hidden"`
		AccentColor string `json:"accentColor"`
	}
	d := json.NewDecoder(bytes.NewReader(insts[magpieID]))
	d.DisallowUnknownFields() // nothing of the Claude endpoint's is left
	if err := d.Decode(&got); err != nil {
		t.Fatalf("%v\n%s", err, insts[magpieID])
	}
	if got.Driver != "openai-compat" || got.Config.URL != gateway.URL()+"/v1" || got.Config.Key != gateway.TokenFor("openmausbot") || len(got.Config.ManagedModels) == 0 {
		t.Errorf("not OpenAI-compatible on the gateway: %s", insts[magpieID])
	}
	if got.DisplayName != "Gateway" || !got.Hidden || got.AccentColor != "#ff8800" {
		t.Errorf("the user's settings not kept: %s", insts[magpieID])
	}
	if !sameJSON(string(insts["codex"]), json.RawMessage(`{"driver":"codex"}`)) || len(insts) != 2 {
		t.Errorf("the user's engines changed: %s", readFile(path))
	}
	if why := a.Check(); why != "" {
		t.Errorf("check: %s", why)
	}
}

// OpenMausBot runs a claudeAgent engine with the claude it finds: one a
// Claude engine of the user's names, its own copy in its data folder,
// one on PATH or in a folder it adds to PATH (server/env-path.ts).
func TestOpenMausBotFindsClaudeCode(t *testing.T) {
	home := syncHome(t)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".openmausbot")
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"instances":{"codex":{"driver":"codex"}}}`)
	has := func() bool { proc.Forget(); return ombClaudeCLI(path, dir) }
	if has() {
		t.Fatal("found a claude CLI on a computer with none")
	}
	npm := filepath.Join(dir, "tools", "npm")
	if runtime.GOOS != "windows" {
		npm = filepath.Join(npm, "bin")
	}
	for _, at := range []string{
		filepath.Join(npm, exe("claude")),
		filepath.Join(home, ".local", "bin", exe("claude")),
		filepath.Join(home, ".claude", "local", exe("claude")),
	} {
		testenv.Program(t, at, "#!/bin/sh\nexit 0\n")
		if !has() {
			t.Errorf("not found at %s", at)
		}
		os.Remove(at)
		if has() {
			t.Fatalf("still found after %s was removed", at)
		}
	}
	// the user's Claude engine names its binary: found while it is there
	cli := filepath.Join(home, "opt", "claude", exe("claude"))
	writeFile(t, path, `{"instances":{"claude":{"driver":"claudeAgent","config":{"cli":`+jsonString(cli)+`}}}}`)
	if has() {
		t.Error("a cli that isn't there taken for one")
	}
	testenv.Program(t, cli, "#!/bin/sh\nexit 0\n")
	if !has() {
		t.Error("the user's own cli not found")
	}
}
