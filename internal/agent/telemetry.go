package agent

// An agent's own telemetry, off (OipsAnthony on X: "agent 管理页面支持给每个
// agent 关闭遥测功能"). An agent with a documented switch for the usage data
// it sends its vendor has it in its settings on the Agents page, which
// writes that switch into the agent's own config. Only agents whose key was
// read in their source or binary:
//
//   - Codex: [analytics] enabled = false in config.toml. "When false,
//     disables OpenAI analytics across Codex product surfaces"; the default
//     statsig metrics exporter follows it (AnalyticsConfigToml and
//     OtelConfigToml in codex-rs/config/src/types.rs, rust-v0.167.0).
//     feedback.enabled is left alone: it takes /feedback away, which only
//     sends when the user asks.
//   - Claude Code: env DISABLE_TELEMETRY and DISABLE_ERROR_REPORTING in
//     settings.json. 2.1.316 takes any value of DISABLE_TELEMETRY as its
//     "no-telemetry" level, from any settings tier, and any value of
//     DISABLE_ERROR_REPORTING keeps its error reports in.
//     CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC would also stop its updates,
//     which is no telemetry.
//   - Gemini CLI and Qwen Code: privacy.usageStatisticsEnabled = false in
//     settings.json (settingsSchema.ts of each, default true).
//   - Crush: options.disable_metrics = true in crush.json (Options in
//     internal/config/config.go, read from its binary; crush also takes
//     DO_NOT_TRACK).
//   - Kimi Code: telemetry = false at the top of config.toml (app.py:
//     telemetry_disabled = not config.telemetry or KIMI_DISABLE_TELEMETRY).
//   - Goose: GOOSE_TELEMETRY_ENABLED: false in config.yaml (the key its
//     `goose configure` telemetry choice writes).
//   - Pi: enableInstallTelemetry = false in settings.json (docs/settings.md:
//     anonymous install/update reporting, default true; its env
//     PI_TELEMETRY wins over the file).
//   - Cline CLI: telemetryOptOut = true in data/settings/global-settings.json
//     (the setting its core reads for every telemetry event; default
//     false).
//
// Antigravity, Droid, OpenCode, Cursor CLI, Hermes and omp have none to
// write: Antigravity's agy keeps it as the Google account's server-side
// setting (its EnableTelemetry field is json:"-"), Droid's are set by an
// organisation, OpenCode's and omp's OpenTelemetry export is opt-in,
// Cursor's privacy mode is the account's, and Hermes sends none.
//
// None is written unless the user turns it off here. Off writes each key
// the file doesn't have and remembers it in the stash; a key the user has
// already written is theirs: one that already turns telemetry off is left
// as it is, and one that keeps it on is never written over (off is
// refused, naming it). On takes out only what magpie wrote, as magpie
// wrote it. Connecting or disconnecting the agent leaves it alone: it is
// about the agent's vendor, not about magpie.
//
// Only an agent on this computer has it: a WSL distro's file isn't read on
// each look, which would start a stopped distro (#3896).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/settings"
)

// TelemetryState is an agent's telemetry as its config has it.
type TelemetryState struct {
	// Off: every key of the switch turns telemetry off
	Off bool `json:"off"`
	// Own: keys the user wrote that keep it off, which on leaves in
	Own []string `json:"own,omitempty"`
	// Keys: what the switch writes, as the file spells it
	Keys []string `json:"keys"`
	// File: the config it writes
	File string `json:"file"`
	// Error: why the config couldn't be read, when it couldn't (the
	// state is then unknown)
	Error string `json:"error,omitempty"`
	// All: Settings › Privacy turns every agent's off, which holds this
	// agent's row (telemetry_all.go); AllError why it couldn't here
	All      bool   `json:"all,omitempty"`
	AllError string `json:"allError,omitempty"`
}

// Telemetry is an agent's switch for its own telemetry.
type Telemetry struct {
	Get func() (TelemetryState, error)
	Set func(off bool) error
	// all: the stash key that says Settings › Privacy's switch for every
	// agent turned this one off (telemetry_all.go)
	all string
}

