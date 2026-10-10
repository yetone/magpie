package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

// StepCode keeps its configuration in ~/.stepcode/config.toml (or
// the parent of an absolute STEP_CODING_AGENT_DIR) and its models in
// models.json using Pi's provider format.
// magpie manages defaultProvider and defaultModel in config.toml, and one
// providers.magpie entry in models.json.

func stepcode(home string) *Agent { return stepcodeIn(here(home)) }

func stepcodeDir(at place) string {
	if d := strings.TrimSpace(at.getenv("STEP_CODING_AGENT_DIR")); filepath.IsAbs(d) {
		return filepath.Dir(filepath.Clean(d))
	}
	name := ".stepcode"
	if at.spell == nil {
		if custom := strings.TrimSpace(at.getenv("STEPCODE_CONFIG_DIR")); custom != "" {
			name = custom
		}
	}
	return filepath.Join(at.home, name)
}

func stepcodeProvider(at place) any {
	ms := make([]map[string]any, 0)
	for _, m := range magpieModels("stepcode") {
		ms = append(ms, piModelJSON(m, at.gw(), true))
	}
	return map[string]any{
		"name":    "magpie",
		"baseUrl": at.v1(),
		"api":     "openai-completions",
		"apiKey":  agentKeyAt("stepcode", at.gw()),
		"models":  ms,
	}
}

type stepcodeTOML struct {
	Provider *string   `toml:"defaultProvider"`
	Model    *string   `toml:"defaultModel"`
	Scope    *[]string `toml:"enabledModels"`
}

func readStepcodeTOML(path string) (stepcodeTOML, error) {
	var out stepcodeTOML
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return out, nil
	}
	if err := toml.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func stepcodeValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stepcodeSetString(path, key string, val *string) error {
	if val == nil {
		return edit.DelTOMLTop(path, key)
	}
	return edit.SetTOMLTopPreserving(path, edit.KV{Path: key, Value: *val})
}

func stepcodeScopeRaw(pats []string) (edit.Raw, error) {
	b, err := toml.Marshal(map[string]any{"enabledModels": pats})
	if err != nil {
		return "", err
	}
	_, rhs, ok := strings.Cut(string(b), "=")
	if !ok {
		return "", fmt.Errorf("unexpected toml output for enabledModels: %s", string(b))
	}
	return edit.Raw(strings.TrimSpace(rhs)), nil
}

func stepcodeValidateModels(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	normalized := jsonc.ToJSON(b)
	if !gjson.ValidBytes(normalized) {
		return fmt.Errorf("%s: invalid JSON", path)
	}
	root := gjson.ParseBytes(normalized)
	if !root.IsObject() {
		return fmt.Errorf("%s: root must be a JSON object", path)
	}
	if prov := root.Get("providers"); prov.Exists() {
		if !prov.IsObject() {
			return fmt.Errorf("%s: 'providers' must be a JSON object", path)
		}
		if mag := prov.Get(magpieID); mag.Exists() {
			if !mag.IsObject() {
				return fmt.Errorf("%s: 'providers.%s' must be a JSON object", path, magpieID)
			}
		}
	}
	return nil
}

func stepcodeJSONEmpty(b []byte) bool {
	if len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	if bytes.Contains(b, []byte("//")) || bytes.Contains(b, []byte("/*")) {
		return false
	}
	normalized := jsonc.ToJSON(b)
	res := gjson.ParseBytes(normalized)
	if !res.IsObject() {
		return false
	}
	return len(res.Map()) == 0
}

func stepcodeFileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

type stepcodeState struct {
	OrigProvider        *string  `json:"orig_provider,omitempty"`
	OrigModel           *string  `json:"orig_model,omitempty"`
	OrigHasScope        bool     `json:"orig_has_scope"`
	OrigScope           []string `json:"orig_scope,omitempty"`
	LastHasScope        bool     `json:"last_has_scope"`
	LastScope           []string `json:"last_scope,omitempty"`
	AppendedScope       []string `json:"appended_scope,omitempty"`
	OrigMagpieRaw       *string  `json:"orig_magpie_raw,omitempty"`
	ConfigFileExisted   bool     `json:"config_file_existed"`
	ModelsFileExisted   bool     `json:"models_file_existed"`
	ProvidersMapExisted bool     `json:"providers_map_existed"`
}

