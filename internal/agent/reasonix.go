package agent

// Reasonix Studio 2.x and its CLI share config.toml and .env under REASONIX_HOME,
// ~/.reasonix, or %APPDATA%/reasonix on Windows. Magpie owns one [[providers]]
// entry, never the user's other providers. Project/session overrides stay theirs.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

const reasonixKey = "MAGPIE_REASONIX_API_KEY"

var reasonixKeyLine = regexp.MustCompile(`(?m)^\s*(?:export\s+)?` + reasonixKey + `\s*=`)

// Every API request and catalog sync constructs its own Agent. The transaction
// must cover the whole edit and rollback across those instances, including reads.
var reasonixEdits sync.RWMutex

type reasonixConfig struct {
	DefaultModel *string `toml:"default_model"`
	Agent        struct {
		Planner *string `toml:"planner_model"`
	} `toml:"agent"`
	Providers []reasonixProvider `toml:"providers"`
	Desktop   struct {
		Access []string `toml:"provider_access"`
	} `toml:"desktop"`
}

type reasonixProvider struct {
	Name      string                   `toml:"name"`
	Kind      string                   `toml:"kind"`
	BaseURL   string                   `toml:"base_url"`
	APIKeyEnv string                   `toml:"api_key_env"`
	Model     string                   `toml:"model,omitempty"`
	Models    []string                 `toml:"models,omitempty"`
	Default   string                   `toml:"default,omitempty"`
	Effort    string                   `toml:"effort,omitempty"`
	Protocol  string                   `toml:"reasoning_protocol,omitempty"`
	NoProxy   bool                     `toml:"no_proxy,omitempty"`
	Overrides map[string]reasonixModel `toml:"model_overrides,omitempty"`
}

type reasonixModel struct {
	Protocol string   `toml:"reasoning_protocol,omitempty"`
	Efforts  []string `toml:"supported_efforts,omitempty"`
	Vision   *bool    `toml:"vision,omitempty"`
	Context  int      `toml:"context_window,omitempty"`
	Output   int      `toml:"max_output_tokens,omitempty"`
}

// The journal contains settings, never an upstream credential. A separate file
// per home makes portable Studio instances independent and participates in rollback.
type reasonixState struct {
	Version        int     `json:"version"`
	Planner        *string `json:"planner_model,omitempty"`
	PlannerManaged bool    `json:"planner_managed,omitempty"`
	Default        *string `json:"default_model"`
	NewConfig      bool    `json:"new_config"`
	NewEnv         bool    `json:"new_env"`
	AddedAccess    bool    `json:"added_access"`
}

func reasonix(home string) *Agent { return reasonixIn(here(home)) }