// telemetryKey is one key of a switch: name as the file spells it (a
// dotted path in JSON; "table.key" in TOML), value what magpie writes, off
// whether a value the file has keeps telemetry off.
type telemetryKey struct {
	name  string
	value any
	off   func(v any) bool
}

// telemetryFile reads and writes one key of the agent's config. get says
// whether the key is there, and fails on a file that doesn't parse (which
// is unknown, never "not there").
type telemetryFile struct {
	path string
	get  func(k telemetryKey) (v any, has bool, err error)
	set  func(k telemetryKey) error
	del  func(k telemetryKey) error
}

func isFalse(v any) bool { return v == false }
func isTrue(v any) bool  { return v == true }

// anyValue: an environment variable that is set, to anything but empty
func anyValue(v any) bool {
	switch x := v.(type) {
	case string:
		return x != ""
	case nil:
		return false
	}
	return true
}

// telemetrySwitch is the switch over keys in f; stashKey is where what
// magpie wrote is kept.
func telemetrySwitch(at place, f telemetryFile, stashKey string, keys ...telemetryKey) *Telemetry {
	if at.spell != nil || at.cold {
		return nil
	}
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.name
	}
	wrote := func() map[string]bool {
		out := map[string]bool{}
		for _, n := range strings.Split(stashShared()[stashKey], ",") {
			if n != "" {
				out[n] = true
			}
		}
		return out
	}
	keep := func(m map[string]bool) {
		var out []string
		for _, n := range names {
			if m[n] {
				out = append(out, n)
			}
		}
		stash(map[string]string{stashKey: strings.Join(out, ",")})
	}
	ours := func(k telemetryKey, v any) bool {
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(k.value)
		return string(a) == string(b)
	}
	get := func() (TelemetryState, error) {
		st := TelemetryState{Off: true, Keys: names, File: at.native(f.path)}
		mine := wrote()
		for _, k := range keys {
			v, has, err := f.get(k)
			if err != nil {
				return TelemetryState{Keys: names, File: st.File}, err
			}
			if !has || !k.off(v) {
				st.Off = false
				continue
			}
			if !(mine[k.name] && ours(k, v)) {
				st.Own = append(st.Own, k.name)
			}
		}
		return st, nil
	}
	set := func(off bool) error {
		mine := wrote()
		if off {
			// every key read first: one the user set on stops it before
			// anything is written
			var missing []telemetryKey
			for _, k := range keys {
				v, has, err := f.get(k)
				if err != nil {
					return err
				}
				switch {
				case !has:
					missing = append(missing, k)
				case !k.off(v):
					b, _ := json.Marshal(v)
					return fmt.Errorf("%s sets %s to %s; magpie leaves a value of yours as it is (change it there)", at.native(f.path), k.name, b)
				case !ours(k, v):
					delete(mine, k.name) // the user's, as it is now
				}
			}
			for _, k := range missing {
				if err := f.set(k); err != nil {
					keep(mine)
					return err
				}
				mine[k.name] = true
			}
			keep(mine)
			return nil
		}
		for _, k := range keys {
			if !mine[k.name] {
				continue
			}
			v, has, err := f.get(k)
			if err != nil {
				return err
			}
			// changed since: the user's now
			if has && ours(k, v) {
				if err := f.del(k); err != nil {
					keep(mine)
					return err
				}
			}
			delete(mine, k.name)
		}
		keep(mine)
		return nil
	}
	// a change that fails part way puts the file back as it was
	write := func(off bool) error {
		if err := edit.Atomically(func() error { return set(off) }, f.path); err != nil || off {
			return err
		}
		st, err := get()
		if err != nil {
			return err
		}
		if st.Off {
			return fmt.Errorf("%s turns it off itself (%s); magpie leaves a value of yours as it is (take it out there)", at.native(f.path), strings.Join(st.Own, ", "))
		}
		return nil
	}
	return &Telemetry{Get: get, Set: write, all: stashKey + ".all"}
}

