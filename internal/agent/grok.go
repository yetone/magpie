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

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// grokEfforts are the reasoning efforts Grok knows.
var grokEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// grokModelTable is the header prefix of every table magpie writes.
var grokModelTable = `model."` + magpieID + "/"

// grokModelTables are magpie's model tables, for a Grok that reaches the
// gateway's /v1 at v1.
func grokModelTables(v1 string) []edit.Table {
	var out []edit.Table
	for _, m := range magpieModels("grok") {
		kvs := []edit.KV{
			{Path: "model", Value: m.ID},
			{Path: "name", Value: m.Name},
			{Path: "base_url", Value: v1},
			{Path: "api_key", Value: keyAt(v1)},
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

func grok(home string) *Agent { return grokIn(here(home)) }

// grokIn is Grok Build at a place: this machine's home, or a WSL distro's
// (see wsl.go), where GROK_HOME isn't read and its models name the gateway
// as the distro reaches it.
func grokIn(at place) *Agent {
	dir := at.getenv("GROK_HOME")
	if dir == "" {
		dir = filepath.Join(at.home, ".grok")
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
	writeMagpie := func() error { return edit.SetTOMLTables(path, []string{grokModelTable}, grokModelTables(at.v1())) }
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
		return catalog.Efforts(magpieModels("grok"), ref)
	}
	return atomic(&Agent{
		ID: "grok", Name: "Grok Build", Icon: "xai", Aliases: []string{"grok-build", "grok-cli"}, Spelled: prefixed,
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
				return noticeGrok.String()
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
				"base_url", at.v1(), "api_key", at.gwKey())
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
					// magpie's rows for the account Grok Build is signed in
					// to are its own models a second time: they fold into
					// one row (Fate on Discord: grokbuild 在登录态下会加载重复的模型)
					opts := viaMagpie("grok", magpieID+"/")
					for i := range opts {
						opts[i].Same = opts[i].own
					}
					return append(grokOwnOptions(dir, path, cur["model"]), opts...)
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
	}, path)
}

// grokOwnOptions are the models Grok Build offers its signed-in account: as
// it last listed them itself (models_cache.json in its home), and as magpie
// last listed them for a Grok subscription; then the models of the user's
// own config.toml tables, and the current one. Grok asks for them itself,
// on its sign-in or their own key, so each says so (Direct): a row not
// connected listed nothing of Grok's own, only magpie's, unless a Grok
// subscription was signed in in magpie (EZN7L2C3, #834).
func grokOwnOptions(dir, path, cur string) []Option {
	seen := map[string]bool{}
	var out []Option
	add := func(id, name, direct string) {
		if id == "" || seen[id] || strings.HasPrefix(id, magpieID+"/") {
			return
		}
		seen[id] = true
		o := Option{Value: id, Icon: "xai", Direct: direct}
		if name != "" && name != id {
			o.Label = name
		}
		out = append(out, o)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "models_cache.json")); err == nil {
		gjson.GetBytes(raw, "models").ForEach(func(k, v gjson.Result) bool {
			if !v.Get("info.hidden").Bool() {
				add(k.String(), v.Get("info.name").String(), "xAI")
			}
			return true
		})
	}
	ms, _, _ := catalog.Live("grok")
	for _, m := range ms {
		add(m.ID, "", "xAI")
	}
	// the user's own [model."<id>"] tables, each on its own endpoint
	if tables, err := edit.TOMLTables(path); err == nil {
		for _, t := range tables {
			id, ok := strings.CutPrefix(t, "model.")
			if !ok {
				continue
			}
			if u, err := strconv.Unquote(id); err == nil {
				id = u
			}
			kv, _ := edit.GetTOMLTable(path, t)
			direct := "xAI"
			if h := hostOf(kv["base_url"]); h != "" {
				direct = h
			}
			add(id, kv["name"], direct)
		}
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur), Direct: "xAI"}}, out...)
	}
	return group("Grok Build", out)
}

// what grok says after a change (notice.go)
var (
	noticeGrok = newNotice("Grok Build reads its settings at start-up — restart open grok sessions to use this.")
)
