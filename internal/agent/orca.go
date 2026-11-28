package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// Orca (orca.dev, github.com/stablyai/orca, read at v1.4.281) is a
// desktop app that runs agents in parallel worktrees. It keeps no provider
// of its own: its chat runs the user's own Codex and Claude Code CLIs
// (src/main/runtime/agent-session/installation/
// structured-agent-runtime-registrations.ts), each on that CLI's own
// settings. Codex's managed homes mirror ~/.codex/config.toml
// (src/main/codex-accounts/managed-homes/codex-config-mirror.ts), model
// providers and model_catalog_json kept; a Claude profile has
// ~/.claude/settings.json synced in (src/main/claude-accounts/profiles/
// setup/claude-profile-provisioning.ts). Its own settings (agentDefaultEnv
// and the rest) are in a SQLite profile store, which is the app's alone
// (src/main/persistence/profile-state/legacy-json/README.md). So Orca is
// on magpie exactly when the Codex and Claude Code it runs are: connecting
// it connects those of them installed here and not on magpie yet, and
// turning it off takes off again the ones it connected.
//
// Its Codex sessions say clientInfo orca_desktop (src/main/codex/
// app-server/codex-app-server-handshake.ts), which Codex sends as its
// User-Agent's product: UA names those requests Orca's.

// orcaEngines are the magpie agents Orca's chat runs, in order.
var orcaEngines = []string{"codex", "claude"}

// orcaEngine is the magpie agent of an engine; a var so tests can stand
// one in.
var orcaEngine = func(id string) *Agent {
	home, _ := homes()
	switch id {
	case "codex":
		return codex(home)
	case "claude":
		return claude(home)
	}
	return nil
}

// orcaJoinedKey is the stash key of the engines magpie connected along
// with Orca, for its Disconnect to take off again.
const orcaJoinedKey = "orca.joined"

var (
	noticeOrca       = newNotice("Orca's new sessions run on magpie; sessions already open keep what they had.")
	noticeOrcaJoined = newNotice("Orca runs {agents} with their own settings, so magpie connected them too; turning Orca off disconnects them again.")
	errOrcaNoEngine  = errors.New("Orca runs Codex and Claude Code: install either one, then connect Orca")
)

// orcaNote is the provider's note.
const orcaNote = "the Codex and Claude Code it runs are connected to magpie"

// orcaJoined is an engine magpie connected along with Orca, and whether
// the user had disconnected it before that.
type orcaJoined struct {
	Agent string `json:"agent"`
	Off   bool   `json:"off,omitempty"`
}

func orcaJoinedNow() []orcaJoined {
	var js []orcaJoined
	if s := stashShared()[orcaJoinedKey]; s != "" {
		json.Unmarshal([]byte(s), &js)
	}
	return slices.DeleteFunc(js, func(j orcaJoined) bool { return j.Agent == "" })
}

func orcaJoinedSave(js []orcaJoined) {
	if len(js) == 0 {
		forget(orcaJoinedKey)
		return
	}
	b, _ := json.Marshal(js)
	stash(map[string]string{orcaJoinedKey: string(b)})
}

// orcaUserLeft: the user disconnected the engine id themselves. Connected
// again after, it is theirs, and Orca's Disconnect leaves it on. Orca
// runs on it, so Orca is off magpie now too, by the user's say, not
// changed outside magpie.
func orcaUserLeft(id string) {
	if !slices.Contains(orcaEngines, id) {
		return
	}
	js := orcaJoinedNow()
	if left := slices.DeleteFunc(slices.Clone(js), func(j orcaJoined) bool { return j.Agent == id }); len(left) != len(js) {
		orcaJoinedSave(left)
	}
	(&Agent{ID: "orca"}).forgetSetUp()
}

// orcaOS is the system Orca's folders are looked for on; tests stand in
// another.
var orcaOS = runtime.GOOS

// orcaDataDir is Orca's Electron userData folder: its productName, Orca
// (config/electron-builder.config.cjs), under the system's app data
// folder.
func orcaDataDir(home, cfg string) string {
	switch orcaOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Orca")
	case "windows":
		d := appdir.Getenv("APPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(d, "Orca")
	}
	return filepath.Join(cfg, "Orca")
}

func orca(home, cfg string) *Agent {
	dir := orcaDataDir(home, cfg)
	// installed is each engine installed here, in orcaEngines' order
	installed := func() []*Agent {
		var as []*Agent
		for _, id := range orcaEngines {
			if e := orcaEngine(id); e != nil && e.Detected() {
				as = append(as, e)
			}
		}
		return as
	}
	on := func() bool {
		es := installed()
		return len(es) > 0 && !slices.ContainsFunc(es, func(e *Agent) bool { return !e.Wired() })
	}
	leave := func(e *Agent, j orcaJoined) error {
		if !e.Wired() {
			return nil
		}
		if err := e.Disconnect(); err != nil {
			return err
		}
		if !j.Off {
			forget(e.ID + disconnectedKey)
		}
		return nil
	}
	var a *Agent
	a = &Agent{
		ID: "orca", Name: "Orca", Icon: "orca", UA: []string{"orca_desktop"},
		Dir: dir,
		// its data folder or its app; never `orca` on PATH or ~/.orca
		// alone, which GNOME's screen reader of that name has
		detect: func() bool {
			return isDir(dir) && !macAppRemoved("orca") || a.HasApp()
		},
		Notice: func() string {
			if !on() {
				return ""
			}
			notes := []string{noticeOrca.String()}
			var names []string
			for _, j := range orcaJoinedNow() {
				if e := orcaEngine(j.Agent); e != nil && e.Wired() {
					names = append(names, e.Name)
				}
			}
			if len(names) > 0 {
				notes = append(notes, noticeOrcaJoined.say("agents", strings.Join(names, ", ")))
			}
			return strings.Join(notes, " ")
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if on() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				if v == "" {
					// the engines magpie connected along with Orca go off;
					// with none, the user's own are what Orca runs on, and
					// turning Orca off takes them off
					js := orcaJoinedNow()
					forget(orcaJoinedKey)
					for _, j := range js {
						if e := orcaEngine(j.Agent); e != nil {
							if err := leave(e, j); err != nil {
								return err
							}
						}
					}
					if on() {
						for _, e := range installed() {
							if err := e.Disconnect(); err != nil {
								return err
							}
						}
					}
					return nil
				}
				es := installed()
				if len(es) == 0 {
					return errOrcaNoEngine
				}
				js := orcaJoinedNow()
				var now []orcaJoined
				for _, e := range es {
					if e.Wired() {
						continue
					}
					j := orcaJoined{Agent: e.ID, Off: e.Disconnected()}
					if _, err := e.ConnectHow(); err != nil {
						// what this connect put on goes off again
						for _, j := range now {
							if e := orcaEngine(j.Agent); e != nil {
								leave(e, j)
							}
						}
						return err
					}
					now = append(now, j)
					js = append(slices.DeleteFunc(js, func(o orcaJoined) bool { return o.Agent == j.Agent }), j)
					orcaJoinedSave(js)
				}
				return nil
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: orcaNote}}
			},
		}},
	}
	return a
}