// jsonTelemetryFile is a JSON or JSONC config's keys, by dotted path.
func jsonTelemetryFile(path string) telemetryFile {
	return telemetryFile{
		path: path,
		get: func(k telemetryKey) (any, bool, error) {
			// read again only once the file changes: the Agents page asks
			// on each poll
			doc, err := filememo.ReadNow("telemetry json", path, func(raw []byte) (any, error) {
				b := jsonc.ToJSON(edit.TrimBOM(raw))
				if len(strings.TrimSpace(string(b))) == 0 {
					return nil, nil
				}
				var doc any
				err := json.Unmarshal(b, &doc)
				return doc, err
			})
			if errors.Is(err, fs.ErrNotExist) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", path, err)
			}
			for _, part := range strings.Split(k.name, ".") {
				m, ok := doc.(map[string]any)
				if !ok {
					return nil, false, nil
				}
				if doc, ok = m[part]; !ok {
					return nil, false, nil
				}
			}
			return doc, true, nil
		},
		set: func(k telemetryKey) error { return edit.SetJSON(path, edit.KV{Path: k.name, Value: k.value}) },
		del: func(k telemetryKey) error { return edit.DelJSON(path, k.name) },
	}
}

// topTelemetryFile is a file whose keys sit at the top level, TOML or
// YAML (the Kimi and Goose switches): parse reads it strictly, so a file
// that doesn't parse is unknown and nothing is written to it.
func topTelemetryFile(path, tag string, parse func([]byte, *map[string]any) error,
	set func(path string, k telemetryKey) error, del func(path, name string) error) telemetryFile {
	return telemetryFile{
		path: path,
		get: func(k telemetryKey) (any, bool, error) {
			m, err := filememo.ReadNow(tag, path, func(raw []byte) (map[string]any, error) {
				m := map[string]any{}
				if len(bytes.TrimSpace(edit.TrimBOM(raw))) == 0 {
					return m, nil
				}
				if err := parse(edit.TrimBOM(raw), &m); err != nil {
					return nil, err
				}
				return m, nil
			})
			if errors.Is(err, fs.ErrNotExist) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", path, err)
			}
			v, has := m[k.name]
			return v, has, nil
		},
		set: func(k telemetryKey) error { return set(path, k) },
		del: func(k telemetryKey) error { return del(path, k.name) },
	}
}

// tomlTopTelemetryFile is a TOML config's top-level keys.
func tomlTopTelemetryFile(path string) telemetryFile {
	return topTelemetryFile(path, "telemetry toml",
		func(b []byte, m *map[string]any) error { return toml.Unmarshal(b, m) },
		func(path string, k telemetryKey) error {
			return edit.SetTOMLTop(path, edit.KV{Path: k.name, Value: k.value})
		},
		func(path, name string) error { return edit.DelTOMLTop(path, name) })
}

// yamlTopTelemetryFile is a YAML config's top-level keys.
func yamlTopTelemetryFile(path string) telemetryFile {
	return topTelemetryFile(path, "telemetry yaml",
		func(b []byte, m *map[string]any) error { return yaml.Unmarshal(b, m) },
		func(path string, k telemetryKey) error {
			return edit.SetYAMLTop(path, edit.KV{Path: k.name, Value: k.value})
		},
		func(path, name string) error { return edit.DelYAMLTop(path, name) })
}

