package agent

// Claude Desktop runs on a third-party gateway in its "3p" deployment mode:
// no Anthropic sign-in, and its Code and Cowork tabs work on any plan (a
// Free account signed in sees only "Get Claude Code" there). The mode is
// claude_desktop_config.json's deploymentMode, in its own folder and in
// Claude-3p beside it; the gateway is a profile in Claude-3p/configLibrary,
// the one _meta.json applies:
//
//	Claude/claude_desktop_config.json      {"deploymentMode":"3p",…}
//	Claude-3p/claude_desktop_config.json   {"deploymentMode":"3p",…}
//	Claude-3p/configLibrary/<id>.json      {"inferenceProvider":"gateway",
//	  "inferenceGatewayBaseUrl":"http://127.0.0.1:3425",
//	  "inferenceGatewayApiKey":"magpie-claude-desktop",
//	  "inferenceGatewayAuthScheme":"bearer",
//	  "disableDeploymentModeChooser":true,"coworkEgressAllowedHosts":["*"]}
//	Claude-3p/configLibrary/_meta.json     {"appliedId":"<id>",
//	  "entries":[…,{"id":"<id>","name":"magpie"}]}
//
// as CC Switch writes them (claude_desktop_config.rs). The folders are in
// ~/Library/Application Support on macOS, %LOCALAPPDATA% on Windows and
// $XDG_CONFIG_HOME on Linux. With no inferenceModels Desktop lists the
// gateway's /v1/models, which the gateway gives it by ids it keeps (see
// gateway/desktop.go). Desktop reads all this at start-up only.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// desktopProfileID is magpie's profile in Desktop's configLibrary: a
// UUID like the ones Desktop makes, its last part "magpie" in hex.
const desktopProfileID = "00000000-0000-4000-8000-6d6167706965"

// desktopPaths are the files and folders magpie writes for Claude Desktop.
type desktopPaths struct {
	dir, dir3p          string // Claude, Claude-3p
	config, config3p    string // their claude_desktop_config.json
	library, meta, prof string // configLibrary, its _meta.json, magpie's profile
}

func desktopPathsOf(dir, dir3p string) desktopPaths {
	lib := filepath.Join(dir3p, "configLibrary")
	return desktopPaths{
		dir: dir, dir3p: dir3p,
		config:   filepath.Join(dir, "claude_desktop_config.json"),
		config3p: filepath.Join(dir3p, "claude_desktop_config.json"),
		library:  lib, meta: filepath.Join(lib, "_meta.json"), prof: filepath.Join(lib, desktopProfileID+".json"),
	}
}

// desktopDirs are Desktop's Claude and Claude-3p folders on goos.
func desktopDirs(goos, home string, getenv func(string) string) (string, string) {
	switch goos {
	case "darwin":
		d := filepath.Join(home, "Library", "Application Support")
		return filepath.Join(d, "Claude"), filepath.Join(d, "Claude-3p")
	case "windows":
		d := getenv("LOCALAPPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Local")
		}
		return windowsDesktopDir(d, false), windowsDesktopDir(d, true)
	}
	d := getenv("XDG_CONFIG_HOME")
	if d == "" || !filepath.IsAbs(d) {
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "Claude"), filepath.Join(d, "Claude-3p")
}

// DesktopConfig3p is the claude_desktop_config.json Claude Desktop reads in
// its 3p mode: there its whole userData is Claude-3p, its MCP servers too
// (%LOCALAPPDATA%\Claude-3p on Windows, Claude-3p beside Claude elsewhere).
func DesktopConfig3p(home string) string {
	_, d := desktopDirs(runtime.GOOS, home, os.Getenv)
	return filepath.Join(d, "claude_desktop_config.json")
}