func stepcodeReadStash() (map[string]string, error) {
	b, err := os.ReadFile(stashPath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stepcode stash: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("stepcode stash: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("stepcode stash: expected an object")
	}
	return m, nil
}

func loadStepcodeState(at place) (*stepcodeState, error) {
	m, err := stepcodeReadStash()
	if err != nil {
		return nil, err
	}
	raw, ok := m[at.key("stepcode.wiring")]
	if !ok {
		return nil, nil
	}
	var state *stepcodeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("stepcode wiring state: %w", err)
	}
	if state == nil {
		return nil, fmt.Errorf("stepcode wiring state: expected an object")
	}
	return state, nil
}

func stepcodeStoreState(at place, state *stepcodeState) error {
	m, err := stepcodeReadStash()
	if err != nil {
		return err
	}
	key := at.key("stepcode.wiring")
	if state == nil {
		delete(m, key)
	} else {
		b, err := json.Marshal(state)
		if err != nil {
			return err
		}
		m[key] = string(b)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stashPath()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(stashPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if err := f.Close(); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	return edit.WriteAtomic(stashPath(), data)
}

func stepcodeStartup(cfg stepcodeTOML) string {
	p := stepcodeValue(cfg.Provider)
	m := stepcodeValue(cfg.Model)
	def := ""
	switch {
	case m == "":
		def = ""
	case p == "":
		def = m
	default:
		def = p + "/" + m
	}
	if cfg.Scope == nil || len(*cfg.Scope) == 0 {
		return def
	}
	scope := *cfg.Scope
	if def != "" && slices.ContainsFunc(scope, func(pat string) bool { return piTakes(pat, def) }) {
		return def
	}
	first := strings.TrimSpace(scope[0])
	if i := strings.LastIndex(first, ":"); i >= 0 && slices.Contains(piLevels, first[i+1:]) {
		first = first[:i]
	}
	if strings.ContainsAny(first, "*?[") || !strings.Contains(first, "/") {
		return def
	}
	return first
}

func stepcodeIn(at place) *Agent {
	dir := stepcodeDir(at)
	path := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")
	authPath := filepath.Join(dir, "auth.json")

	block := theirsKept(modelsPath, "providers."+magpieID, func() any { return stepcodeProvider(at) }, "models")

	startup := func() string {
		curTOML, err := readStepcodeTOML(path)
		if err != nil {
			return ""
		}
		return stepcodeStartup(curTOML)
	}

	unwire := func() error {
		state, err := loadStepcodeState(at)
		if err != nil {
			return err
		}
		if state == nil {
			return nil
		}
		curTOML, err := readStepcodeTOML(path)
		if err != nil {
			return err
		}
		if err := stepcodeValidateModels(modelsPath); err != nil {
			return err
		}

		if stepcodeValue(curTOML.Provider) == magpieID {
			if err := stepcodeSetString(path, "defaultProvider", state.OrigProvider); err != nil {
				return err
			}
			if err := stepcodeSetString(path, "defaultModel", state.OrigModel); err != nil {
				return err
			}
		}

		curTOML, err = readStepcodeTOML(path)
		if err != nil {
			return err
		}
		curHasScope := (curTOML.Scope != nil)
		var curList []string
		if curHasScope {
			curList = *curTOML.Scope
		}

		if curHasScope == state.LastHasScope && slices.Equal(curList, state.LastScope) {
			if state.OrigHasScope {
				raw, err := stepcodeScopeRaw(state.OrigScope)
				if err != nil {
					return err
				}
				if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "enabledModels", Value: raw}); err != nil {
					return err
				}
			} else {
				if err := edit.DelTOMLTop(path, "enabledModels"); err != nil {
					return err
				}
			}
		} else if curHasScope && len(state.AppendedScope) > 0 {
			kept := slices.DeleteFunc(slices.Clone(curList), func(p string) bool {
				return slices.Contains(state.AppendedScope, p)
			})
			if !slices.Equal(kept, curList) {
				raw, err := stepcodeScopeRaw(kept)
				if err != nil {
					return err
				}
				if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "enabledModels", Value: raw}); err != nil {
					return err
				}
			}
		}

		if state.OrigMagpieRaw != nil {
			if err := edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: json.RawMessage(*state.OrigMagpieRaw)}); err != nil {
				return err
			}
		} else {
			if err := edit.DelJSON(modelsPath, "providers."+magpieID); err != nil {
				return err
			}
			if !state.ProvidersMapExisted {
				if rawP, ok := edit.GetJSON(modelsPath, "providers"); ok && strings.TrimSpace(rawP) == "{}" {
					if err := edit.DelJSON(modelsPath, "providers"); err != nil {
						return err
					}
				}
			}
		}

		if !state.ModelsFileExisted {
			if b, err := os.ReadFile(modelsPath); err == nil {
				if stepcodeJSONEmpty(b) {
					if err := edit.Remove(modelsPath); err != nil {
						return err
					}
				}
			}
		}
		if !state.ConfigFileExisted {
			if b, err := os.ReadFile(path); err == nil {
				if len(bytes.TrimSpace(b)) == 0 {
					if err := edit.Remove(path); err != nil {
						return err
					}
				}
			}
		}

		return stepcodeStoreState(at, nil)
	}

	a := &Agent{
		ID:      "stepcode",
		Name:    "StepCode",
		Icon:    "stepfun-color",
		Bin:     "step",
		Aliases: []string{"step"},
		Dir:     dir,
		Path:    path,
		Spelled: prefixed,
		UA:      []string{"stepcode"},
		detect: func() bool {
			return agentDir(dir)
		},
		Beside: func() bool {
			s, err := loadStepcodeState(at)
			return err == nil && s != nil
		},
		Unwire: unwire,
		Check: func() string {
			cur, err := readStepcodeTOML(path)
			if err != nil {
				return "StepCode's config (config.toml) is unreadable or malformed"
			}
			if err := stepcodeValidateModels(modelsPath); err != nil {
				return "StepCode's models.json is unreadable or malformed"
			}
			if stepcodeValue(cur.Provider) != magpieID {
				return ""
			}
			return wiringOff("StepCode", modelsPath, func(k string) (string, bool) {
				return edit.GetJSON(modelsPath, "providers."+magpieID+"."+k)
			}, "baseUrl", at.v1(), "apiKey", agentKeyAt("stepcode", at.gw()))
		},
		Sync: func() error {
			state, err := loadStepcodeState(at)
			if err != nil {
				return err
			}
			if state == nil {
				return nil
			}
			if err := stepcodeValidateModels(modelsPath); err != nil {
				return err
			}
			return syncJSON(modelsPath, "providers."+magpieID, block)
		},
		Fields: []Field{
			{
				Key:   "model",
				Label: "model",
				Get:   startup,
				Set: func(v string) error {
					if v == "" {
						return unwire()
					}
					ref, isMag := strings.CutPrefix(v, magpieID+"/")
					if !isMag || !isMagpie(ref) {
						p, m, ok := strings.Cut(v, "/")
						if !ok || p == "" || m == "" {
							return fmt.Errorf("expected provider/model, got %q", v)
						}
						if err := unwire(); err != nil {
							return err
						}
						cur, err := readStepcodeTOML(path)
						if err != nil {
							return err
						}
						if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "defaultProvider", Value: p}, edit.KV{Path: "defaultModel", Value: m}); err != nil {
							return err
						}
						if cur.Scope != nil && len(*cur.Scope) > 0 {
							if !slices.ContainsFunc(*cur.Scope, func(pat string) bool { return piTakes(pat, v) }) {
								newScope := append(*cur.Scope, v)
								raw, err := stepcodeScopeRaw(newScope)
								if err != nil {
									return err
								}
								if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "enabledModels", Value: raw}); err != nil {
									return err
								}
							}
						}
						return nil
					}

					curTOML, err := readStepcodeTOML(path)
					if err != nil {
						return err
					}
					if err := stepcodeValidateModels(modelsPath); err != nil {
						return err
					}
					state, err := loadStepcodeState(at)
					if err != nil {
						return err
					}
					if state == nil {
						state = &stepcodeState{}
						state.ConfigFileExisted = stepcodeFileExists(path)
						state.ModelsFileExisted = stepcodeFileExists(modelsPath)
						if rawP, ok := edit.GetJSON(modelsPath, "providers"); ok && rawP != "" {
							state.ProvidersMapExisted = true
						}
						if rawMagpie, ok := edit.GetJSON(modelsPath, "providers."+magpieID); ok {
							state.OrigMagpieRaw = &rawMagpie
						}
						state.OrigProvider = curTOML.Provider
						state.OrigModel = curTOML.Model
						if curTOML.Scope != nil {
							state.OrigHasScope = true
							state.OrigScope = slices.Clone(*curTOML.Scope)
						}
					} else {
						if stepcodeValue(curTOML.Provider) != magpieID {
							state.OrigProvider = curTOML.Provider
							state.OrigModel = curTOML.Model
						}
						curHasScope := (curTOML.Scope != nil)
						var curList []string
						if curHasScope {
							curList = *curTOML.Scope
						}
						scopeChanged := false
						if curHasScope != state.LastHasScope {
							scopeChanged = true
						} else if curHasScope && !slices.Equal(curList, state.LastScope) {
							scopeChanged = true
						}
						if scopeChanged {
							state.OrigHasScope = curHasScope
							if curHasScope {
								filtered := slices.DeleteFunc(slices.Clone(curList), func(p string) bool {
									return slices.Contains(state.AppendedScope, p)
								})
								state.OrigScope = filtered
							} else {
								state.OrigScope = nil
							}
						}
					}

					// Update target scope
					if curTOML.Scope != nil && len(*curTOML.Scope) > 0 {
						pats := *curTOML.Scope
						if !slices.ContainsFunc(pats, func(p string) bool { return piTakes(p, v) }) {
							newScope := append(slices.Clone(pats), v)
							state.AppendedScope = append(state.AppendedScope, v)
							state.LastHasScope = true
							state.LastScope = newScope
						} else {
							state.LastHasScope = true
							state.LastScope = slices.Clone(pats)
						}
					} else {
						state.LastHasScope = (curTOML.Scope != nil)
						if curTOML.Scope != nil {
							state.LastScope = slices.Clone(*curTOML.Scope)
						} else {
							state.LastScope = nil
						}
					}

					// Save state before config edits
					if err := stepcodeStoreState(at, state); err != nil {
						return err
					}

					if err := edit.SetJSON(modelsPath, edit.KV{Path: "providers." + magpieID, Value: block()}); err != nil {
						return err
					}
					if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "defaultProvider", Value: magpieID}, edit.KV{Path: "defaultModel", Value: ref}); err != nil {
						return err
					}
					if state.LastHasScope && (curTOML.Scope == nil || !slices.Equal(*curTOML.Scope, state.LastScope)) {
						raw, err := stepcodeScopeRaw(state.LastScope)
						if err != nil {
							return err
						}
						if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "enabledModels", Value: raw}); err != nil {
							return err
						}
					}

					return nil
				},
				Options: func(cur map[string]string) []Option {
					return append(ownOptions(authPath, cur["model"]), viaMagpie("stepcode", magpieID+"/")...)
				},
			},
		},
	}

	return atomic(a, path, modelsPath, stashPath())
}
