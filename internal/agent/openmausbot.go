package agent

// OpenMausBot (milind-soni/OpenMausBot, Apache-2.0, an Electron chat app
// whose bots run the CLIs installed on the computer) keeps its settings in
// config.json in its data folder: $OMB_DATA_DIR, else ~/.openmausbot on
// every system (server/config.ts's DATA_DIR). Each engine a bot can run on
// is an instance of a driver in that file's "instances" map; with none
// saved it runs its default fleet (server/instance-fleets.ts).
//
// Its Claude bots run Claude Code isolated, with only the sign-in keys of
// ~/.claude/settings.json (ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, …)
// carried over (server/drivers/claude.ts's readClaudeAuthSettings), so a
// Claude Code wired to magpie reached the gateway already, but its picker
// listed Claude's own models alone: the models setting env magpie writes
// isn't read. Its Codex bots run the user's ~/.codex as it is, which
// magpie's Codex wiring already covers.
//
// So magpie is an engine of its own there, "magpie", written as
// OpenMausBot's own "Claude endpoint" (server/claude-endpoints.ts's
// newClaudeEndpoint) writes one: a claudeAgent instance with access
// "custom", a Claude home of its own under the data folder, requireApiKey,
// the gateway's address and a key naming OpenMausBot (gateway.TokenFor) as
// ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN and ANTHROPIC_API_KEY, and every magpie model in endpointModels, its
// picker. An instance with any of those keys owns its routing: neither the
// user's settings nor a provider switch in OpenMausBot's environment
// reaches it (claudeEnvironment, claudeInstanceOwnsRouting).
//
// A claudeAgent engine runs the claude CLI. Where OpenMausBot has none to
// run (ombClaudeCLI), the engine listed magpie's models and answered
// "claude CLI not found", with an install button that needs Node's npm
// (KorwinTeo on X). There magpie's engine is OpenMausBot's own
// OpenAI-compatible one instead (server/drivers/openai-compat.ts, as its
// openai-compat-endpoints.ts adds one): it calls the gateway's /v1 itself,
// with OpenMausBot's own tools, and needs no CLI. The models are magpie's
// in managedModels, so OpenMausBot asks the gateway for no other list. A
// claudeAgent engine with no CLI to run is written over so; one on the
// OpenAI-compatible driver stays on it, so a bot's engine never changes
// under it twice.
//
// A saved map is the whole fleet: one holding magpie's alone would take
// every other engine away. So when the file has none, magpie writes the
// default fleet beside its own, as OpenMausBot itself does when the user
// adds an engine (persistableInstanceConfigs), and off takes the map out
// again while it is still that fleet.
//
// OpenMausBot reads the file when it starts, and writes its whole map of
// engines, as it holds it, when the user changes one in its Settings
// (routes/instances.ts: replaceInstances). So a change is refused while it
// runs, where magpie can tell; a sync writes anyway, as one OpenMausBot
// writes over is written again by the next, or within half a minute by
// KeepOpenMausBotListed. One running on an older list than the file's
// offers a Restart on its Agents row (apprestart.go), which reads it.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// ombInstance is the key of magpie's engine in OpenMausBot's config.json.
const ombInstance = "instances." + magpieID

// ombFleetKey is the stash key that says magpie wrote OpenMausBot's default
// fleet into a file that had none.
const ombFleetKey = "openmausbot.fleet"

