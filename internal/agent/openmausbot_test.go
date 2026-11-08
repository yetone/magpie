package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
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

// ombAntigravity signs magpie in to Antigravity with the reporter's
// models (maxleebeta on Discord, 11-08): Gemini 3.8 Flash's two efforts,
// one 1M family model, and two Claude models.
func ombAntigravity(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie")
	writeFile(t, filepath.Join(cfg, "logins.json"), `[{"agent":"antigravity","user":"u@example.com","seen":"2026-09-25T08:45:36Z","on":true,"auth":{"refresh_token":"r","access_token":"a","expiry_date":99999999999999}}]`)
	if err := catalog.SaveLive("antigravity", "", []catalog.Model{
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)", Images: true, Context: 1048576},
		{ID: "gemini-3.8-flash-low", Name: "Gemini 3.8 Flash (Low)", Images: true, Context: 1048576},
		{ID: "claude-sonnet-4-6-thinking", Name: "Claude Sonnet 4.6 (Thinking)", Images: true},
		{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 (Thinking)", Images: true},
	}); err != nil {
		t.Fatal(err)
	}
}

// ombSaves writes magpie's engine at path as OpenMausBot saves its fleet,
// JSON.stringify(cfg, null, 2), with engine in magpie's place.
func ombSaves(t *testing.T, path string, engine json.RawMessage) {
	t.Helper()
	var file map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
		t.Fatal(err)
	}
	var e any
	if err := json.Unmarshal(engine, &e); err != nil {
		t.Fatal(err)
	}
	file["instances"].(map[string]any)[magpieID] = e
	b, _ := json.MarshalIndent(file, "", "  ")
	writeFile(t, path, string(b))
}

// The models picked in magpie stay in OpenMausBot's picker, and so in its
// search (maxleebeta on Discord, 11-08: "3.8" found nothing). OpenMausBot
// writes back the engine it read at start when an engine is changed in
// its Settings, and an edit of magpie's engine there keeps only the model
// picked; no catalog change follows, so before KeepOpenMausBotListed
// nothing wrote magpie's list again.
func TestOpenMausBotListWrittenAgain(t *testing.T) {
	home := syncHome(t)
	stashHome(t)
	ombRelay(t)
	a := openMausBot(home)
	path := a.Path
	// the round reads $HOME, stashHome's now: point it at this file
	t.Setenv("OMB_DATA_DIR", filepath.Dir(path))
	writeFile(t, path, `{"instances":{"claude":{"driver":"claudeAgent"}}}`)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	// the engine as OpenMausBot read it when it started, the user's model
	// picked in it
	var held map[string]any
	json.Unmarshal(ombRead(t, path)[magpieID], &held)
	held["environment"].(map[string]any)["ANTHROPIC_MODEL"] = "relay/gpt-5.5"
	heldRaw, _ := json.Marshal(held)

	ombAntigravity(t)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	const gemini = "antigravity/gemini-3.8-flash[1m]"
	if !slices.Contains(ombSaved(path), gemini) {
		t.Fatalf("magpie's sync didn't list Antigravity's models: %q", ombSaved(path))
	}
	full := ombSaved(path)

	for _, c := range []struct {
		name   string
		engine json.RawMessage
	}{
		// the fleet it held, written back over magpie's list
		{"written back", heldRaw},
		// Edit endpoint in its Settings: endpointModels is the model alone
		{"edited there", func() json.RawMessage {
			var e map[string]any
			json.Unmarshal(heldRaw, &e)
			e["config"].(map[string]any)["endpointModels"] = []string{"relay/gpt-5.5"}
			b, _ := json.Marshal(e)
			return b
		}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			ombSaves(t, path, c.engine)
			if slices.Contains(ombSaved(path), gemini) {
				t.Fatal("the stale engine lists Gemini 3.8 Flash; the test proves nothing")
			}
			if err := ombListedOnce(); err != nil {
				t.Fatal(err)
			}
			if got := ombSaved(path); !slices.Equal(got, full) {
				t.Fatalf("listed %q, want magpie's %q", got, full)
			}
			e := ombFormat(t, ombRead(t, path)[magpieID])
			if e.Environment["ANTHROPIC_MODEL"] != "relay/gpt-5.5" {
				t.Fatalf("the model picked in OpenMausBot was replaced: %q", e.Environment["ANTHROPIC_MODEL"])
			}
			if _, ok := ombRead(t, path)["claude"]; !ok {
				t.Fatal("OpenMausBot's other engine is gone")
			}
		})
	}

	// a round with the list as magpie's writes nothing
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(path, old, old)
	before := readFile(path)
	if err := ombListedOnce(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); !st.ModTime().Equal(old) || readFile(path) != before {
		t.Fatal("a round rewrote a list already magpie's")
	}

	// while magpie serves, the loop writes it again
	ombSaves(t, path, heldRaw)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { keepOMBListed(ctx, 10*time.Millisecond); close(done) }()
	for end := time.Now().Add(10 * time.Second); !slices.Equal(ombSaved(path), full); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			cancel()
			<-done
			t.Fatalf("not written again while serving: %q", ombSaved(path))
		}
	}
	cancel()
	<-done

	// off in magpie: OpenMausBot's file is left as it is
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	after := readFile(path)
	if err := ombListedOnce(); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != after {
		t.Fatalf("a round wrote an engine magpie was taken out of:\n%s", readFile(path))
	}
}

