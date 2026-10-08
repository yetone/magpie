package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
)

// Goose (Block's goose, its CLI and desktop app alike) reaches magpie as a
// custom provider: a JSON file of its own in custom_providers beside
// config.yaml, named by the provider's id, which goose loads at start-up
// (crates/goose/src/config/declarative_providers.rs). Its schema is
// DeclarativeProviderConfig (crates/goose-providers/src/declarative.rs), and
// each of its models a ModelInfo (crates/goose-provider-types/src/base.rs):
//
//   - context_limit is the window goose's ContextLimitResolver takes for the
//     model before anything else but GOOSE_CONTEXT_LIMIT
//     (crates/goose-provider-types/src/context_limit.rs). Without it goose
//     guesses from its own registry by the model's name, or 128K.
//   - reasoning is what the desktop app's model picker asks before it shows
//     its Thinking Level (fetchModelReasoning, ui/desktop/src/components/
//     settings/models/modelInterface.ts); a model declared without it is
//     taken for one that doesn't think.
//
// dynamic_models false keeps goose to this list rather than the gateway's
// /v1/models (every model, none of their windows), and
// skip_canonical_filtering keeps magpie's order and models goose's registry
// doesn't know.

// gooseProviderID is magpie's file in Goose's custom_providers and the
// provider GOOSE_PROVIDER names.
const gooseProviderID = magpieID

// gooseThinks matches the model names goose sends its thinking level for on
// an OpenAI-compatible provider: OpenAI's reasoning models, as goose's
// is_openai_responses_model tells them (crates/goose-provider-types/src/
// formats/openai.rs). For any other model goose keeps the level to itself —
// thinking_effort is one of its own request params, never sent — so a
// level offered for it would change nothing; it is declared without
// reasoning rather than shown a picker that does nothing.
var gooseThinks = regexp.MustCompile(`(?i)(?:^|[-/])(?:o\d+(?:$|-)|gpt-(?:5|6)(?:$|[-.]))`)

// gooseKeys are the secrets goose reads for the native providers whose
// models magpie can list, and which goose takes from the environment before
// its keyring (Config::get_secret, crates/goose/src/config/base.rs; the keys
// from crates/goose/src/providers/<name>_def.rs).
var gooseKeys = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"google":     "GOOGLE_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