// ombFleet is OpenMausBot's DEFAULT_FLEET (server/instance-fleets.ts at
// 516ca75, API_KEY_FLEET in its place), the engines it runs while its file
// saves none.
const ombFleet = `{
  "grok": {"driver": "grokAgent"},
  "kimi": {"driver": "kimiAgent"},
  "droid": {"driver": "droidAgent"},
  "cursor": {"driver": "cursorAgent"},
  "githubCopilot": {"driver": "copilotAgent"},
  "commandCode": {"driver": "commandCodeAgent"},
  "claude": {"driver": "claudeAgent"},
  "codex": {"driver": "codex"},
  "chatgpt": {"driver": "codex", "displayName": "ChatGPT plan", "config": {"authMode": "chatgpt-plan"}},
  "antigravity": {"driver": "antigravityAgent"},
  "opencodeGo": {"driver": "opencodeGo"},
  "openaiCompat": {"driver": "openai-compat"},
  "mistral": {"driver": "mistral"},
  "cerebras": {"driver": "cerebras"},
  "groq": {"driver": "groq"},
  "greenference": {"driver": "greenference"},
  "meta": {"driver": "meta"},
  "deepseek": {"driver": "deepseekAgent"},
  "museCode": {"driver": "museCode"},
  "openai": {"driver": "openai-compat", "displayName": "OpenAI", "access": "api", "icon": {"kind": "preset", "preset": "openai"},
    "config": {"url": "https://api.openai.com/v1", "apiKeyEnv": "OMB_OPENAI_API_KEY", "catalog": "openai"}},
  "claudeApi": {"driver": "claudeAgent", "displayName": "Claude (API key)", "access": "api", "icon": {"kind": "preset", "preset": "anthropic"},
    "config": {"requireApiKey": true}},
  "xaiApi": {"driver": "grok", "displayName": "xAI", "access": "api", "icon": {"kind": "preset", "preset": "xai"}},
  "openrouter": {"driver": "openai-compat", "displayName": "OpenRouter", "access": "api", "icon": {"kind": "preset", "preset": "openrouter"},
    "config": {"url": "https://openrouter.ai/api/v1", "apiKeyEnv": "OMB_OPENROUTER_API_KEY"}},
  "qwen": {"driver": "qwenAgent"},
  "hermes": {"driver": "hermesAgent"},
  "cline": {"driver": "clineAgent"},
  "pi": {"driver": "piAgent"}
}`

// ombModelID is the model id OpenMausBot takes for a Claude endpoint
// (server/drivers/claude-model-extras.ts's CLAUDE_MODEL_ID); one other id
// in the list makes it refuse the whole engine.
var ombModelID = lazyre.New(`(?i)^[a-z0-9][a-z0-9._:/-]*(?:\[1m\])?$`)

// ombRouting is the environment that picks Claude Code's endpoint and
// sign-in (config.ts's CLAUDE_ROUTING_ENV): magpie's on its engine, so none
// of the user's stays there.
var ombRouting = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// ombProc is OpenMausBot's app as ps lists it: the Mac bundle's
// executable, the Linux one (electron-builder.yml's executableName).
var ombProc = []string{`OpenMausBot\.app/Contents/MacOS/OpenMausBot( |$)`, `^([^ ]*/)?openmausbot( |$)`}

// ombApp is OpenMausBot's macOS bundle (electron-builder.yml's appId).
var ombApp = desktopApp{id: "com.openmausbot.app", names: []string{"OpenMausBot.app"}}

var noticeOMB = newNotice("OpenMausBot reads its engines when it starts and writes back the ones it holds when you change one in it — quit OpenMausBot before changing this, then open it and pick magpie as a bot's engine.")

// errOMBRunning is a change refused while OpenMausBot runs.
var errOMBRunning = errors.New("OpenMausBot writes its engines back over what magpie writes now: quit OpenMausBot, then try again")

// ombRunning says OpenMausBot is open where magpie can tell; on Windows,
// where Running can't, no, and the notice asks to quit it first.
var ombRunning = func() bool {
	return runtime.GOOS != "windows" && Running(ombProc...)
}