// windowsDesktopDir is %LOCALAPPDATA%\Claude (or Claude-3p), else the first
// folder there named Claude… (with -3p in it or not), as CC Switch finds it.
func windowsDesktopDir(local string, threep bool) string {
	name := "Claude"
	if threep {
		name = "Claude-3p"
	}
	exact := filepath.Join(local, name)
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	ents, _ := os.ReadDir(local)
	var found []string
	for _, e := range ents {
		if n := e.Name(); e.IsDir() && strings.HasPrefix(n, "Claude") && strings.Contains(n, "-3p") == threep {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return exact
	}
	sort.Strings(found)
	return filepath.Join(local, found[0])
}

func claudeDesktop(home string) *Agent {
	p := desktopPathsOf(desktopDirs(runtime.GOOS, home, os.Getenv))
	// %APPDATA%\Claude is where Desktop keeps its MCP servers on Windows
	also := ""
	if runtime.GOOS == "windows" {
		if d := os.Getenv("APPDATA"); d != "" {
			also = filepath.Join(d, "Claude")
		}
	}
	return &Agent{
		ID: "claude-desktop", Name: "Claude Desktop", Icon: "claude-color", Aliases: []string{"claude-app"},
		Dir: p.dir, Path: p.config,
		detect: func() bool {
			for _, d := range []string{p.dir, p.dir3p, also} {
				if d == "" {
					continue
				}
				if isDir(d) {
					return true
				}
			}
			return false
		},
		Notice: func() string {
			if desktopWired(p) {
				return "Claude Desktop reads this at start-up — quit and reopen it to run on magpie (Code and Cowork, no Anthropic sign-in)."
			}
			return "Claude Desktop reads this at start-up — quit and reopen it to sign in with Anthropic again."
		},
		Check: func() string {
			if !desktopWired(p) {
				return ""
			}
			if m, _ := edit.GetJSON(p.config, "deploymentMode"); m != "3p" {
				return "Claude Desktop's deploymentMode (claude_desktop_config.json) is no longer 3p, so it signs in with Anthropic rather than using magpie"
			}
			if id, _ := edit.GetJSON(p.meta, "appliedId"); id != desktopProfileID {
				return "Claude Desktop uses another gateway configuration now (appliedId in configLibrary/_meta.json), not magpie's"
			}
			return wiringOff("Claude Desktop", p.prof, func(k string) (string, bool) { return edit.GetJSON(p.prof, k) },
				"inferenceGatewayBaseUrl", gateway.URL(), "inferenceGatewayApiKey", gateway.TokenFor("claude-desktop"))
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if desktopWired(p) {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				on := desktopOn
				if v == "" {
					on = desktopOff
				}
				if err := on(p); err != nil {
					return err
				}
				// its Code tab is Claude Code, told what Desktop's ids for
				// magpie's models can do while that one runs on magpie
				_ = claude(home).Sync()
				return nil
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie",
					Note: "Desktop's third-party gateway: Code and Cowork on magpie's models, no Anthropic sign-in (restart Desktop)"}}
			},
		}},
	}
}

// desktopWired: _meta.json lists magpie's profile.
func desktopWired(p desktopPaths) bool {
	entries, _, err := desktopEntries(p.meta)
	return err == nil && slices.ContainsFunc(entries, desktopOurs)
}

// desktopEntries reads _meta.json's entries, and whether the file is there.
func desktopEntries(meta string) ([]json.RawMessage, bool, error) {
	b, err := edit.Read(meta)
	if err != nil || b == nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, true, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, true, errors.New(meta + ": not a JSON object")
	}
	var entries []json.RawMessage
	if raw, ok := doc["entries"]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, true, errors.New(meta + ": entries is not a list")
		}
	}
	return entries, true, nil
}

func desktopEntryID(raw json.RawMessage) string {
	var e struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &e)
	return e.ID
}

func desktopOurs(raw json.RawMessage) bool { return desktopEntryID(raw) == desktopProfileID }

// desktopObject: the file is missing, empty or a JSON object — one magpie
// can edit without losing anything.
func desktopObject(path string) error {
	b, err := edit.Read(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return errors.New(path + ": not a JSON object")
	}
	return nil
}

// The stash keeps what Desktop had before magpie: each file's
// deploymentMode ("" when it had none), the profile applied, and the
// files and folders magpie made, so switching off puts all of it back.
const (
	desktopStashed = "claude-desktop.on"
	desktopMode    = "claude-desktop.mode"
	desktopMode3p  = "claude-desktop.mode3p"
	desktopApplied = "claude-desktop.applied"
	desktopMade    = "claude-desktop.made"
)