// reasonixIn is Reasonix at a place: this machine's home, or a WSL
// distro's (see wsl.go), where its CLI keeps Linux's ~/.reasonix
// (REASONIX_HOME isn't read) and its provider names the gateway as the
// distro reaches it, with the key it takes from there.
func reasonixIn(at place) *Agent {
	goos, getenv := runtime.GOOS, os.Getenv
	if at.spell != nil {
		goos, getenv = "linux", func(string) string { return "" }
	}
	dir := reasonixHome(goos, at.home, getenv)
	gwKey := func() string { return agentKeyAt("reasonix", at.gw()) }
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	path, env := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	statePath := filepath.Join(filepath.Dir(provider.Path()), fmt.Sprintf("reasonix-%x.json", sum[:8]))
	load := func() (reasonixConfig, *reasonixState, error) {
		cfg, err := readReasonix(path)
		if err != nil {
			return cfg, nil, err
		}
		b, err := edit.Read(statePath)
		if err != nil {
			return cfg, nil, err
		}
		var state *reasonixState
		if b != nil {
			state = &reasonixState{}
			if err := json.Unmarshal(b, state); err != nil {
				return cfg, nil, fmt.Errorf("Reasonix restore record: %w", err)
			}
			if state.Version != 1 {
				return cfg, nil, fmt.Errorf("Reasonix restore record has an unsupported or missing version")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(b, &fields); err != nil {
				return cfg, nil, err
			}
			for _, key := range []string{"default_model", "new_config", "new_env", "added_access"} {
				value, present := fields[key]
				if !present || key != "default_model" && strings.TrimSpace(string(value)) == "null" {
					return cfg, nil, fmt.Errorf("Reasonix restore record is incomplete")
				}
			}
		}
		return cfg, state, nil
	}
	model := func() string {
		cfg, err := readReasonix(path)
		if err == nil && cfg.DefaultModel != nil {
			return *cfg.DefaultModel
		}
		return ""
	}
	checkOwner := func(cfg reasonixConfig, state *reasonixState) error {
		for _, p := range cfg.Providers {
			if p.Name == magpieID && (state == nil || p.APIKeyEnv != reasonixKey) {
				return fmt.Errorf("Reasonix already has a provider named magpie that magpie does not own")
			}
			if p.Name != magpieID && p.APIKeyEnv == reasonixKey {
				return fmt.Errorf("Reasonix's %s is used by another provider", reasonixKey)
			}
		}
		b, err := edit.Read(env)
		if err != nil {
			return err
		}
		// Studio's dotenv reader resolves duplicate assignments differently from
		// the editor. Ownership is ambiguous: do not replace or remove either.
		if len(reasonixKeyLine.FindAllIndex(b, -1)) > 1 {
			return fmt.Errorf("Reasonix credentials repeat %s", reasonixKey)
		}
		if _, exists := edit.GetEnvFile(env, reasonixKey); exists && state == nil {
			return fmt.Errorf("Reasonix's %s already exists; it was not created by magpie", reasonixKey)
		}
		return nil
	}
	writeProvider := func(p reasonixProvider) error {
		b, err := toml.Marshal(struct {
			Providers []reasonixProvider `toml:"providers"`
		}{[]reasonixProvider{p}})
		if err != nil {
			return err
		}
		return edit.SetTOMLArrayTable(path, "providers", "name", magpieID, string(b))
	}
	// Restore only what this adapter added. A later native selection and edits to
	// other providers, credentials, and the desktop access list survive.
	restore := func(cfg reasonixConfig, state *reasonixState, native string) error {
		if state == nil {
			if native == "" {
				return nil
			}
			return edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: native})
		}
		if err := checkOwner(cfg, state); err != nil {
			return err
		}
		if cfg.Agent.Planner != nil && usesMagpie(*cfg.Agent.Planner) {
			// Keep the connection while the independently selected Plan uses it.
			if native != "" {
				return edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: native})
			}
			if cfg.DefaultModel != nil && usesMagpie(*cfg.DefaultModel) {
				if state.Default != nil {
					return edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: *state.Default})
				}
				return edit.DelTOMLTop(path, "default_model")
			}
			return nil
		}
		if err := edit.DelTOMLArrayTable(path, "providers", "name", magpieID); err != nil {
			return err
		}
		if native != "" {
			if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: native}); err != nil {
				return err
			}
		} else if cfg.DefaultModel != nil && usesMagpie(*cfg.DefaultModel) {
			if state.Default != nil {
				if err := edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: *state.Default}); err != nil {
					return err
				}
			} else if err := edit.DelTOMLTop(path, "default_model"); err != nil {
				return err
			}
		}
		if state.AddedAccess && slices.Contains(cfg.Desktop.Access, magpieID) {
			access := slices.DeleteFunc(cfg.Desktop.Access, func(v string) bool { return v == magpieID })
			if err := edit.SetTOMLKey(path, "desktop", "provider_access", reasonixArray(access)); err != nil {
				return err
			}
		}
		if v, exists := edit.GetEnvFile(env, reasonixKey); exists && v == gwKey() {
			if err := edit.DelEnvFile(env, reasonixKey); err != nil {
				return err
			}
		}
		for _, f := range []struct {
			path string
			made bool
		}{{path, state.NewConfig}, {env, state.NewEnv}} {
			if _, err := os.Stat(f.path); os.IsNotExist(err) {
				continue
			}
			if b, err := edit.Read(f.path); err != nil {
				return err
			} else if f.made && len(strings.TrimSpace(string(b))) == 0 {
				if err := edit.Remove(f.path); err != nil {
					return err
				}
			}
		}
		return edit.Remove(statePath)
	}
	ensureGateway := func(cfg reasonixConfig, state **reasonixState, ref, selected string) error {
		currentState := *state
		models := magpieModels("reasonix")
		if !slices.ContainsFunc(models, func(m catalog.Model) bool { return m.ID == ref }) {
			return fmt.Errorf("magpie has no visible model %q", ref)
		}
		if err := checkOwner(cfg, currentState); err != nil {
			return err
		}
		if currentState == nil {
			_, cErr := os.Stat(path)
			_, eErr := os.Stat(env)
			currentState = &reasonixState{Version: 1, Default: cfg.DefaultModel, NewConfig: os.IsNotExist(cErr), NewEnv: os.IsNotExist(eErr)}
			currentState.AddedAccess = cfg.Desktop.Access != nil && !slices.Contains(cfg.Desktop.Access, magpieID)
		}
		p := reasonixCatalogAt(models, selected, cfg.magpie().Effort, at.v1())
		if err := writeProvider(p); err != nil {
			return err
		}
		if cfg.Desktop.Access != nil && !slices.Contains(cfg.Desktop.Access, magpieID) {
			access := append(cfg.Desktop.Access, magpieID)
			if err := edit.SetTOMLKey(path, "desktop", "provider_access", reasonixArray(access)); err != nil {
				return err
			}
			currentState.AddedAccess = true
		}
		if token, _ := edit.GetEnvFile(env, reasonixKey); token != gwKey() {
			if err := edit.SetEnvFile(env, edit.KV{Path: reasonixKey, Value: gwKey()}); err != nil {
				return err
			}
		}
		b, err := json.Marshal(currentState)
		if err != nil {
			return err
		}
		if err := edit.WriteAtomic(statePath, b); err != nil {
			return err
		}
		*state = currentState
		return nil
	}
	set := func(v string) error {
		cfg, state, err := load()
		if err != nil {
			return err
		}
		ref, magpie := strings.CutPrefix(v, magpieID+"/")
		if !magpie {
			known := cfg.hasModel(v) || cfg.DefaultModel != nil && *cfg.DefaultModel == v ||
				state != nil && state.Default != nil && *state.Default == v
			if v != "" && !known {
				return fmt.Errorf("Reasonix has no model %q", v)
			}
			return restore(cfg, state, v)
		}
		if err := ensureGateway(cfg, &state, ref, ref); err != nil {
			return err
		}
		return edit.SetTOMLTopPreserving(path, edit.KV{Path: "default_model", Value: v})
	}

	a := &Agent{
		ID: "reasonix", Name: "Reasonix Studio", Icon: "reasonix-color", Aliases: []string{"reasonix-studio"},
		Bin: "reasonix", Dir: dir, Path: path, UA: []string{"reasonix"},
		detect: func() bool { return reasonixDetected(at.home) },
		Notice: func() string {
			return "Reasonix Studio 2.x and the native 1.39.x/2.x CLI read global settings at startup. Start a new process to load changes. Project/session models can override Executor and Plan."
		},
		Check: func() string {
			ref, on := strings.CutPrefix(model(), magpieID+"/")
			if !on {
				cfg, _, err := load()
				if err != nil {
					return "Reasonix configuration cannot be read"
				}
				if cfg.Agent.Planner != nil {
					ref, on = strings.CutPrefix(*cfg.Agent.Planner, magpieID+"/")
				}
				if !on {
					return ""
				}
			}
			cfg, state, err := load()
			if err != nil {
				return "Reasonix configuration cannot be read"
			}
			p := cfg.magpie()
			if state == nil || p.APIKeyEnv != reasonixKey {
				return "Reasonix's magpie provider or restore record is missing"
			}
			if err := checkOwner(cfg, state); err != nil {
				return err.Error()
			}
			if p.Kind != "openai" || p.BaseURL != at.v1() {
				return "Reasonix's magpie base_url or kind changed"
			}
			if token, _ := edit.GetEnvFile(env, reasonixKey); token != gwKey() {
				return "Reasonix's gateway credential changed"
			}
			if !slices.Contains(p.Models, ref) || cfg.Agent.Planner != nil && usesMagpie(*cfg.Agent.Planner) && !slices.Contains(p.Models, strings.TrimPrefix(*cfg.Agent.Planner, magpieID+"/")) {
				return "Reasonix's selected model is missing from the magpie catalog"
			}
			if cfg.Desktop.Access != nil && !slices.Contains(cfg.Desktop.Access, magpieID) {
				return "Reasonix Studio's provider_access excludes magpie"
			}
			return ""
		},
		Sync: func() error {
			cfg, state, err := load()
			if err != nil {
				return err
			}
			if state == nil {
				return nil
			}
			if err := checkOwner(cfg, state); err != nil {
				return err
			}
			current := cfg.magpie()
			if current.Name == "" {
				return nil
			}
			models := magpieModels("reasonix")
			if len(models) == 0 {
				return fmt.Errorf("magpie has no visible Reasonix models")
			}
			next := reasonixCatalogAt(models, current.Default, current.Effort, at.v1())
			if reflect.DeepEqual(current, next) {
				return nil
			}
			return writeProvider(next)
		},
		Fields: []Field{
			{Key: "model", Label: "model", Get: model, Set: set,
				Options: func(cur map[string]string) []Option {
					cfg, state, _ := load()
					var own []Option
					for _, p := range cfg.Providers {
						if p.Name == magpieID {
							continue
						}
						models := p.Models
						if len(models) == 0 && p.Model != "" {
							models = []string{p.Model}
						}
						for _, m := range models {
							own = append(own, Option{Value: p.Name + "/" + m, Icon: ModelIcon(m)})
						}
					}
					addNative := func(v string) {
						if v != "" && !usesMagpie(v) && !slices.ContainsFunc(own, func(o Option) bool { return o.Value == v }) {
							own = append(own, Option{Value: v, Icon: ModelIcon(v)})
						}
					}
					if state != nil && state.Default != nil {
						addNative(*state.Default)
					}
					addNative(cur["model"])
					return append(group("Reasonix Studio", own), viaMagpie("reasonix", magpieID+"/")...)
				},
			},
			{Key: "effort", Label: "effort", Quiet: true,
				Get: func() string {
					cfg, _ := readReasonix(path)
					if usesMagpie(model()) {
						return cfg.magpie().Effort
					}
					return ""
				},
				Set: func(v string) error {
					cfg, state, err := load()
					if err != nil {
						return err
					}
					if state == nil || cfg.DefaultModel == nil || !usesMagpie(*cfg.DefaultModel) {
						if v == "" {
							return nil
						}
						return fmt.Errorf("select a magpie model in Reasonix before setting effort")
					}
					if err := checkOwner(cfg, state); err != nil {
						return err
					}
					p := cfg.magpie()
					ref := strings.TrimPrefix(*cfg.DefaultModel, magpieID+"/")
					if v == "auto" {
						v = ""
					}
					if v != "" && !slices.Contains(p.Overrides[ref].Efforts, v) {
						return fmt.Errorf("Reasonix model %q does not support effort %q", ref, v)
					}
					p.Effort = v
					return writeProvider(p)
				},
				Options: func(cur map[string]string) []Option {
					ref, ok := strings.CutPrefix(cur["model"], magpieID+"/")
					if !ok {
						return nil
					}
					cfg, _ := readReasonix(path)
					levels := cfg.magpie().Overrides[ref].Efforts
					if len(levels) == 0 {
						return nil
					}
					return static(append([]string{"auto"}, levels...)...)
				},
			},
		},
	}
	planner := Field{Key: "planner", Label: "planner", Get: func() string {
		cfg, _ := readReasonix(path)
		if cfg.Agent.Planner != nil {
			return *cfg.Agent.Planner
		}
		return ""
	}, Options: a.Fields[0].Options, Set: func(v string) error {
		cfg, state, err := load()
		if err != nil {
			return err
		}
		off := v == "off"
		if off {
			v = ""
		}
		ref, via := strings.CutPrefix(v, magpieID+"/")
		if via {
			selected := ref
			if cfg.DefaultModel != nil && usesMagpie(*cfg.DefaultModel) {
				selected = strings.TrimPrefix(*cfg.DefaultModel, magpieID+"/")
			}
			if err := ensureGateway(cfg, &state, ref, selected); err != nil {
				return err
			}
			if !state.PlannerManaged {
				state.Planner = cfg.Agent.Planner
				state.PlannerManaged = true
			}
		} else if v != "" {
			known := cfg.hasModel(v) || cfg.Agent.Planner != nil && *cfg.Agent.Planner == v ||
				cfg.DefaultModel != nil && *cfg.DefaultModel == v ||
				state != nil && (state.Planner != nil && *state.Planner == v || state.Default != nil && *state.Default == v)
			if !known {
				return fmt.Errorf("Reasonix has no planner model %q", v)
			}
		}
		if v == "" && !off && state != nil && state.PlannerManaged && state.Planner != nil {
			v = *state.Planner
		}
		if v == "" {
			err = edit.DelTOMLKey(path, "agent", "planner_model")
		} else {
			err = edit.SetTOMLKey(path, "agent", "planner_model", v)
		}
		if err != nil {
			return err
		}
		if state != nil {
			raw, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if err = edit.WriteAtomic(statePath, raw); err != nil {
				return err
			}
		}
		if !via && !usesMagpie(model()) {
			next, _, err := load()
			if err != nil {
				return err
			}
			return restore(next, state, "")
		}
		return nil
	}}
	options := planner.Options
	planner.Options = func(cur map[string]string) []Option {
		choices := options(cur)
		cfg, state, _ := load()
		addNative := func(ref *string) {
			if ref != nil && *ref != "" && !usesMagpie(*ref) && !slices.ContainsFunc(choices, func(o Option) bool { return o.Value == *ref }) {
				choices = append(choices, Option{Value: *ref, Icon: ModelIcon(*ref), Group: "Reasonix Studio"})
			}
		}
		addNative(cfg.Agent.Planner)
		if state != nil {
			addNative(state.Planner)
		}
		return append([]Option{{Value: "off", Label: "off", Note: "Disable the separate planner"}}, choices...)
	}
	a.Fields[0].Label = "executor"
	a.Fields = append(a.Fields, planner)
	return serializedReasonix(atomic(a, path, env, statePath))
}

