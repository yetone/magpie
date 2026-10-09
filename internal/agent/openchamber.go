package agent

// OpenChamber (a desktop, web and VS Code front end for OpenCode, #321) runs
// an OpenCode of its own — the desktop app bundles OpenCode 2 — on the
// user's OpenCode config: $OPENCODE_CONFIG_DIR, else
// $XDG_CONFIG_HOME/opencode (openCodeDir). Its own settings are under
// $OPENCHAMBER_DATA_DIR, ~/.config/openchamber by default (not moved by
// XDG_CONFIG_HOME):
//
//	preferences.json  {"version":1,"fields":{"<key>":{"value":…,"updatedAt":<ms>}}}: the profile
//	settings.json     the instance's facts, plus a copy of the profile's values for older builds
//
// Each new session is sent the model it picks, in this order: the project's
// defaultModel (settings.json projects[]), the profile's defaultModel, the
// agent's own, and only then OpenCode's model — so a defaultModel of its own
// wins over the one magpie sets for OpenCode. Its small model (titles, commit
// messages) is smallModelOverride once smallModelUseDefault is false, else
// OpenCode's small_model. A model is "provider/model" as OpenCode names it,
// so one of magpie's needs magpie's provider in the OpenCode config.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// openChamberDir is where OpenChamber keeps its settings.
func openChamberDir(home string) string {
	if d := appdir.Getenv("OPENCHAMBER_DATA_DIR"); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
	}
	return filepath.Join(home, ".config", "openchamber")
}

// ocStore reads and writes OpenChamber's profile as it does: a key in
// preferences.json wins over its copy in settings.json, which is all a
// build from before the split reads, and seeds preferences.json when that
// is missing.
type ocStore struct{ dir string }

func (s ocStore) prefs() string    { return filepath.Join(s.dir, "preferences.json") }
func (s ocStore) settings() string { return filepath.Join(s.dir, "settings.json") }

