package agent

// Kimi Code's subagents (XiaoLing_Arisa on X): the new Kimi Code (2.x) puts
// them on its [secondary_model] (docs: kimi-code-cli/configuration/
// config-files#secondary-model; packages/agent-core-v2 session/subagent/
// configSection.ts):
//
//	[secondary_model]
//	default_model = "alias"      # what a subagent runs on unless the lead names one
//	default_effort = "high"      # their thinking level
//	[secondary_model.models]     # the pool the lead may pick from
//	"alias" = "when to use it"
//
// Every name is a key of a [models."<key>"] table. With no section a
// subagent runs on the lead's model and effort; default_model alone is a pool
// of one; the legacy model key stands for default_model. Kimi refuses to
// start, resume or fork a session when default_model is missing beside a
// pool or isn't in it, a pool entry names no model, or default_effort is one
// a pool model can't take (assertValidSubagentModelPool,
// assertValidSubagentDefaultEffort). So magpie's model tables going (a
// disconnect, a model leaving the catalog) take their names out of the
// section first. kimi-cli (Python) has no such section.

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
)

const kimiSecondary = "secondary_model"

// kimiSec is [secondary_model] as Kimi reads it.
type kimiSec struct {
	Default string            // default_model
	Legacy  string            // model, read when default_model isn't there
	Pool    map[string]string // [secondary_model.models]; nil without one
	Effort  string            // default_effort
}

// def is the model a subagent starts on by Kimi's reading.
func (s kimiSec) def() string {
	if s.Default != "" {
		return s.Default
	}
	return s.Legacy
}