// gooseConfigured is the providers other than magpie that the Goose whose
// config.yaml is at cfg has set up: the ones its providers map marks
// configured or that it uses now (set_active_provider, crates/goose/src/
// config/providers.rs), the ones a legacy <name>_configured marker names
// (migrate_provider_config, config/migrations.rs), and a provider whose key
// is in the environment. A key in goose's keyring can't be seen from here;
// such a provider still counts once goose has used it, as each provider it
// is set up with gets an entry. getenv is the environment goose runs in:
// none of a WSL distro's is read.
func gooseConfigured(cfg string, getenv func(string) string) []string {
	set := map[string]bool{}
	if b, err := os.ReadFile(cfg); err == nil {
		var c map[string]any
		if yaml.Unmarshal(b, &c) == nil {
			if ps, ok := c["providers"].(map[string]any); ok {
				for name, v := range ps {
					if e, ok := v.(map[string]any); ok && e["configured"] == true {
						set[name] = true
					}
				}
			}
			for k, v := range c {
				if name, ok := strings.CutSuffix(k, "_configured"); ok && v == true {
					set[name] = true
				}
			}
			for _, k := range []string{"active_provider", "GOOSE_PROVIDER"} {
				if p, ok := c[k].(string); ok && p != "" {
					set[p] = true
				}
			}
		}
	}
	for p, k := range gooseKeys {
		if getenv(k) != "" {
			set[p] = true
		}
	}
	delete(set, gooseProviderID)
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// gooseProviderPath is magpie's custom provider file for the Goose whose
// config.yaml is at cfg.
func gooseProviderPath(cfg string) string {
	return filepath.Join(filepath.Dir(cfg), "custom_providers", gooseProviderID+".json")
}

// gooseProviderJSON is magpie as a custom provider of the Goose at a
// place, its models the catalog as Goose is shown it.
func gooseProviderJSON(at place) map[string]any {
	models := []map[string]any{}
	for _, m := range magpieModels("goose") {
		e := map[string]any{"name": m.ID, "reasoning": gooseThinks.MatchString(m.ID)}
		if m.Context > 0 {
			e["context_limit"] = m.Context
		}
		models = append(models, e)
	}
	p := map[string]any{
		"name":                     gooseProviderID,
		"engine":                   "openai",
		"display_name":             "magpie",
		"description":              "Every model magpie has, through its local gateway",
		"api_key_env":              "",
		"base_url":                 at.v1(),
		"headers":                  map[string]any{"Authorization": "Bearer " + at.gwKey()},
		"requires_auth":            false,
		"skip_canonical_filtering": true,
		"models":                   models,
	}
	// goose refuses a provider whose static list is empty
	if len(models) > 0 {
		p["dynamic_models"] = false
	}
	return p
}

// writeGooseProvider writes magpie's custom provider file for Goose.
func writeGooseProvider(path string, at place) error {
	b, err := json.MarshalIndent(gooseProviderJSON(at), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(path, append(b, '\n'))
}

// syncGooseProvider rewrites magpie's custom provider file for Goose with
// the catalog as it is now, where magpie wrote one and it differs.
func syncGooseProvider(path string, at place) error {
	raw, err := edit.Read(path)
	if err != nil || raw == nil {
		return nil
	}
	if sameJSON(string(raw), gooseProviderJSON(at)) {
		return nil
	}
	return writeGooseProvider(path, at)
}

// removeGooseProvider takes magpie's custom provider file away once Goose's
// model is no longer one of magpie's.
func removeGooseProvider(path string) error {
	if err := edit.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Goose keeps the provider and model it uses in one of two layouts of its
// config.yaml (crates/goose/src/config/providers.rs, migrations.rs). The
// old one is two flat keys, GOOSE_PROVIDER and GOOSE_MODEL. The new one,
// which goose migrates a config to on its next write, is active_provider
// and an entry in the providers map holding that provider's model:
//
//	active_provider: magpie
//	providers:
//	  magpie:
//	    enabled: true
//	    model: zhipu/glm-5.3
//	    configured: true
//
// goose reads active_provider before GOOSE_PROVIDER and the entry's model
// before GOOSE_MODEL (get_active_provider, get_active_model), and once a
// providers map is there its migration deletes the flat keys on the next
// write (cleanup_legacy_provider_keys). So a model set with the flat keys
// alone on a migrated config is never used, and lost.

// gooseNewLayout reports whether the Goose config at cfg is in the new
// layout: it has a providers map or an active_provider.
func gooseNewLayout(cfg string) bool {
	b, err := edit.Read(cfg)
	if err != nil || b == nil {
		return false
	}
	var c map[string]any
	if yaml.Unmarshal(b, &c) != nil {
		return false
	}
	_, providers := c["providers"]
	_, active := c["active_provider"]
	return providers || active
}

// gooseActive is the provider and model the Goose config at cfg uses, read
// as goose reads them (the environment aside).
func gooseActive(cfg string) (provider, model string) {
	provider, _ = edit.GetYAMLTop(cfg, "active_provider")
	if provider == "" {
		provider, _ = edit.GetYAMLTop(cfg, "GOOSE_PROVIDER")
	}
	if provider != "" && !strings.Contains(provider, ".") {
		model, _ = edit.GetYAML(cfg, "providers."+provider+".model")
	}
	if model == "" {
		model, _ = edit.GetYAMLTop(cfg, "GOOSE_MODEL")
	}
	return provider, model
}

// gooseModel is the Goose config at cfg's model as provider/model.
func gooseModel(cfg string) string {
	p, m := gooseActive(cfg)
	switch {
	case m == "":
		return ""
	case p == "":
		return m
	}
	return p + "/" + m
}

// setGooseModel makes v, a provider/model, the model the Goose config at
// cfg uses, in the layout the config is in: on a new one as goose's own
// set_active_provider writes it, other providers' entries kept; on an old
// one the flat keys, which an old goose reads and a new one migrates.
func setGooseModel(cfg, v string) error {
	p, m, ok := strings.Cut(v, "/")
	if !ok || p == "" || m == "" {
		return fmt.Errorf("expected provider/model, got %q", v)
	}
	if !gooseNewLayout(cfg) {
		return edit.SetYAMLTop(cfg, edit.KV{Path: "GOOSE_PROVIDER", Value: p}, edit.KV{Path: "GOOSE_MODEL", Value: m})
	}
	if strings.Contains(p, ".") {
		return fmt.Errorf("goose provider %q: a dot can't be written into its providers map", p)
	}
	if err := edit.SetYAML(cfg,
		edit.KV{Path: "active_provider", Value: p},
		edit.KV{Path: "providers." + p + ".enabled", Value: true},
		edit.KV{Path: "providers." + p + ".model", Value: m},
		edit.KV{Path: "providers." + p + ".configured", Value: true},
	); err != nil {
		return err
	}
	// flat keys left beside the map would be read by no goose that reads
	// the map, and deleted by the next one that writes
	return edit.DelYAMLTop(cfg, "GOOSE_PROVIDER", "GOOSE_MODEL")
}

// clearGooseModel takes the Goose config at cfg off any model, as goose's
// clear_active_provider does, and drops magpie's own providers entry, as
// magpie's custom provider goes with it; other providers' entries are kept.
func clearGooseModel(cfg string) error {
	if err := edit.DelYAMLTop(cfg, "active_provider", "GOOSE_PROVIDER", "GOOSE_MODEL"); err != nil {
		return err
	}
	return edit.DelYAML(cfg, "providers."+gooseProviderID)
}