// hasPrefs says whether preferences.json is there; an error when it is
// but isn't a version-1 document, which OpenChamber doesn't write to either.
func (s ocStore) hasPrefs() (bool, error) {
	b, err := os.ReadFile(s.prefs())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var doc struct {
		Version int             `json:"version"`
		Fields  json.RawMessage `json:"fields"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Version != 1 || len(doc.Fields) == 0 || doc.Fields[0] != '{' {
		return false, fmt.Errorf("OpenChamber's %s isn't a preferences file it can read; fix or remove it first", s.prefs())
	}
	return true, nil
}

func (s ocStore) get(key string) string {
	if v, ok := edit.GetJSON(s.prefs(), "fields."+key+".value"); ok {
		return v
	}
	v, _ := edit.GetJSON(s.settings(), key)
	return v
}

// set writes key = v, or takes key out for a nil v, in both files: the
// copy in settings.json only where that file is, or the profile has
// nowhere else to go.
func (s ocStore) set(kvs map[string]any) error {
	has, err := s.hasPrefs()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(s.settings())
	for key, v := range kvs {
		if has {
			if v == nil {
				err = edit.DelJSON(s.prefs(), "fields."+key)
			} else {
				err = edit.SetJSON(s.prefs(), edit.KV{Path: "fields." + key + ".value", Value: v},
					edit.KV{Path: "fields." + key + ".updatedAt", Value: time.Now().UnixMilli()})
			}
			if err != nil {
				return err
			}
		}
		if has && statErr != nil {
			continue
		}
		if v == nil {
			err = edit.DelJSON(s.settings(), key)
		} else {
			err = edit.SetJSON(s.settings(), edit.KV{Path: key, Value: v})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// model and small are the models OpenChamber sends, "" when it leaves them
// to OpenCode's config.
func (s ocStore) model() string { return s.get("defaultModel") }
func (s ocStore) small() string {
	if s.get("smallModelUseDefault") != "false" {
		return ""
	}
	return s.get("smallModelOverride")
}

// projectModels are the projects that pick a default model of their own,
// name → model.
func (s ocStore) projectModels() map[string]string {
	raw, ok := edit.GetJSON(s.settings(), "projects")
	if !ok {
		return nil
	}
	var ps []struct {
		Label, Path, DefaultModel string
	}
	json.Unmarshal([]byte(raw), &ps)
	out := map[string]string{}
	for _, p := range ps {
		if p.DefaultModel == "" {
			continue
		}
		name := p.Label
		if name == "" {
			name = filepath.Base(p.Path)
		}
		out[name] = p.DefaultModel
	}
	return out
}

// openChamberOnMagpie says whether OpenChamber sends one of magpie's
// models: OpenCode's config must keep magpie's provider for it.
func openChamberOnMagpie() bool {
	home, _ := os.UserHomeDir()
	s := ocStore{openChamberDir(home)}
	return usesMagpie(s.model(), s.small())
}

func openChamber(home, cfg string) *Agent {
	s := ocStore{openChamberDir(home)}
	oc := opencode(home, cfg)
	ocPath := oc.Path
	auth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	provider := theirsKept(ocPath, "provider."+magpieID, func() any { return magpieProviderJSONFor("opencode", "opencode") }, "models")
	opts := func(key string) func(map[string]string) []Option {
		return func(cur map[string]string) []Option {
			return append(ownOptions(auth, cur[key]), viaMagpie("opencode", magpieID+"/")...)
		}
	}
	// stepped off magpie: its provider goes too, when nothing else in the
	// OpenCode config names one of its models
	tidy := func() error {
		m, _ := edit.GetJSON(ocPath, "model")
		sm, _ := edit.GetJSON(ocPath, "small_model")
		if usesMagpie(m, sm, s.model(), s.small()) {
			return nil
		}
		return edit.DelJSON(ocPath, "provider."+magpieID)
	}
	return &Agent{
		ID: "openchamber", Name: "OpenChamber", Icon: "openchamber", Aliases: []string{"chamber"}, Spelled: openCodeSpelled(ocPath, gatewayV1),
		// its model requests are its OpenCode's, and counted as OpenCode's
		Bin: "openchamber", Dir: s.dir, Path: s.prefs(),
		Notice: func() string {
			var notes []string
			if usesMagpie(s.model()) {
				ps := s.projectModels()
				for _, name := range slices.Sorted(maps.Keys(ps)) {
					if m := ps[name]; !usesMagpie(m) {
						notes = append(notes, fmt.Sprintf("OpenChamber's project %s has a default model of its own (%s), which it uses there instead.", name, m))
					}
				}
			}
			if Running(`OpenChamber\.app/`, `(^|/)openchamber( |$)`) {
				notes = append(notes, "OpenChamber takes the new model for new sessions; sessions already open keep theirs.")
			}
			return strings.Join(notes, " ")
		},
		Check: func() string {
			if !usesMagpie(s.model(), s.small()) {
				return ""
			}
			return wiringOff("OpenChamber", ocPath, func(k string) (string, bool) { return edit.GetJSON(ocPath, "provider."+magpieID+".options."+k) },
				"baseURL", gatewayV1(), "apiKey", gateway.Token)
		},
		Sync: func() error {
			if _, ok := edit.GetJSON(ocPath, "provider."+magpieID); !ok && usesMagpie(s.model(), s.small()) {
				return edit.SetJSON(ocPath, edit.KV{Path: "provider." + magpieID, Value: provider()})
			}
			return syncJSONInOrder(ocPath, "provider."+magpieID, provider)
		},
		Fields: []Field{
			{Key: "model", Label: "model", Get: s.model, Options: opts("model"), Set: func(v string) error {
				if v == "" {
					if err := s.set(map[string]any{"defaultModel": nil, "defaultVariant": nil}); err != nil {
						return err
					}
					return tidy()
				}
				v, err := openCodeRef(ocPath, "opencode", v)
				if err != nil {
					return err
				}
				kv := map[string]any{"defaultModel": v}
				// the variant was the old model's, as OpenChamber's own
				// picker has it
				if v != s.model() {
					kv["defaultVariant"] = nil
				}
				return s.set(kv)
			}},
			{Key: "small", Label: "small", Get: s.small, Options: opts("small"), Set: func(v string) error {
				if v == "" {
					if err := s.set(map[string]any{"smallModelUseDefault": nil, "smallModelOverride": nil}); err != nil {
						return err
					}
					return tidy()
				}
				v, err := openCodeRef(ocPath, "opencode", v)
				if err != nil {
					return err
				}
				return s.set(map[string]any{"smallModelUseDefault": false, "smallModelOverride": v})
			}},
		},
	}
}
