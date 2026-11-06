package agent

// DS Bot (github.com/Fei2411/DS-Bot, Apache-2.0) is a DSH plugin and an
// Electron desktop app built on it. The plugin, loaded into DSH Desktop or
// `dsh web`, runs on ~/.dsh and is magpie's dsh row. The desktop app ships
// its own DSH (0.2.2-rc.1 in DS Bot 0.3.0) and runs it with DSH_HOME at a
// data folder of its own (app/src/main.mjs), booting the profile ds-bot
// (app/src/profile.mjs). Its models are that DSH's llm-pi-ai routes and
// its new Bots start on DSH's agent-default-model (ds-bot/src/host/
// models.js), so magpie wires that home as it wires ~/.dsh: its route in
// each profile's patch list, the key in .env and DSH's key store, and the
// start in agent-default-model (dshOn).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
)

const dsbotID = "ds-bot"

// dsbotProfile is the profile the app's DSH boots, under its data folder
// (PROFILE in app/src/profile.mjs).
const dsbotProfile = "ds-bot"

// dsbotFile is the app's profile's patch list in its data folder dir. The
// app's initProfile writes each of the profile's files only where it is
// missing, as dsh's does: one magpie made before the app first opened is
// kept (seen with DS Bot 0.3.0, whose template is dsh's byte for byte).
func dsbotFile(dir string) string {
	return filepath.Join(dir, "profiles", dsbotProfile, "cordis.patch.yml")
}

// dsbotHome is the app's data folder, the DSH_HOME its DSH runs on, and
// whether it is known. chooseLocations (app/src/first-run.mjs) takes it
// from DS_BOT_HOME, which runs the app on throwaway folders, else from
// locations.json's dataDir in the app's Electron userData (DS_BOT_SHELL_DATA
// when set, else <appData>/DS Bot, or <appData>/DeepSeek Bot, its name
// before, while that one is there and DS Bot's has no locations.json:
// configureIdentity in app/src/legacy.mjs), else ~/.ds-bot, the folder its
// first run saves when the user keeps the default. A locations.json that
// can't be read or names no folder is unknown: never taken for the default,
// where magpie would wire a folder the app doesn't run on.
func dsbotHome() (string, bool) {
	if dir := appdir.Getenv("DS_BOT_HOME"); dir != "" {
		return dir, true
	}
	userData := appdir.Getenv("DS_BOT_SHELL_DATA")
	if userData == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", false
		}
		userData = filepath.Join(cfg, "DS Bot")
		if legacy := filepath.Join(cfg, "DeepSeek Bot"); !isFile(filepath.Join(userData, "locations.json")) && isDir(legacy) {
			userData = legacy
		}
	}
	b, err := os.ReadFile(filepath.Join(userData, "locations.json"))
	if errors.Is(err, os.ErrNotExist) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, ".ds-bot"), true
	}
	if err != nil {
		return "", false
	}
	var places struct {
		DataDir string `json:"dataDir"`
	}
	if json.Unmarshal(b, &places) != nil || strings.TrimSpace(places.DataDir) == "" {
		return "", false
	}
	return places.DataDir, true
}

// dsbotDataDir is dsbotHome, "" when it is unknown, with the folder noted
// as DS Bot's (dshApps): the routes written there are DS Bot's models.
func dsbotDataDir() string {
	dir, ok := dsbotHome()
	if !ok {
		return ""
	}
	dshApps.Store(filepath.Clean(dir), dsbotID)
	return dir
}

func dsbot(home string) *Agent {
	dir := dsbotDataDir()
	// the app, installed on this machine (Linux's AppImage and deb aren't
	// looked for: there the data folder its first start makes finds it)
	app := func() bool { return (&Agent{ID: dsbotID}).HasApp() }
	if dir == "" {
		// its data folder can't be told: nothing to read or write
		return &Agent{ID: dsbotID, Name: "DS Bot", Icon: "deepseek-color", detect: app}
	}
	fresh := func() string { return dsbotFile(dir) }
	a := dshOn(here(home), dir, dsbotID, fresh, func() string { return dsbotProfile })
	a.ID, a.Name, a.Icon = dsbotID, "DS Bot", "deepseek-color"
	a.Aliases = []string{"dsbot", "deepseek-bot"}
	// no Bin: its command (dsbot, on Linux) is the app itself, which a
	// version check would open
	// installed and not opened yet, it has no data folder
	a.sign = app
	a.Notice = func() string {
		var notes []string
		if app() && dshUnbooted(dsbotFile(dir)) {
			notes = append(notes, noticeDsbotUnopened.String())
		} else {
			// a Bot has its own model, kept when the start changes: only
			// a new one starts on agent-default-model, and only while no
			// default Bot model is set in the app (defaultBotModel)
			notes = append(notes, noticeDsbotNewBots.String())
		}
		if dshSearchesHere(dir) && !gateway.CanSearch() {
			notes = append(notes, noticeDsbotSearch.String())
		}
		return strings.Join(notes, " ")
	}
	// what magpie made for an app never opened goes
	a.Restore = func() error { return dshTidyMade(dir, dsbotFile(dir)) }
	return a
}

var (
	noticeDsbotUnopened = newNotice("DS Bot hasn't been opened yet: it starts on this the first time it opens.")
	noticeDsbotNewBots  = newNotice("New Bots in DS Bot start on this, unless a default model for Bots is set in DS Bot. A Bot made before keeps its own model until you pick another for it.")
	noticeDsbotSearch   = newNotice("DS Bot's web search goes through magpie, which has nothing to search with yet: set up a provider that searches by itself (DeepSeek, Claude, Codex, …) or a search API under Settings › Web search.")
)
