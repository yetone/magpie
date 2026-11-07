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

// ombEngine is OpenMausBot's instanceConfigSchema (server/config.ts) with
// the claudeAgent config its Claude endpoints have; nothing else taken.
type ombEngine struct {
	Driver      string            `json:"driver"`
	DisplayName string            `json:"displayName"`
	Access      string            `json:"access"`
	Environment map[string]string `json:"environment"`
	Config      struct {
		CLI            string   `json:"cli"`
		ConfigDir      string   `json:"configDir"`
		RequireAPIKey  bool     `json:"requireApiKey"`
		EndpointModels []string `json:"endpointModels"`
	} `json:"config"`
}

// ombFormat decodes magpie's engine strictly as OpenMausBot reads a Claude
// endpoint: an instance id of its pattern, an absolute Claude home, every
// model one CLAUDE_MODEL_ID takes, the endpoint's environment.
func ombFormat(t *testing.T, raw []byte) ombEngine {
	t.Helper()
	var e ombEngine
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		t.Fatalf("not OpenMausBot's engine: %v\n%s", err, raw)
	}
	if !regexp.MustCompile(`^[\w.-]+$`).MatchString(magpieID) {
		t.Errorf("instance id %q", magpieID)
	}
	if e.Driver != "claudeAgent" || e.Access != "custom" || !e.Config.RequireAPIKey || !filepath.IsAbs(e.Config.ConfigDir) {
		t.Errorf("envelope: %s", raw)
	}
	if len(e.Config.EndpointModels) == 0 {
		t.Errorf("no models: %s", raw)
	}
	for _, m := range e.Config.EndpointModels {
		if !regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._:/-]*(?:\[1m\])?$`).MatchString(m) {
			t.Errorf("a model OpenMausBot refuses the engine for: %q", m)
		}
	}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"} {
		if e.Environment[k] == "" {
			t.Errorf("%s not set: %s", k, raw)
		}
	}
	return e
}

func ombRead(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var file struct {
		Instances map[string]json.RawMessage `json:"instances"`
	}
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	return file.Instances
}

// ombRelay gives magpie a relay with a model OpenMausBot would refuse.
func ombRelay(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "gpt-5.5", "glm 4.6 (beta)"}}); err != nil {
		t.Fatal(err)
	}
}

// OpenMausBot (a user on X) gets magpie as an engine of its own, written
// as its own Claude endpoints are; a file with no engines saved gets the
// default fleet beside it, so no bot loses its engine, and off gives the
// file back with none.
func TestOpenMausBotEngine(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	dir := filepath.Join(home, ".openmausbot")
	path := filepath.Join(dir, "config.json")
	a := openMausBot(home)
	if a.Path != path || a.Dir != dir {
		t.Fatalf("path %s", a.Path)
	}
	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == "openmausbot" && x.Name == "OpenMausBot" }) {
		t.Fatal("OpenMausBot is not among the agents")
	}
	if a.Detected() && !macBundle(ombApp) {
		t.Fatal("found with no ~/.openmausbot")
	}
	const user = `{"language":"de","threads":{"maxConcurrentPerBot":3}}`
	writeFile(t, path, user)
	if !a.Detected() {
		t.Fatal("not found with ~/.openmausbot")
	}
	f := a.Field("provider")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if f.Get() != magpieID {
		t.Fatalf("get: %q", f.Get())
	}
	var file map[string]json.RawMessage
	json.Unmarshal([]byte(readFile(path)), &file)
	if string(file["language"]) != `"de"` || !sameJSON(string(file["threads"]), json.RawMessage(`{"maxConcurrentPerBot":3}`)) {
		t.Errorf("OpenMausBot's settings changed: %s", readFile(path))
	}
	insts := ombRead(t, path)
	e := ombFormat(t, insts[magpieID])
	if e.Environment["ANTHROPIC_BASE_URL"] != gateway.URL() || e.Environment["ANTHROPIC_AUTH_TOKEN"] != gateway.Token || e.Environment["ANTHROPIC_API_KEY"] != gateway.Token {
		t.Errorf("environment: %v", e.Environment)
	}
	if e.Config.ConfigDir != filepath.Join(dir, "providers", magpieID) {
		t.Errorf("Claude home %s", e.Config.ConfigDir)
	}
	if !slices.Contains(e.Config.EndpointModels, "relay/glm-4.6") || e.Environment["ANTHROPIC_MODEL"] != e.Config.EndpointModels[0] {
		t.Errorf("models: %v, picked %s", e.Config.EndpointModels, e.Environment["ANTHROPIC_MODEL"])
	}
	// every engine of the default fleet, as OpenMausBot runs it with none
	var fleet map[string]json.RawMessage
	json.Unmarshal([]byte(ombFleet), &fleet)
	if len(fleet) != 27 {
		t.Errorf("default fleet of %d", len(fleet))
	}
	for id, v := range fleet {
		if !sameJSON(string(insts[id]), v) {
			t.Errorf("%s: %s, want %s", id, insts[id], v)
		}
	}
	if len(insts) != len(fleet)+1 {
		t.Errorf("%d engines", len(insts))
	}
	if why := a.Check(); why != "" {
		t.Errorf("check: %s", why)
	}

	// a sync with nothing to change writes nothing
	st, _ := os.Stat(path)
	before := readFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); readFile(path) != before || !after.ModTime().Equal(st.ModTime()) {
		t.Error("a sync with nothing to change wrote the file")
	}

	// moved off the gateway, Check says so, and a sync brings it back
	edit.SetJSON(path, edit.KV{Path: ombInstance + ".environment.ANTHROPIC_BASE_URL", Value: "http://127.0.0.1:1"})
	if why := a.Check(); !strings.Contains(why, "ANTHROPIC_BASE_URL") {
		t.Errorf("check: %q", why)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if why := a.Check(); why != "" {
		t.Errorf("check after sync: %s", why)
	}

	// off: the fleet magpie wrote goes with it
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(user)) {
		t.Errorf("off left %s", readFile(path))
	}
	if f.Get() != "" {
		t.Errorf("still on: %q", f.Get())
	}
	// switched off, a sync writes nothing
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(readFile(path), json.RawMessage(user)) {
		t.Errorf("a sync while off wrote %s", readFile(path))
	}
}

// A fleet of the user's own is theirs: only magpie's engine is added, it
// runs the Claude Code they gave OpenMausBot, and off takes only it out.
// What they change on magpie's engine in OpenMausBot stays through a sync,
// but for the routing, which is magpie's.
func TestOpenMausBotKeepsTheUsers(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	path := filepath.Join(home, ".openmausbot", "config.json")
	const claude = `{"driver":"claudeAgent","config":{"cli":"/opt/claude/bin/claude"}}`
	const work = `{"driver":"claudeAgent","displayName":"Work","config":{"configDir":"~/.claude-work"}}`
	writeFile(t, path, `{"instances":{"claude":`+claude+`,"claudeWork":`+work+`,"codex":{"driver":"codex"}}}`)
	a := openMausBot(home)
	f := a.Field("provider")
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	insts := ombRead(t, path)
	if len(insts) != 4 || !sameJSON(string(insts["claude"]), json.RawMessage(claude)) || !sameJSON(string(insts["claudeWork"]), json.RawMessage(work)) {
		t.Fatalf("the user's engines changed: %s", readFile(path))
	}
	e := ombFormat(t, insts[magpieID])
	if e.Config.CLI != "/opt/claude/bin/claude" {
		t.Errorf("the user's Claude Code not carried over: %+v", e.Config)
	}

	// the user edits magpie's engine in OpenMausBot
	var mine map[string]any
	json.Unmarshal(insts[magpieID], &mine)
	mine["displayName"] = "Gateway"
	mine["hidden"] = true
	mine["accentColor"] = "#ff8800"
	mine["config"].(map[string]any)["permissionMode"] = "auto"
	mine["config"].(map[string]any)["endpointModels"] = []any{"gone/old"}
	env := mine["environment"].(map[string]any)
	env["HTTPS_PROXY"] = "http://proxy:8080"
	env["CLAUDE_CODE_USE_BEDROCK"] = "1"
	env["ANTHROPIC_AUTH_TOKEN"] = "sk-elsewhere"
	env["ANTHROPIC_MODEL"] = "gone/old"
	edit.SetJSON(path, edit.KV{Path: ombInstance, Value: mine})
	for range 2 {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		ombEngine
		Hidden      bool   `json:"hidden"`
		AccentColor string `json:"accentColor"`
		Config      struct {
			PermissionMode string   `json:"permissionMode"`
			EndpointModels []string `json:"endpointModels"`
		} `json:"config"`
	}
	insts = ombRead(t, path)
	json.Unmarshal(insts[magpieID], &got)
	if got.DisplayName != "Gateway" || !got.Hidden || got.AccentColor != "#ff8800" || got.Config.PermissionMode != "auto" {
		t.Errorf("the user's settings not kept: %s", insts[magpieID])
	}
	if got.Environment["HTTPS_PROXY"] != "http://proxy:8080" {
		t.Errorf("the user's environment not kept: %v", got.Environment)
	}
	if _, ok := got.Environment["CLAUDE_CODE_USE_BEDROCK"]; ok || got.Environment["ANTHROPIC_AUTH_TOKEN"] != gateway.Token {
		t.Errorf("the routing is the user's: %v", got.Environment)
	}
	if slices.Contains(got.Config.EndpointModels, "gone/old") || got.Environment["ANTHROPIC_MODEL"] != got.Config.EndpointModels[0] {
		t.Errorf("models %v, picked %s", got.Config.EndpointModels, got.Environment["ANTHROPIC_MODEL"])
	}
	// a pick of one of magpie's models is the user's
	if len(got.Config.EndpointModels) > 1 {
		pick := got.Config.EndpointModels[1]
		edit.SetJSON(path, edit.KV{Path: ombInstance + ".environment.ANTHROPIC_MODEL", Value: pick})
		a.Sync()
		if v, _ := edit.GetJSON(path, ombInstance+".environment.ANTHROPIC_MODEL"); v != pick {
			t.Errorf("the user's pick %s became %s", pick, v)
		}
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	insts = ombRead(t, path)
	if _, ok := insts[magpieID]; ok || len(insts) != 3 || !sameJSON(string(insts["claude"]), json.RawMessage(claude)) {
		t.Errorf("off: %s", readFile(path))
	}
}

// A default fleet magpie wrote that the user has changed since is theirs:
// off takes magpie's engine out and leaves the rest.
func TestOpenMausBotFleetChanged(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	path := filepath.Join(home, ".openmausbot", "config.json")
	a := openMausBot(home)
	f := a.Field("provider")
	// no file yet: the app installed, never opened
	if err := f.Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if insts := ombRead(t, path); len(insts) != 28 {
		t.Fatalf("%d engines", len(insts))
	}
	edit.SetJSON(path, edit.KV{Path: "instances.grok.hidden", Value: true})
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	insts := ombRead(t, path)
	if _, ok := insts[magpieID]; ok || len(insts) != 27 || !strings.Contains(string(insts["grok"]), "hidden") {
		t.Errorf("off: %s", readFile(path))
	}
}

// A change is refused while OpenMausBot runs, as it writes its engines
// back over magpie's; a file whose instances isn't a map of engines is
// left alone.
func TestOpenMausBotRefuses(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	path := filepath.Join(home, ".openmausbot", "config.json")
	writeFile(t, path, `{"language":"en"}`)
	was := ombRunning
	ombRunning = func() bool { return true }
	t.Cleanup(func() { ombRunning = was })
	f := openMausBot(home).Field("provider")
	if err := f.Set(magpieID); err != errOMBRunning {
		t.Fatalf("set while running: %v", err)
	}
	if readFile(path) != `{"language":"en"}` {
		t.Errorf("written while running: %s", readFile(path))
	}
	ombRunning = func() bool { return false }
	writeFile(t, path, `{"instances":[1]}`)
	if err := f.Set(magpieID); err == nil || readFile(path) != `{"instances":[1]}` {
		t.Errorf("a list of instances: %v %s", err, readFile(path))
	}
}

// The data folder is $OMB_DATA_DIR when it is absolute, as OpenMausBot
// takes it, else ~/.openmausbot.
func TestOpenMausBotDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OMB_DATA_DIR", "")
	if d := ombDir(home); d != filepath.Join(home, ".openmausbot") {
		t.Errorf("default %s", d)
	}
	other := filepath.Join(home, "omb")
	t.Setenv("OMB_DATA_DIR", other)
	if d := ombDir(home); d != other {
		t.Errorf("OMB_DATA_DIR: %s", d)
	}
	if p := openMausBot(home).Path; p != filepath.Join(other, "config.json") {
		t.Errorf("path %s", p)
	}
	t.Setenv("OMB_DATA_DIR", "rel/omb")
	if d := ombDir(home); d != filepath.Join(home, ".openmausbot") {
		t.Errorf("relative: %s", d)
	}
}

// OpenMausBot refuses an engine whose models list one id its pattern
// doesn't take, so magpie leaves such a model out.
func TestOpenMausBotModelIDs(t *testing.T) {
	for _, id := range []string{"relay/glm-4.6", "claude-opus-4-7[1m]", "or/x-ai/grok-4:free"} {
		if !ombModelID.MatchString(id) {
			t.Errorf("refused %q", id)
		}
	}
	for _, id := range []string{"relay/glm 4.6", "-x", "a@b", "m+1", ""} {
		if ombModelID.MatchString(id) {
			t.Errorf("taken %q", id)
		}
	}
}

func TestOpenMausBotProcess(t *testing.T) {
	match := func(cmd string) bool {
		for _, p := range ombProc {
			if regexp.MustCompile(p).MatchString(cmd) {
				return true
			}
		}
		return false
	}
	for _, cmd := range []string{
		"/Applications/OpenMausBot.app/Contents/MacOS/OpenMausBot",
		"/opt/OpenMausBot/openmausbot --no-sandbox",
		"/tmp/.mount_OpenMaXyz/openmausbot",
	} {
		if !match(cmd) {
			t.Errorf("not OpenMausBot: %s", cmd)
		}
	}
	for _, cmd := range []string{"vim /Users/u/.openmausbot/config.json", "/usr/bin/openmausbotd", "grep openmausbot", "/Users/u/.openmausbot/x"} {
		if match(cmd) {
			t.Errorf("taken for OpenMausBot: %s", cmd)
		}
	}
}