func openMausBot(home string) *Agent {
	dir := ombDir(home)
	path := filepath.Join(dir, "config.json")
	wired := func() bool { _, ok := edit.GetJSON(path, ombInstance); return ok }
	return atomic(&Agent{
		ID: "openmausbot", Name: "OpenMausBot", Icon: "openmausbot-color", Aliases: []string{"omb", "openmaus", "open-maus-bot"},
		Dir: dir, Path: path,
		detect: func() bool { return isDir(dir) || macBundle(ombApp) || ombWindowsApp() },
		Notice: func() string {
			if wired() && Running(ombProc...) {
				return noticeOMB.String()
			}
			return ""
		},
		ListOnly: true,
		// with no model OpenMausBot would take, the engine stays as it was
		Sync: func() error {
			if !wired() {
				return nil
			}
			want := ombMerged(path, dir, gateway.URL())
			if want == nil {
				return nil
			}
			return syncJSON(path, ombInstance, func() any { return want })
		},
		ListsModels: true,
		Check: func() string {
			if !wired() {
				return ""
			}
			if ombDriver(path) == ombAPIDriver {
				return wiringOff("OpenMausBot", path, func(k string) (string, bool) { return edit.GetJSON(path, ombInstance+".config."+k) }, "url", gateway.URL()+"/v1")
			}
			return wiringOff("OpenMausBot", path, func(k string) (string, bool) { return edit.GetJSON(path, ombInstance+".environment."+k) }, "ANTHROPIC_BASE_URL", gateway.URL())
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if ombRunning() {
					return errOMBRunning
				}
				if v == "" {
					return ombUnwire(path)
				}
				return ombWire(path, dir, gateway.URL())
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model under OpenMausBot's magpie engine; pick it for a bot"}}
			},
		}},
	}, path)
}

// ombDir is OpenMausBot's data folder: $OMB_DATA_DIR (only an absolute
// path, here: OpenMausBot takes it as written), else ~/.openmausbot.
func ombDir(home string) string {
	if d := strings.TrimSpace(appdir.Getenv("OMB_DATA_DIR")); d != "" && filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(home, ".openmausbot")
}

// ombWindowsApp reports whether OpenMausBot's per-user Windows install is
// there (electron-builder's NSIS, perMachine false: the package's name
// under Programs).
func ombWindowsApp() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	d := appdir.Getenv("LOCALAPPDATA")
	return d != "" && isFile(filepath.Join(d, "Programs", "openmausbot", "OpenMausBot.exe"))
}