func serializedReasonix(a *Agent) *Agent {
	for i := range a.Fields {
		f := &a.Fields[i]
		if get := f.Get; get != nil {
			f.Get = func() string {
				reasonixEdits.RLock()
				defer reasonixEdits.RUnlock()
				return get()
			}
		}
		if set := f.Set; set != nil {
			f.Set = func(v string) error {
				reasonixEdits.Lock()
				defer reasonixEdits.Unlock()
				return set(v)
			}
		}
		if options := f.Options; options != nil {
			f.Options = func(cur map[string]string) []Option {
				reasonixEdits.RLock()
				defer reasonixEdits.RUnlock()
				return options(cur)
			}
		}
	}
	if check := a.Check; check != nil {
		a.Check = func() string {
			reasonixEdits.RLock()
			defer reasonixEdits.RUnlock()
			return check()
		}
	}
	if sync := a.Sync; sync != nil {
		a.Sync = func() error {
			reasonixEdits.Lock()
			defer reasonixEdits.Unlock()
			return sync()
		}
	}
	return a
}

func reasonixHome(goos, home string, getenv func(string) string) string {
	if dir := getenv("REASONIX_HOME"); dir != "" {
		return filepath.Clean(dir)
	}
	if goos == "windows" && getenv("APPDATA") != "" {
		return filepath.Join(getenv("APPDATA"), "reasonix")
	}
	return filepath.Join(home, ".reasonix")
}