// desktopOn points Claude Desktop at the gateway: deploymentMode 3p in both
// config files, magpie's profile written and applied. Every other key, and
// every other profile, stays as it is.
func desktopOn(p desktopPaths) error {
	// nothing is written unless every file can be
	for _, f := range []string{p.config, p.config3p, p.prof} {
		if err := desktopObject(f); err != nil {
			return err
		}
	}
	entries, _, err := desktopEntries(p.meta)
	if err != nil {
		return err
	}
	wired := slices.ContainsFunc(entries, desktopOurs)
	if !wired {
		var made []string
		for _, f := range []string{p.dir, p.config, p.dir3p, p.config3p, p.library, p.meta} {
			if _, err := os.Stat(f); err != nil {
				made = append(made, f)
			}
		}
		mode, _ := edit.GetJSON(p.config, "deploymentMode")
		mode3p, _ := edit.GetJSON(p.config3p, "deploymentMode")
		applied, _ := edit.GetJSON(p.meta, "appliedId")
		forget(desktopMode, desktopMode3p, desktopApplied, desktopMade)
		stash(map[string]string{desktopStashed: "1", desktopMode: mode, desktopMode3p: mode3p,
			desktopApplied: applied, desktopMade: strings.Join(made, "\n")})
	}

	// the profile: the gateway's address and key set, Desktop's own
	// settings in it (the user's, from its window) kept
	if b, _ := edit.Read(p.prof); len(bytes.TrimSpace(b)) == 0 {
		if err := os.MkdirAll(p.library, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p.prof, []byte("{}\n"), 0o600); err != nil {
			return err
		}
	}
	kvs := []edit.KV{
		{Path: "inferenceProvider", Value: "gateway"},
		{Path: "inferenceGatewayBaseUrl", Value: gateway.URL()},
		{Path: "inferenceGatewayApiKey", Value: gateway.TokenFor("claude-desktop")},
		{Path: "inferenceGatewayAuthScheme", Value: "bearer"},
	}
	// policies seeded once: the user may tighten them in Desktop
	if _, ok := edit.GetJSON(p.prof, "disableDeploymentModeChooser"); !ok {
		kvs = append(kvs, edit.KV{Path: "disableDeploymentModeChooser", Value: true})
	}
	if _, ok := edit.GetJSON(p.prof, "coworkEgressAllowedHosts"); !ok {
		kvs = append(kvs, edit.KV{Path: "coworkEgressAllowedHosts", Value: []string{"*"}})
	}
	if err := edit.SetJSON(p.prof, kvs...); err != nil {
		return err
	}

	// _meta.json: magpie's entry (where it was, if it was), applied
	if !wired {
		entry, _ := json.Marshal(map[string]string{"id": desktopProfileID, "name": "magpie"})
		entries = append(entries, entry)
		if err := edit.SetJSON(p.meta, edit.KV{Path: "entries", Value: entries}); err != nil {
			return err
		}
	}
	if err := edit.SetJSON(p.meta, edit.KV{Path: "appliedId", Value: desktopProfileID}); err != nil {
		return err
	}

	// the mode last: until here Desktop still starts as it did
	for _, f := range []string{p.config3p, p.config} {
		if err := edit.SetJSON(f, edit.KV{Path: "deploymentMode", Value: "3p"}); err != nil {
			return err
		}
	}
	return nil
}

// desktopOff puts Claude Desktop back as it was before magpie: each
// deploymentMode as it was (gone where there was none), the profile it had
// applied, magpie's profile and entry removed, and the files and folders
// magpie made gone again if nothing else went into them.
func desktopOff(p desktopPaths) error {
	entries, _, err := desktopEntries(p.meta)
	if err != nil {
		return err
	}
	stashed := unstash(desktopStashed) != ""
	mode, mode3p, applied := unstash(desktopMode), unstash(desktopMode3p), unstash(desktopApplied)
	var made []string
	if m := unstash(desktopMade); m != "" {
		made = strings.Split(m, "\n")
	}
	if !stashed && !slices.ContainsFunc(entries, desktopOurs) {
		return nil
	}

	for f, was := range map[string]string{p.config: mode, p.config3p: mode3p} {
		if cur, _ := edit.GetJSON(f, "deploymentMode"); cur != "3p" {
			continue // changed since, by Desktop or the user: theirs
		}
		if was != "" {
			err = edit.SetJSON(f, edit.KV{Path: "deploymentMode", Value: was})
		} else {
			err = edit.DelJSON(f, "deploymentMode")
		}
		if err != nil {
			return err
		}
	}

	if slices.ContainsFunc(entries, desktopOurs) {
		entries = slices.DeleteFunc(entries, desktopOurs)
		if len(entries) == 0 && slices.Contains(made, p.meta) {
			err = edit.DelJSON(p.meta, "entries")
		} else {
			if entries == nil {
				entries = []json.RawMessage{}
			}
			err = edit.SetJSON(p.meta, edit.KV{Path: "entries", Value: entries})
		}
		if err != nil {
			return err
		}
	}
	if id, _ := edit.GetJSON(p.meta, "appliedId"); id == desktopProfileID {
		// the profile applied before, if it is still there, else the first
		// one left, else none
		next := ""
		if applied != "" && applied != desktopProfileID && slices.ContainsFunc(entries, func(e json.RawMessage) bool { return desktopEntryID(e) == applied }) {
			next = applied
		} else if len(entries) > 0 {
			next = desktopEntryID(entries[0])
		}
		if next != "" {
			err = edit.SetJSON(p.meta, edit.KV{Path: "appliedId", Value: next})
		} else {
			err = edit.DelJSON(p.meta, "appliedId")
		}
		if err != nil {
			return err
		}
	}
	if err := os.Remove(p.prof); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// what magpie made, deepest first: a file only if it is an empty
	// object now, a folder only if it is empty
	for _, f := range slices.Backward(made) {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.IsDir() {
			os.Remove(f)
			continue
		}
		if b, _ := os.ReadFile(f); desktopEmpty(b) {
			os.Remove(f)
		}
	}
	return nil
}

func desktopEmpty(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && len(m) == 0
}