// OpenMausBot running on the list it read before magpie last changed its
// engine offers the row's Restart, and its own save of the same engine
// after it started doesn't.
func TestOpenMausBotRestartOnMac(t *testing.T) {
	home := restartSandbox(t)
	ombRelay(t)
	apps := filepath.Join(home, "Applications")
	bundle := macApp(t, apps, "OpenMausBot.app", "com.openmausbot.app", "OpenMausBot")
	restartOnMac = true
	appFolders = func() []string { return []string{apps} }
	alive, up := false, time.Hour
	running = func(pat string) []runningProc {
		ps := []runningProc{
			{up, bundle + "/Contents/MacOS/OpenMausBot"},
			{up, bundle + "/Contents/Frameworks/OpenMausBot Helper (Renderer).app/Contents/MacOS/OpenMausBot Helper (Renderer) --type=renderer"},
			// its server, a script on the app's own executable
			{0, bundle + "/Contents/MacOS/OpenMausBot " + bundle + "/Contents/Resources/server/index.js"},
		}
		var out []runningProc
		for _, p := range ps {
			if alive && regexp.MustCompile(pat).MatchString(p.cmd) {
				out = append(out, p)
			}
		}
		return out
	}
	a := openMausBot(home)
	writeFile(t, a.Path, `{"instances":{"claude":{"driver":"claudeAgent"}}}`)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	wrote := time.Now().Add(-30 * time.Minute)
	os.Chtimes(a.Path, wrote, wrote)
	alive = true
	if a.AppBehind() == nil {
		t.Fatal("no Restart for OpenMausBot started an hour before magpie wrote its engine")
	}

	var did []string
	appQuit = func(_ context.Context, b string) error { did = append(did, "quit "+b); alive = false; return nil }
	appOpen = func(_ context.Context, b string) error { did = append(did, "open "+b); alive, up = true, 0; return nil }
	if err := a.RestartApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"quit " + bundle, "open " + bundle}; !slices.Equal(did, want) {
		t.Fatalf("did %q, want %q", did, want)
	}
	if a.AppBehind() != nil {
		t.Fatal("still offered after the restart")
	}

	// it saves its fleet a few seconds after it started: no change of magpie's
	up = 10 * time.Second
	ombSaves(t, a.Path, ombRead(t, a.Path)[magpieID])
	at := time.Now().Add(-6 * time.Second)
	os.Chtimes(a.Path, at, at)
	if got := a.AppBehind(); got != nil {
		t.Fatalf("OpenMausBot's own save after its start read as magpie's change: behind since %v", got.Since)
	}

	// a model magpie adds is one it hasn't read
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "gpt-5.5", "glm-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ombSaved(a.Path), "relay/glm-5") {
		t.Fatalf("not synced: %q", ombSaved(a.Path))
	}
	if a.AppBehind() == nil {
		t.Fatal("no Restart after magpie added a model OpenMausBot started without")
	}
}