func readReasonix(path string) (reasonixConfig, error) {
	var cfg reasonixConfig
	b, err := edit.Read(path)
	if err == nil && b != nil {
		err = toml.Unmarshal(b, &cfg)
	}
	if err != nil {
		return cfg, fmt.Errorf("Reasonix config.toml: %w", err)
	}
	names := map[string]bool{}
	for _, p := range cfg.Providers {
		if strings.TrimSpace(p.Name) == magpieID && p.Name != magpieID {
			return cfg, fmt.Errorf("Reasonix provider has a noncanonical magpie name")
		}
		if names[p.Name] {
			return cfg, fmt.Errorf("Reasonix has duplicate provider %q", p.Name)
		}
		names[p.Name] = true
	}
	return cfg, nil
}

func (cfg reasonixConfig) magpie() reasonixProvider {
	for _, p := range cfg.Providers {
		if p.Name == magpieID {
			return p
		}
	}
	return reasonixProvider{}
}

func (cfg reasonixConfig) hasModel(ref string) bool {
	for _, p := range cfg.Providers {
		if p.Name == magpieID {
			continue
		}
		if ref == p.Name || ref == p.Model || slices.Contains(p.Models, ref) {
			return true
		}
		if name, model, ok := strings.Cut(ref, "/"); ok && name == p.Name && (model == p.Model || slices.Contains(p.Models, model)) {
			return true
		}
	}
	return false
}

