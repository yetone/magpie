package agent

// Grok Build, xAI's grok CLI, keeps its settings in ~/.grok/config.toml (or
// $GROK_HOME's): the model new sessions start with under [models] default,
// and models of the user's own as [model."<id>"] tables. magpie adds one
// such table per catalog model, named "magpie/<provider>/<model>", pointing
// at the gateway with magpie's own key — a model with no key of its own
// would be sent the user's xAI sign-in — so the catalog joins Grok's own
// models in its /model picker.
//
// A signed-in Grok also takes remote "campaign" patches from xAI, applied
// above config.toml, and a launch campaign sets models.default (September
// 2026's grok-4.7-launch did): a default picked in magpie was ignored and
// every new session started on the campaign's model. So a default picked
// here turns campaigns off ([features] campaigns = false), and clearing it
// gives them back.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// grokEfforts are the reasoning efforts Grok knows.
var grokEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// grokModelTable is the header prefix of every table magpie writes.
var grokModelTable = `model."` + magpieID + "/"

func grokModelTables() []edit.Table {
	var out []edit.Table
	for _, m := range magpieModels() {
		kvs := []edit.KV{
			{Path: "model", Value: m.ID},
			{Path: "name", Value: m.Name},
			{Path: "base_url", Value: gatewayV1()},
			{Path: "api_key", Value: gateway.Token},
			{Path: "api_backend", Value: "chat_completions"},
		}
		if m.Context > 0 {
			kvs = append(kvs, edit.KV{Path: "context_window", Value: m.Context})
		}
		var efforts []string
		for _, e := range grokEfforts { // in Grok's order
			if contains(m.Efforts, e) {
				efforts = append(efforts, strconv.Quote(e))
			}
		}
		if len(efforts) > 0 {
			kvs = append(kvs, edit.KV{Path: "reasoning_efforts", Value: edit.Raw("[" + strings.Join(efforts, ", ") + "]")})
		}
		out = append(out, edit.Table{Name: "model." + strconv.Quote(magpieID+"/"+m.ID), KVs: kvs})
	}
	return out
}

func grok(home string) *Agent {
	dir := os.Getenv("GROK_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".grok")
	}
	path := filepath.Join(dir, "config.toml")
	get := func(k string) (string, error) {
		models, err := edit.GetTOMLTable(path, "models")
		return models[k], err
	}
	wired := func() (bool, error) {
		tables, err := edit.TOMLTables(path)
		if err != nil {
			return false, err
		}
		for _, t := range tables {
			if strings.HasPrefix(t, grokModelTable) {
				return true, nil
			}
		}
		return false, nil
	}
	writeMagpie := func() error { return edit.SetTOMLTables(path, []string{grokModelTable}, grokModelTables()) }
	dropMagpie := func() error { return edit.SetTOMLTables(path, []string{grokModelTable}, nil) }
	// the default model is the user's, not a campaign's
	ownDefault := func(features map[string]string) error {
		if features["campaigns"] == "false" {
			return nil
		}
		return edit.SetTOMLKey(path, "features", "campaigns", false)
	}
	// the efforts of a model through magpie, as its catalog entry has them
	efforts := func(model string) []string {
		ref, ok := strings.CutPrefix(model, magpieID+"/")
		if !ok {
			return nil
		}
		return catalog.Efforts(magpieModels(), ref)
	}
	return &Agent{
		ID: "grok", Name: "Grok Build", Icon: "xai", Aliases: []string{"grok-build", "grok-cli"},
		UA:  []string{"grok-shell", "grok-pager", "xai-grok-build"}, // grok-pager: its terminal front end
		Dir: dir, Path: path,
		Sync: func() error {
			ok, err := wired()
			if err != nil || !ok {
				return err
			}
			model, err := get("default")
			if err != nil {
				return err
			}
			if model != "" {
				features, err := edit.GetTOMLTable(path, "features")
				if err != nil {
					return err
				}
				if err := ownDefault(features); err != nil {
					return err
				}
			}
			return writeMagpie()
		},
		Notice: func() string {
			if Running(`(^|/)grok( |$)`) {
				return "Grok Build reads its settings at start-up — restart open grok sessions to use this."
			}
			return ""
		},
		Check: func() string {
			v, err := get("default")
			if err != nil {
				return err.Error()
			}
			if !usesMagpie(v) {
				return ""
			}
			t, err := edit.GetTOMLTable(path, "model."+strconv.Quote(v))
			if err != nil {
				return err.Error()
			}
			if t == nil {
				return "Grok Build's [model." + strconv.Quote(v) + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Grok Build", path, func(k string) (string, bool) { v, ok := t[k]; return v, ok },
				"base_url", gatewayV1(), "api_key", gateway.Token)
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				Get: func() string { v, _ := get("default"); return v },
				Set: func(v string) error {
					e, err := get("default_reasoning_effort")
					if err != nil {
						return err
					}
					features, err := edit.GetTOMLTable(path, "features")
					if err != nil {
						return err
					}
					if v == "" {
						if err := edit.DelTOMLKey(path, "models", "default"); err != nil {
							return err
						}
						if err := edit.DelTOMLKey(path, "features", "campaigns"); err != nil {
							return err
						}
						return dropMagpie()
					}
					if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
						if err := writeMagpie(); err != nil {
							return err
						}
					} else if err := dropMagpie(); err != nil {
						return err
					}
					if e != "" {
						if es := efforts(v); es != nil && !contains(es, e) {
							if err := edit.DelTOMLKey(path, "models", "default_reasoning_effort"); err != nil {
								return err
							}
						}
					}
					if err := ownDefault(features); err != nil {
						return err
					}
					return edit.SetTOMLKey(path, "models", "default", v)
				},
				Options: func(cur map[string]string) []Option {
					return append(grokOwnOptions(cur["model"]), viaMagpie(magpieID+"/")...)
				},
			},
			{
				// the effort new sessions start with; Grok applies it to a
				// model that supports it and ignores it otherwise
				Key: "effort", Label: "effort",
				Get: func() string { v, _ := get("default_reasoning_effort"); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLKey(path, "models", "default_reasoning_effort")
					}
					return edit.SetTOMLKey(path, "models", "default_reasoning_effort", v)
				},
				Options: func(cur map[string]string) []Option {
					if es := efforts(cur["model"]); es != nil {
						return static(es...)
					}
					return static("low", "medium", "high")
				},
			},
		},
	}
}

// grokOwnOptions are the models Grok Build offers its signed-in account, as
// magpie last listed them for a Grok subscription, and the current one.
func grokOwnOptions(cur string) []Option {
	ms, _, _ := catalog.Live("grok")
	seen := map[string]bool{}
	var out []Option
	for _, m := range ms {
		seen[m.ID] = true
		out = append(out, Option{Value: m.ID, Icon: "xai"})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return group("Grok Build", out)
}