// codexTelemetry is Codex's: [analytics] enabled = false in config.toml.
func codexTelemetry(at place, path string) *Telemetry {
	f := telemetryFile{
		path: path,
		get: func(k telemetryKey) (any, bool, error) {
			doc, err := codexTopOf(path)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", path, err)
			}
			table, key, _ := strings.Cut(k.name, ".")
			var m map[string]any
			switch table {
			case "analytics":
				if doc.Analytics == nil {
					return nil, false, nil
				}
				var ok bool
				if m, ok = doc.Analytics.(map[string]any); !ok {
					return nil, false, fmt.Errorf("%s: analytics is not a table", path)
				}
			}
			v, has := m[key]
			return v, has, nil
		},
		set: func(k telemetryKey) error {
			table, key, _ := strings.Cut(k.name, ".")
			// written inline or as dotted keys, the table is the user's
			// spelling, where a [analytics] of magpie's would define it
			// twice
			if doc, err := codexTopOf(path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			} else if t, err := edit.GetTOMLTable(path, table); err != nil {
				return err
			} else if t == nil && doc.Analytics != nil {
				return fmt.Errorf("%s has %s written in a way magpie doesn't add to; set %s there", at.native(path), table, k.name)
			}
			return edit.SetTOMLKey(path, table, key, k.value)
		},
		del: func(k telemetryKey) error {
			table, key, _ := strings.Cut(k.name, ".")
			return edit.DelTOMLKey(path, table, key)
		},
	}
	return telemetrySwitch(at, f, at.key("codex.telemetry"), telemetryKey{name: "analytics.enabled", value: false, off: isFalse})
}

// claudeTelemetry is Claude Code's: its telemetry and error reports off in
// settings.json's env.
func claudeTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, jsonTelemetryFile(path), at.key("claude.telemetry"),
		telemetryKey{name: "env.DISABLE_TELEMETRY", value: "1", off: anyValue},
		telemetryKey{name: "env.DISABLE_ERROR_REPORTING", value: "1", off: anyValue})
}

// usageStatsTelemetry is Gemini CLI's and Qwen Code's: their usage
// statistics off in settings.json.
func usageStatsTelemetry(at place, path, id string) *Telemetry {
	return telemetrySwitch(at, jsonTelemetryFile(path), at.key(id+".telemetry"),
		telemetryKey{name: "privacy.usageStatisticsEnabled", value: false, off: isFalse})
}

// crushTelemetry is Crush's: options.disable_metrics in crush.json.
func crushTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, jsonTelemetryFile(path), at.key("crush.telemetry"),
		telemetryKey{name: "options.disable_metrics", value: true, off: isTrue})
}

// kimiTelemetry is Kimi Code's: telemetry in config.toml.
func kimiTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, tomlTopTelemetryFile(path), at.key("kimi.telemetry"),
		telemetryKey{name: "telemetry", value: false, off: isFalse})
}

// gooseTelemetry is Goose's: GOOSE_TELEMETRY_ENABLED in config.yaml.
func gooseTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, yamlTopTelemetryFile(path), at.key("goose.telemetry"),
		telemetryKey{name: "GOOSE_TELEMETRY_ENABLED", value: false, off: isFalse})
}

// piTelemetry is Pi's: its install and update reporting in settings.json.
func piTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, jsonTelemetryFile(path), at.key("pi.telemetry"),
		telemetryKey{name: "enableInstallTelemetry", value: false, off: isFalse})
}

// clineTelemetry is Cline CLI's: telemetryOptOut in its global settings.
func clineTelemetry(at place, path string) *Telemetry {
	return telemetrySwitch(at, jsonTelemetryFile(path), at.key("cline.telemetry"),
		telemetryKey{name: "telemetryOptOut", value: true, off: isTrue})
}

// ErrNoTelemetry: the agent has no telemetry switch magpie knows.
var ErrNoTelemetry = errors.New("magpie knows no switch for this agent's telemetry")

// SetTelemetry turns a's own telemetry off, or back to what it was.
func (a *Agent) SetTelemetry(off bool) error {
	if a.Telemetry == nil {
		return ErrNoTelemetry
	}
	// the switch for every agent holds each one's row while it is on
	telemetryAllMu.Lock()
	defer telemetryAllMu.Unlock()
	if s, err := settings.SharedChecked(); err != nil {
		return err
	} else if s.AgentTelemetryOff {
		return ErrTelemetryAllOff
	}
	return a.Telemetry.Set(off)
}