func reasonixCatalog(models []catalog.Model, selected, effort string) reasonixProvider {
	return reasonixCatalogAt(models, selected, effort, gatewayV1())
}

// reasonixCatalogAt is reasonixCatalog for a Reasonix reaching the
// gateway's /v1 at v1.
func reasonixCatalogAt(models []catalog.Model, selected, effort, v1 string) reasonixProvider {
	p := reasonixProvider{Name: magpieID, Kind: "openai", BaseURL: v1, APIKeyEnv: reasonixKey,
		Default: selected, Protocol: "openai", NoProxy: true, Overrides: map[string]reasonixModel{}}
	for _, m := range models {
		p.Models = append(p.Models, m.ID)
		ov := reasonixModel{Vision: m.ImageInput, Context: m.Context, Output: maxTokens(m)}
		if ov.Vision == nil && m.Images {
			vision := true
			ov.Vision = &vision
		}
		for _, e := range m.Efforts {
			if e == "off" {
				e = "none"
			}
			if slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, e) && !slices.Contains(ov.Efforts, e) {
				ov.Efforts = append(ov.Efforts, e)
			}
		}
		if len(ov.Efforts) == 0 {
			ov.Protocol = "none"
		}
		p.Overrides[m.ID] = ov
	}
	if !slices.Contains(p.Models, selected) {
		p.Default = firstOf(p.Models)
	}
	if slices.Contains(p.Overrides[p.Default].Efforts, effort) {
		p.Effort = effort
	}
	return p
}

func reasonixArray(values []string) edit.Raw {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return edit.Raw("[" + strings.Join(quoted, ", ") + "]")
}