// ombModels is the magpie models OpenMausBot's picker lists for its magpie
// engine, spelled as Claude Code takes them, a 1M one marked [1m]; an id
// OpenMausBot wouldn't take is left out.
func ombModels() []string {
	mark := claude1MFor("openmausbot")
	out := []string{}
	for _, m := range sharedModels("openmausbot") {
		id := provider.ClaudeSpelled(mark(m.ID))
		if ombModelID.MatchString(id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// ombAPIDriver is OpenMausBot's OpenAI-compatible driver, which calls a
// model's API itself and needs no CLI.
const ombAPIDriver = "openai-compat"

// ombDriver is the driver of magpie's engine at path, "" with none.
func ombDriver(path string) string {
	d, _ := edit.GetJSON(path, ombInstance+".driver")
	return d
}

// ombOnAPI says magpie's engine at path, in the folder dir, is to be
// OpenAI-compatible: it is already, or OpenMausBot has no Claude Code to
// run it as a Claude endpoint.
func ombOnAPI(path, dir string) bool {
	return ombDriver(path) == ombAPIDriver || !ombClaudeCLI(path, dir)
}

// ombInheritedCLI is the Claude Code the user gave OpenMausBot
// (inheritedCli): the cli of the first claudeAgent that is no endpoint.
func ombInheritedCLI(path string) string {
	raw, ok := edit.GetJSON(path, "instances")
	if !ok {
		return ""
	}
	var insts map[string]struct {
		Driver string         `json:"driver"`
		Config map[string]any `json:"config"`
	}
	json.Unmarshal([]byte(raw), &insts)
	ids := make([]string, 0, len(insts))
	for id := range insts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		e := insts[id]
		if _, endpoint := e.Config["endpointModels"]; id == magpieID || e.Driver != "claudeAgent" || endpoint {
			continue
		}
		if cli, _ := e.Config["cli"].(string); cli != "" {
			return cli
		}
	}
	return ""
}

// ombClaudeCLI says OpenMausBot has a claude CLI to run a claudeAgent
// engine with, as it looks for one (server/drivers/claude.ts,
// server/env-path.ts): the one a Claude engine of the user's names, the
// copy it installed in its own folder (engine-install.ts: tools/npm under
// its data folder), or claude on PATH and in the folders it adds to it
// (~/.local/bin, npm's, bun's, Volta's, ~/.claude/local, Homebrew's). A var
// so tests say.
var ombClaudeCLI = func(path, dir string) bool {
	if cli := ombInheritedCLI(path); cli != "" {
		if filepath.IsAbs(cli) && isFile(cli) || !filepath.IsAbs(cli) && binAt(cli) != "" {
			return true
		}
	}
	own := filepath.Join(dir, "tools", "npm")
	if runtime.GOOS != "windows" {
		own = filepath.Join(own, "bin")
	}
	home, _ := os.UserHomeDir()
	dirs := append([]string{own, filepath.Join(home, ".claude", "local")}, proc.SystemDirs("/opt/homebrew/bin", "/usr/local/bin")...)
	return proc.ToolIn("claude", dirs) != "" || binAt("claude") != ""
}

// ombAPIModels is the magpie models OpenMausBot's picker lists for an
// OpenAI-compatible magpie engine: as the gateway's /v1 serves them.
func ombAPIModels() []string {
	out := []string{}
	for _, m := range sharedModels("openmausbot") {
		if id := strings.TrimSpace(m.ID); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// ombWanted is the models magpie lists in its engine at path, in the folder
// dir, as the engine it is to be takes them.
func ombWanted(path, dir string) []string {
	if ombOnAPI(path, dir) {
		return ombAPIModels()
	}
	return ombModels()
}

// ombInstanceJSON is magpie's engine in OpenMausBot's config.json at path,
// in the folder dir, on the gateway at gw, in the shape OpenMausBot's own
// Claude endpoint has, or its OpenAI-compatible one has where it has no
// Claude Code to run (ombOnAPI); nil with no model it would take.
func ombInstanceJSON(path, dir, gw string) map[string]any {
	if ombOnAPI(path, dir) {
		models := ombAPIModels()
		if len(models) == 0 {
			return nil
		}
		// access "custom": where OpenMausBot listed magpie's engine before
		return map[string]any{
			"driver": ombAPIDriver, "displayName": "magpie", "access": "custom",
			"config": map[string]any{
				"url":           gw + "/v1",
				"key":           agentKeyAt("openmausbot", gw),
				"managedModels": models,
			},
		}
	}
	models := ombModels()
	if len(models) == 0 {
		return nil
	}
	config := map[string]any{
		"configDir":      filepath.Join(dir, "providers", magpieID),
		"requireApiKey":  true,
		"endpointModels": models,
	}
	if cli := ombInheritedCLI(path); cli != "" {
		config["cli"] = cli
	}
	// a key naming OpenMausBot: the Claude Code it runs sends nothing of
	// OpenMausBot's own, so with the gateway's plain token its calls were
	// counted as Claude Code's (huangfuyafei on X); the gateway still
	// treats them as Claude Code's for its tiers (claudeCodeInside)
	key := agentKeyAt("openmausbot", gw)
	return map[string]any{
		"driver": "claudeAgent", "displayName": "magpie", "access": "custom",
		"config": config,
		"environment": map[string]any{
			"ANTHROPIC_BASE_URL":   gw,
			"ANTHROPIC_AUTH_TOKEN": key,
			// Claude Code's own sign-in check needs a key it knows, as on
			// OpenMausBot's endpoints (endpointEnvironment)
			"ANTHROPIC_API_KEY": key,
			"ANTHROPIC_MODEL":   models[0],
		},
	}
}

// ombMerged is magpie's engine as ombInstanceJSON writes it, merged into
// the one at path: its name, colour, icon, whether it is on or hidden, the
// keys magpie doesn't write and the environment besides the routing are
// the user's, and so is the model picked first while magpie still lists it.
// The routing, the Claude home and the models are magpie's. One moved to
// the OpenAI-compatible driver keeps only its name, colour, icon and
// whether it is on or hidden: a Claude endpoint's config and environment
// mean nothing to that driver.
func ombMerged(path, dir, gw string) any {
	want := ombInstanceJSON(path, dir, gw)
	if want == nil {
		return nil
	}
	cur, ok := edit.GetJSON(path, ombInstance)
	if !ok {
		return want
	}
	var have map[string]json.RawMessage
	if json.Unmarshal([]byte(cur), &have) != nil || have == nil {
		return want
	}
	var was string
	json.Unmarshal(have["driver"], &was)
	if was != want["driver"] {
		for k := range have {
			if !slices.Contains([]string{"displayName", "accentColor", "icon", "enabled", "hidden"}, k) {
				delete(have, k)
			}
		}
	}
	if want["driver"] == ombAPIDriver {
		b, err := json.Marshal(want)
		var w map[string]json.RawMessage
		if err != nil || json.Unmarshal(b, &w) != nil {
			return want
		}
		if _, named := have["displayName"]; named {
			delete(w, "displayName")
		}
		return mergeTheirs(have, w, nil)
	}
	var theirs map[string]any
	if json.Unmarshal(have["environment"], &theirs) == nil {
		env := want["environment"].(map[string]any)
		models := want["config"].(map[string]any)["endpointModels"].([]string)
		for k, v := range theirs {
			s, isText := v.(string)
			switch {
			case !isText || slices.Contains(ombRouting, k):
			case k == "ANTHROPIC_MODEL":
				if slices.Contains(models, s) {
					env[k] = s
				}
			default:
				env[k] = s
			}
		}
	}
	b, err := json.Marshal(want)
	var w map[string]json.RawMessage
	if err != nil || json.Unmarshal(b, &w) != nil {
		return want
	}
	// displayName is the user's once they renamed it
	if _, named := have["displayName"]; named {
		delete(w, "displayName")
	}
	return mergeTheirs(have, w, []string{"environment"})
}

// ombSaved is the models magpie's engine at path lists in OpenMausBot's
// picker, as the file has them: a Claude endpoint's endpointModels, an
// OpenAI-compatible engine's managedModels.
func ombSaved(path string) []string {
	key := ".config.endpointModels"
	if ombDriver(path) == ombAPIDriver {
		key = ".config.managedModels"
	}
	raw, _ := edit.GetJSON(path, ombInstance+key)
	var have []string
	json.Unmarshal([]byte(raw), &have)
	return have
}

// ombPart is what OpenMausBot reads of magpie's engine at path when it
// starts, for a Restart while it runs on an older one (AppBehind): the
// driver, the gateway, the model first picked and the models listed, not
// the key.
func ombPart(path string) string {
	driver := ombDriver(path)
	url, _ := edit.GetJSON(path, ombInstance+".environment.ANTHROPIC_BASE_URL")
	model, _ := edit.GetJSON(path, ombInstance+".environment.ANTHROPIC_MODEL")
	if driver == ombAPIDriver {
		url, _ = edit.GetJSON(path, ombInstance+".config.url")
		model = ""
	}
	return driver + "\n" + url + "\n" + model + "\n" + strings.Join(ombSaved(path), "\n")
}

// ombListStale says magpie's engine at path lists other models than magpie
// does for OpenMausBot. A sync rewrites the list when magpie's changes, but
// OpenMausBot writes back the one it held since it started when the user
// changes an engine in it, and an edit of magpie's engine there keeps the
// model picked alone (claude-endpoints.ts's updateClaudeEndpoint): either
// leaves the picker, and its search, without models picked in magpie
// (maxleebeta, 11-08: Antigravity's weren't found), and no catalog change
// follows to write the list again.
//
// So is a Claude endpoint OpenMausBot has no Claude Code to run
// (ombOnAPI): it never answered, and nothing else writes it over.
//
// It is not a Check drift: that reads as unwired, and a port change
// rewires only agents without one.
func ombListStale(path, dir string) bool {
	want := ombWanted(path, dir)
	if len(want) == 0 {
		return false
	}
	if ombOnAPI(path, dir) && ombDriver(path) != ombAPIDriver {
		return true
	}
	have := ombSaved(path)
	if len(have) != len(want) {
		return true
	}
	for _, m := range want {
		if !slices.Contains(have, m) {
			return true
		}
	}
	return false
}

// ombKeepEvery is how often KeepOpenMausBotListed looks.
const ombKeepEvery = 30 * time.Second

// KeepOpenMausBotListed writes magpie's models into OpenMausBot's engine
// again, while the gateway serves, when OpenMausBot wrote a list over
// them (ombListStale). The model picked in it stays.
func KeepOpenMausBotListed(ctx context.Context) { keepOMBListed(ctx, ombKeepEvery) }

// keepOMBListed is KeepOpenMausBotListed at an interval the tests can
// shorten; a failure is said once, not every round.
func keepOMBListed(ctx context.Context, every time.Duration) {
	said := ""
	for {
		msg := ""
		if err := ombListedOnce(); err != nil {
			msg = err.Error()
		}
		if msg != "" && msg != said {
			log.Print("OpenMausBot: ", msg)
		}
		said = msg
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(every):
		}
	}
}

// ombListedOnce is one round of KeepOpenMausBotListed.
func ombListedOnce() error {
	home, _ := homes()
	a := openMausBot(home)
	if !a.Wired() || !ombListStale(a.Path, a.Dir) {
		return nil
	}
	return synced(a.Sync)
}

// ombWire writes magpie's engine into OpenMausBot's config.json at path,
// with the default fleet beside it when the file saves no engines.
func ombWire(path, dir, gw string) error {
	v := ombMerged(path, dir, gw)
	if v == nil {
		return errors.New("no magpie model OpenMausBot can list: pick models for OpenMausBot first")
	}
	raw, ok := edit.GetJSON(path, "instances")
	var insts map[string]json.RawMessage
	if ok && (json.Unmarshal([]byte(raw), &insts) != nil || insts == nil) {
		return errors.New(path + "'s instances isn't a map of engines, so magpie leaves it alone")
	}
	if len(insts) > 0 {
		return edit.SetJSON(path, edit.KV{Path: ombInstance, Value: v})
	}
	var fleet map[string]json.RawMessage
	json.Unmarshal([]byte(ombFleet), &fleet)
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fleet[magpieID] = b
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := edit.SetJSON(path, edit.KV{Path: "instances", Value: fleet}); err != nil {
		return err
	}
	stash(map[string]string{ombFleetKey: "1"})
	return nil
}

// ombUnwire takes magpie's engine out of OpenMausBot's config.json at
// path, and the default fleet magpie wrote with it while the rest is still
// that fleet, so OpenMausBot runs its defaults again as they are then.
func ombUnwire(path string) error {
	if err := edit.DelJSON(path, ombInstance); err != nil {
		return err
	}
	if !t3Stashed(ombFleetKey) {
		return nil
	}
	forget(ombFleetKey)
	if raw, ok := edit.GetJSON(path, "instances"); ok && sameJSON(raw, json.RawMessage(ombFleet)) {
		return edit.DelJSON(path, "instances")
	}
	return nil
}
