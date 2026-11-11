package agent

// Paseo (getpaseo/paseo, an app that runs Claude Code, Codex and others
// for you) starts the same Claude Code, which reads ~/.claude/settings.json
// and its modelPicker. Paseo lists models itself, though: its own first-party
// list plus the settings.json `model` and a few ANTHROPIC_*_MODEL tier
// variables (packages/server/src/server/agent/providers/claude/models.ts),
// never modelPicker. So the models magpie serves besides the main one and
// the tiers were not in Paseo's picker (@TheMist016 on X). Paseo takes more
// from its own ~/.paseo/config.json, agents.providers.claude.additionalModels,
// [{id,label,description}], appended to what it finds, each id passed to
// Claude Code as written (docs/custom-providers.md, ProviderProfileModelSchema).
//
// So the switch writes the models Claude Code's /model lists (the same ids,
// [1m] marks and names as claudeModelPicker) there, and takes out only its
// own entries on off, the user's staying. Claude Code itself must be
// connected to magpie for those ids to be served.

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

const paseoMark = "via magpie"

// paseoModels are magpie's models as Paseo's additionalModels entries.
func paseoModels() []map[string]any {
	mark1M := claude1MFor("paseo")
	labels := listLabels("paseo")
	rows := []map[string]any{}
	seen := map[string]bool{}
	for _, o := range viaMagpie("paseo", "") {
		id := provider.ClaudeSpelled(mark1M(o.Value))
		if seen[id] {
			continue
		}
		seen[id] = true
		note := strings.TrimSuffix(o.Note, " · "+paseoMark)
		desc := paseoMark
		if note != "" && note != paseoMark {
			desc = note + " · " + paseoMark
		}
		rows = append(rows, map[string]any{"id": id, "label": cmp.Or(labels[o.Ref], o.Label, o.Value), "description": desc})
	}
	return rows
}

// paseoOurs says an entry of additionalModels is magpie's.
func paseoOurs(e map[string]any) bool {
	d, _ := e["description"].(string)
	return d == paseoMark || strings.HasSuffix(d, " · "+paseoMark)
}

// paseoList is the file's additionalModels, and whether it could be read:
// a list that can't be is left as it is.
func paseoList(path string) (list []map[string]any, ok bool) {
	raw, has := edit.GetJSONRaw(path, paseoKey)
	if !has {
		return nil, true
	}
	return list, json.Unmarshal([]byte(raw), &list) == nil
}

const paseoKey = "agents.providers.claude.additionalModels"

// paseoWrite puts magpie's entries (none when on is false) among the
// user's, which keep their place and come first.
func paseoWrite(path string, on bool) error {
	cur, ok := paseoList(path)
	if !ok {
		return nil
	}
	mine := map[string]bool{}
	var out []map[string]any
	for _, e := range cur {
		if !paseoOurs(e) {
			out = append(out, e)
			if id, _ := e["id"].(string); id != "" {
				mine[id] = true
			}
		}
	}
	if on {
		for _, e := range paseoModels() {
			// an id the user listed themselves stays theirs
			if !mine[e["id"].(string)] {
				out = append(out, e)
			}
		}
	}
	if len(out) == 0 {
		if _, has := edit.GetJSONRaw(path, paseoKey); !has {
			return nil
		}
		return edit.DelJSON(path, paseoKey)
	}
	if raw, has := edit.GetJSONRaw(path, paseoKey); has && sameJSON(raw, out) {
		return nil
	}
	return edit.SetJSON(path, edit.KV{Path: paseoKey, Value: out})
}

func paseo(home string) *Agent {
	dir := os.Getenv("PASEO_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".paseo")
	}
	path := filepath.Join(dir, "config.json")
	wired := func() bool {
		l, _ := paseoList(path)
		for _, e := range l {
			if paseoOurs(e) {
				return true
			}
		}
		return false
	}
	return &Agent{
		ID: "paseo", Name: "Paseo", Icon: "generic", Aliases: []string{"getpaseo"},
		Dir: dir, Path: path,
		detect: func() bool { return isDir(dir) },
		Notice: func() string {
			if runtime.GOOS == "windows" || Running(`(^|/)[Pp]aseo( |$|\.)`, `Paseo\.app/`) {
				return noticePaseo.String()
			}
			return ""
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			return paseoWrite(path, true)
		},
		ListsModels: true,
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error { return paseoWrite(path, v != "") },
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in Paseo's Claude model picker"}}
			},
		}},
	}
}

var noticePaseo = newNotice("Paseo reads its model list when its daemon starts — restart Paseo's daemon to see magpie's models. Claude Code must be connected to magpie too.")