// pool is the pool's names as Kimi resolves it (resolveSubagentModelPool):
// the table's, else default_model's (or model's) alone; nil for none. The
// default first, then the rest by name.
func (s kimiSec) pool() []string {
	if s.Pool == nil {
		if d := s.def(); d != "" {
			return []string{d}
		}
		return nil
	}
	out := make([]string, 0, len(s.Pool))
	for k := range s.Pool {
		if k != s.Default {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if _, ok := s.Pool[s.Default]; ok {
		out = append([]string{s.Default}, out...)
	}
	return out
}

// kimiModelInfo is what a [models."<key>"] table says of thinking, its
// [overrides] over it.
type kimiModelInfo struct {
	Efforts []string
	Default string
	Caps    []string
}

func (m kimiModelInfo) thinks() bool {
	return slices.Contains(m.Caps, "thinking") || slices.Contains(m.Caps, "always_thinking") || len(m.Efforts) > 0
}

func (m kimiModelInfo) always() bool { return slices.Contains(m.Caps, "always_thinking") }

// kimiDoc is the config at path, read: [secondary_model] and the model
// tables' thinking. ok is false when it can't be read or parsed.
func kimiDoc(path string) (sec kimiSec, models map[string]kimiModelInfo, ok bool) {
	type levels struct {
		Capabilities   []string `toml:"capabilities"`
		SupportEfforts []string `toml:"support_efforts"`
		DefaultEffort  string   `toml:"default_effort"`
	}
	var doc struct {
		Secondary *struct {
			DefaultModel  string            `toml:"default_model"`
			Model         string            `toml:"model"`
			Models        map[string]string `toml:"models"`
			DefaultEffort string            `toml:"default_effort"`
		} `toml:"secondary_model"`
		Models map[string]struct {
			levels
			Overrides *levels `toml:"overrides"`
		} `toml:"models"`
	}
	b, err := edit.Read(path)
	if err != nil {
		return kimiSec{}, nil, false
	}
	if err := toml.Unmarshal(edit.TrimBOM(b), &doc); err != nil {
		return kimiSec{}, nil, false
	}
	if s := doc.Secondary; s != nil {
		sec = kimiSec{Default: s.DefaultModel, Legacy: s.Model, Pool: s.Models, Effort: strings.ToLower(strings.TrimSpace(s.DefaultEffort))}
	}
	models = map[string]kimiModelInfo{}
	for k, m := range doc.Models {
		info := kimiModelInfo{Efforts: m.SupportEfforts, Default: m.DefaultEffort, Caps: m.Capabilities}
		if o := m.Overrides; o != nil {
			if o.SupportEfforts != nil {
				info.Efforts = o.SupportEfforts
			}
			if o.DefaultEffort != "" {
				info.Default = o.DefaultEffort
			}
			if o.Capabilities != nil {
				info.Caps = o.Capabilities
			}
		}
		models[k] = info
	}
	return sec, models, true
}

// kimiPoolLevels are the subagent efforts every model of pool takes, as
// Kimi checks default_effort: off unless one always thinks, any other level
// only where each thinks and lists it (or lists none). A model the config
// doesn't describe (Kimi Code's own sign-in's, from its catalog) is taken
// at the common levels. Nil for no pool: Kimi ignores the effort then.
func kimiPoolLevels(pool []string, models map[string]kimiModelInfo) []string {
	if len(pool) == 0 {
		return nil
	}
	off := true
	var levels []string // nil: any yet
	any := true
	for _, alias := range pool {
		m, known := models[alias]
		if !known {
			m = kimiModelInfo{Efforts: kimiCommonLevels[1:], Caps: []string{"thinking"}}
		}
		if m.always() {
			off = false
		}
		if !m.thinks() {
			levels, any = []string{}, false
			continue
		}
		if len(m.Efforts) == 0 {
			continue
		}
		var own []string
		for _, e := range m.Efforts {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" && e != "off" && e != "none" {
				own = append(own, e)
			}
		}
		if any {
			levels, any = own, false
			continue
		}
		levels = slices.DeleteFunc(levels, func(e string) bool { return !slices.Contains(own, e) })
	}
	if any {
		levels = kimiCommonLevels[1:]
	}
	var out []string
	if off {
		out = append(out, "off")
	}
	return append(out, levels...)
}

// kimiSecStashKey is where the stash keeps the subagent model the user had
// before magpie first put one of its own models in [secondary_model].
func kimiSecStashKey(path string) string { return "kimi:" + path + ":secondary" }

// kimiSecStash keeps the user's subagent model, once, as a magpie model
// first goes in.
func kimiSecStash(path string, s kimiSec) {
	k := kimiSecStashKey(path)
	if _, ok := stashShared()[k]; ok {
		return
	}
	b, _ := json.Marshal(map[string]string{"default_model": s.def()})
	stash(map[string]string{k: string(b)})
}

func kimiSecStashed(path string) string {
	var was map[string]string
	json.Unmarshal([]byte(stashShared()[kimiSecStashKey(path)]), &was)
	return was["default_model"]
}

// kimiSecWrite writes [secondary_model] from was to now, each key in place,
// the user's other keys and comments kept. An effort the new pool can't
// take goes first and a new one last, so Kimi, which reads the file as it
// changes, never meets one its pool refuses.
func kimiSecWrite(path string, was, now kimiSec) error {
	if was.Effort != "" && was.Effort != now.Effort {
		if err := edit.DelTOMLKey(path, kimiSecondary, "default_effort"); err != nil {
			return err
		}
	}
	setDefault := func() error {
		if now.Default == was.Default {
			return nil
		}
		if now.Default == "" {
			return edit.DelTOMLKey(path, kimiSecondary, "default_model")
		}
		return edit.SetTOMLKey(path, kimiSecondary, "default_model", now.Default)
	}
	// a section new: its default before its pool, a pool of one till then
	fresh := was.Default == "" && was.Legacy == "" && was.Pool == nil && was.Effort == ""
	if fresh {
		if err := setDefault(); err != nil {
			return err
		}
	}
	if !sameTable(was.Pool, now.Pool) {
		tables, _ := edit.TOMLTables(path)
		own := slices.Contains(tables, kimiSecondary+".models")
		if was.Pool != nil && !own {
			// written inline (models = { … }) or dotted: out, for the table
			if err := edit.DelTOMLKey(path, kimiSecondary, "models"); err != nil {
				return err
			}
		}
		if now.Pool == nil {
			if err := edit.DelTOMLTable(path, kimiSecondary+".models"); err != nil {
				return err
			}
		} else {
			kvs := make([]edit.KV, 0, len(now.Pool))
			for _, k := range now.pool() {
				kvs = append(kvs, edit.KV{Path: strconv.Quote(k), Value: now.Pool[k]})
			}
			if err := edit.SetTOMLTable(path, kimiSecondary+".models", kvs...); err != nil {
				return err
			}
		}
	}
	if !fresh {
		if err := setDefault(); err != nil {
			return err
		}
	}
	if was.Legacy != "" && now.Legacy == "" {
		if err := edit.DelTOMLKey(path, kimiSecondary, "model"); err != nil {
			return err
		}
	}
	if now.Effort != "" && now.Effort != was.Effort {
		if err := edit.SetTOMLKey(path, kimiSecondary, "default_effort", now.Effort); err != nil {
			return err
		}
	}
	return nil
}

// sameTable is maps.Equal with no table and an empty one apart.
func sameTable(a, b map[string]string) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// kimiSecFit is now with its effort dropped when its pool can't take it.
func kimiSecFit(now kimiSec, models map[string]kimiModelInfo) kimiSec {
	if now.Effort != "" && !slices.Contains(kimiPoolLevels(now.pool(), models), now.Effort) && now.pool() != nil {
		now.Effort = ""
	}
	return now
}

// kimiPruneSecondary takes out of [secondary_model] each of magpie's
// models keep says is going: from the pool, and as the default, which goes
// back to the one the user had (restore, while Kimi still has it), else the
// pool's first, else none, so subagents follow the lead again. An effort
// the pool left can't take goes too. The user's own names stay.
func kimiPruneSecondary(path string, keep func(alias string) bool, restore string) error {
	was, models, ok := kimiDoc(path)
	if !ok {
		return nil
	}
	gone := func(k string) bool { return strings.HasPrefix(k, magpieID+"/") && !keep(k) }
	now := was
	if was.Pool != nil {
		now.Pool = map[string]string{}
		for k, v := range was.Pool {
			if !gone(k) {
				now.Pool[k] = v
			}
		}
	}
	if gone(now.Legacy) {
		now.Legacy = ""
	}
	if gone(now.Default) || now.Default == "" && was.Legacy != "" && now.Legacy == "" {
		now.Default = ""
		switch {
		case restore != "" && !strings.HasPrefix(restore, magpieID+"/"):
			// the user's, as they had it: into the pool left, as Kimi's
			// own /secondary-model puts a default
			now.Default = restore
			if len(now.Pool) > 0 && !hasKey(now.Pool, restore) {
				now.Pool[restore] = ""
			}
		case len(now.Pool) > 0:
			now.Default = kimiSec{Pool: now.Pool}.pool()[0]
		}
	}
	if now.Pool != nil && len(now.Pool) == 0 {
		now.Pool = nil
	}
	if now.Pool == nil && now.def() == "" {
		now.Effort = "" // nothing for it to apply to: the lead's own
	}
	now = kimiSecFit(now, models)
	if sameTable(was.Pool, now.Pool) && was.Default == now.Default && was.Legacy == now.Legacy && was.Effort == now.Effort {
		return nil
	}
	if err := kimiSecWrite(path, was, now); err != nil {
		return err
	}
	return kimiSecDropEmpty(path)
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// kimiSecDropEmpty takes out a [secondary_model] left with nothing in it.
func kimiSecDropEmpty(path string) error {
	t, err := edit.GetTOMLTable(path, kimiSecondary)
	if err != nil || t == nil || len(t) > 0 {
		return err
	}
	tables, _ := edit.TOMLTables(path)
	if slices.Contains(tables, kimiSecondary+".models") {
		return nil
	}
	return edit.DelTOMLTable(path, kimiSecondary)
}

// kimiSubagentFields are the subagents' model, pool and effort, for the new
// Kimi Code. wired reports whether magpie's provider is in the config: a
// magpie model can go in only then, its table there.
func kimiSubagentFields(path string, wired func() bool, own func(string) []Option) []Field {
	read := func() (kimiSec, map[string]kimiModelInfo) {
		s, m, _ := kimiDoc(path)
		return s, m
	}
	// check refuses a name Kimi would not resolve: one of magpie's whose
	// table isn't there (not connected, or gone from the catalog)
	check := func(models map[string]kimiModelInfo, names ...string) error {
		for _, k := range names {
			if !strings.HasPrefix(k, magpieID+"/") {
				continue
			}
			if _, ok := models[k]; !ok || !wired() {
				return fmt.Errorf("pick a model through magpie for Kimi Code first; its subagents can then run on one of magpie's")
			}
		}
		return nil
	}
	options := func() []Option {
		if wired() {
			return withVia(own(""), "kimi", magpieID+"/")
		}
		return own("")
	}
	return []Field{{
		// [secondary_model] default_model: unset, every subagent runs on
		// the lead's model (no section at all, as Kimi has it)
		Key: "subagent_model", Label: "subagent model", Quiet: true,
		Get: func() string { s, _ := read(); return s.def() },
		Set: func(v string) error {
			was, models := read()
			now := was
			if v == "" {
				now = kimiSec{}
			} else {
				if err := check(models, v); err != nil {
					return err
				}
				if strings.HasPrefix(v, magpieID+"/") {
					kimiSecStash(path, was)
				}
				// as Kimi's own /secondary-model: the default, and into a
				// pool there is, its description empty
				now.Default, now.Legacy = v, ""
				if was.Pool != nil {
					now.Pool = maps.Clone(was.Pool)
					if !hasKey(now.Pool, v) {
						now.Pool[v] = ""
					}
				}
			}
			now = kimiSecFit(now, models)
			if err := kimiSecWrite(path, was, now); err != nil {
				return err
			}
			return kimiSecDropEmpty(path)
		},
		Options: func(map[string]string) []Option { return options() },
	}, {
		// [secondary_model.models]: the models the lead may put a subagent
		// on, one a line; the default stays one of them
		Key: "subagent_pool", Label: "subagent pool", Quiet: true,
		Get: func() string {
			s, _ := read()
			if s.Pool == nil {
				return ""
			}
			return strings.Join(s.pool(), "\n")
		},
		Set: func(v string) error {
			was, models := read()
			var names []string
			for _, k := range strings.Split(v, "\n") {
				if k = strings.TrimSpace(k); k != "" && !slices.Contains(names, k) {
					names = append(names, k)
				}
			}
			if err := check(models, names...); err != nil {
				return err
			}
			now := was
			if len(names) == 0 {
				now.Pool = nil
			} else {
				if slices.ContainsFunc(names, func(k string) bool { return strings.HasPrefix(k, magpieID+"/") }) {
					kimiSecStash(path, was)
				}
				now.Pool = map[string]string{}
				for _, k := range names {
					now.Pool[k] = was.Pool[k] // a description written stays
				}
				if !hasKey(now.Pool, now.def()) {
					now.Default, now.Legacy = names[0], ""
				}
				if now.Default == "" {
					now.Default, now.Legacy = now.Legacy, ""
				}
			}
			now = kimiSecFit(now, models)
			if err := kimiSecWrite(path, was, now); err != nil {
				return err
			}
			return kimiSecDropEmpty(path)
		},
		Options: func(map[string]string) []Option { return options() },
	}, {
		// [secondary_model] default_effort: unset, a subagent thinks as its
		// model does by default
		Key: "subagent_effort", Label: "subagent effort", Quiet: true,
		Get: func() string { s, _ := read(); return s.Effort },
		Set: func(v string) error {
			v = strings.ToLower(strings.TrimSpace(v))
			was, models := read()
			if v != "" {
				l := kimiPoolLevels(was.pool(), models)
				if l == nil {
					return fmt.Errorf("pick a subagent model for Kimi Code first; with none, its subagents think as the lead does")
				}
				if !slices.Contains(l, v) {
					return fmt.Errorf("Kimi Code's subagent models take %s, not %q: it would refuse to start a session", strings.Join(l, ", "), v)
				}
			}
			now := was
			now.Effort = v
			if err := kimiSecWrite(path, was, now); err != nil {
				return err
			}
			return kimiSecDropEmpty(path)
		},
		Options: func(map[string]string) []Option {
			s, models := read()
			return static(kimiPoolLevels(s.pool(), models)...)
		},
	}}
}

// kimiSubagents are kimiSubagentFields for the new Kimi Code; kimi-cli has
// no subagent pool, and a stopped WSL distro's (cold) has none until it runs
// again: each of the three reads the config, which would start it (#3896).
func kimiSubagents(path string, legacy, cold bool, wired func() bool) []Field {
	if legacy || cold {
		return nil
	}
	return kimiSubagentFields(path, wired, func(cur string) []Option { return kimiOwnOptions(path, cur) })
}
