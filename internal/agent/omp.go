package agent

// omp (oh-my-pi, a fork of Pi) keeps its settings in ~/.omp/agent/config.yml
// (or where ompDir says its variables move it),
// the model of each role under modelRoles as "provider/model", and providers
// of the user's own in models.yml beside it. magpie adds itself there as the
// provider "magpie", keyless (auth: none) on loopback, with the catalog as its models; a
// model through magpie is "magpie/<provider>/<model>", which omp matches
// whole against provider/id. The other roles, the fallback chains and the
// like may name them as well (ompRefKeys); magpie stays while any does.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/dirent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// ompEfforts are the thinking levels omp knows.
var ompEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// ompRefKeys are where omp's config names models: each role (a list to try
// in order, "a,b" or a sequence), the retry fallback chains (a list of
// models under each key; the key is a role's name, the only kind omp 16
// takes (agent-session.ts), or from omp 18 a model selector as well
// (retry-fallback-chains.ts) — a role's name read with them is no model of
// magpie's and is passed over), the models it cycles through (enabledModels,
// also scoped to paths) and the models of task agents. An entry may end in a
// thinking level ("magpie/deepseek/pro:max").
var ompRefKeys = []string{"modelRoles", "retry.fallbackChains", "enabledModels", "task.agentModelOverrides"}

// ompRefs calls fn on each model of a value under ompRefKeys, the spaces
// after a comma kept, and answers the value with fn's answers in place.
func ompRefs(v string, fn func(string) string) string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		lead := len(p) - len(strings.TrimLeft(p, " \t"))
		parts[i] = p[:lead] + fn(p[lead:])
	}
	return strings.Join(parts, ",")
}

// ompLevel splits a role's model from the thinking level omp reads off its
// end, "provider/model:level" (model-resolver.ts), the level returned with
// its colon: one of the efforts, off, or auto, omp's own pick each turn.
// Anything else after a colon is part of the model id (ollama's qwen3:8b),
// and a list of models omp falls back through ("a,b:high") has no level of
// its own, each of its models its own.
func ompLevel(v string) (model, level string) {
	if i := strings.LastIndexByte(v, ':'); i > 0 && !strings.Contains(v, ",") {
		if l := v[i+1:]; l == "off" || l == "auto" || slices.Contains(ompEfforts, l) {
			return v[:i], v[i:]
		}
	}
	return v, ""
}

// ompSplit is omp's SplitSuffix: a role's model and its thinking level
// (ompLevel), one false for a list of models omp falls back through ("a,b",
// or a YAML list, read as one), which is the user's own whatever it names.
func ompSplit(v string) (model, level string, one bool) {
	if strings.Contains(v, ",") {
		return v, "", false
	}
	model, level = ompLevel(v)
	return model, level, true
}

// ompOnMagpie: a role's value is one of magpie's models, at a level or not.
func ompOnMagpie(v string) bool {
	_, _, one := ompSplit(v)
	return one && usesMagpie(v)
}

// ompYAMLList: a value the stash keeps is a list as the YAML it was written
// in ("[a, b]", "- a"), which no model reads as.
func ompYAMLList(v string) bool { return strings.HasPrefix(v, "[") || strings.HasPrefix(v, "- ") }

// ompProfileName is a profile name omp takes (pi-utils dirs.ts,
// normalizeProfileName); it refuses any other.
var ompProfileName = lazyre.New(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ompReserved is a Windows device name omp refuses on every platform
// (pi-utils dirs.ts, normalizeProfileName): CON, PRN, AUX, NUL, COM0–COM9,
// LPT0–LPT9, and the same with a dot and anything after (con.work).
var ompReserved = lazyre.New(`(?i)^(?:CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\..*)?$`)

// ompProfileOK says whether name is one omp takes as a profile (pi-utils
// dirs.ts): "default" is none, a trailing dot is refused, and so is a
// Windows reserved device name. What a profiles folder holds is checked
// against this, and so is what a distro's probe finds, and the profile
// OMP_PROFILE names for the default row.
func ompProfileOK(name string) bool {
	return name != "" && name != "default" && ompProfileName.MatchString(name) &&
		!strings.HasSuffix(name, ".") && !ompReserved.MatchString(name)
}

// ompRoot is where omp keeps its agent folder and its profiles: ~/.omp, or
// ~/$PI_CONFIG_DIR when that variable renames it.
func ompRoot(home string) string {
	if d := appdir.Getenv("PI_CONFIG_DIR"); d != "" {
		return filepath.Join(home, d)
	}
	return filepath.Join(home, ".omp")
}

// ompDir is omp's agent folder, found as omp's pi-utils (dirs.ts) finds it:
// under ~/.omp, or ~/$PI_CONFIG_DIR; a profile's (OMP_PROFILE, else
// PI_PROFILE; "default" is none) is profiles/<name>/agent there; with none,
// PI_CODING_AGENT_DIR — the variable Pi reads — moves it, else it is
// agent there. omp takes that variable as given, without expanding "~".
//
// shared says the folder is one omp has no claim to: PI_CODING_AGENT_DIR
// moved it, and Pi reads that variable too, so Pi's own folder may be what
// it names.
func ompDir(home string) (dir string, shared bool) {
	root := ompRoot(home)
	p, set := appdir.LookupEnv("OMP_PROFILE")
	if !set {
		p = appdir.Getenv("PI_PROFILE")
	}
	if p = strings.TrimSpace(p); ompProfileOK(p) {
		return filepath.Join(root, "profiles", p, "agent"), false
	}
	if d := appdir.Getenv("PI_CODING_AGENT_DIR"); filepath.IsAbs(d) {
		return filepath.Clean(d), true
	}
	return filepath.Join(root, "agent"), false
}

func omp(home string) *Agent {
	dir, shared := ompDir(home)
	a := ompAt(here(home), dir, func() ompProviderEntry { return ompProvider() })
	// Pi's folder is no sign of omp: its config.yml or its command is
	a.dirShared = shared
	return a
}

// ompProfiles are the named omp profiles under ~/.omp/profiles (or
// ~/$PI_CONFIG_DIR/profiles): each is an omp of its own, omp#<name>, with
// its own config.yml and models.yml in profiles/<name>/agent. The one the
// default row already picks (OMP_PROFILE) is left out, so it is not listed
// twice, and the name rules are omp's own. A missing profiles folder is no
// profiles, not an error; a profile whose agent folder has no config.yml
// yet is still a row, and the first Set makes the file.
func ompProfiles(home string) []*Agent {
	root := ompRoot(home)
	entries, err := os.ReadDir(filepath.Join(root, "profiles"))
	if err != nil {
		return nil
	}
	cur, _ := ompDir(home)
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !dirent.IsFolder(filepath.Join(root, "profiles"), e) || !ompProfileOK(name) {
			continue
		}
		if filepath.Join(root, "profiles", name, "agent") == cur {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var out []*Agent
	for _, name := range names {
		a := ompAt(here(home), filepath.Join(root, "profiles", name, "agent"), func() ompProviderEntry { return ompProvider() })
		a.ID, a.Name = "omp#"+name, "omp · "+name
		// the default row keeps omp's name, its command and its own lists
		a.Aliases, a.UA, a.Bin = nil, nil, ""
		profile := filepath.Join(root, "profiles", name)
		a.detect = func() bool { return isDir(profile) }
		out = append(out, a)
	}
	return out
}

// ompIn is omp in a WSL distro (see wsl.go): ~/.omp/agent, as the
// distro's variables that move it aren't read. The omp there isn't the one
// on Windows' PATH: its version is the one the distro's probe asked it
// (whqtian on Discord: a WSL omp's max became xhigh while its version wasn't
// known), and without one its models offer xhigh rather than a max an older
// omp would refuse.
func ompIn(at place) *Agent {
	return ompAt(at, at.join(".omp", "agent"), func() ompProviderEntry { return ompProviderAt(at.gw(), at.version) })
}

// ompAt is omp with its agent folder at dir, magpie's entry in its
// models.yml being entry's.
func ompAt(at place, dir string, entry func() ompProviderEntry) *Agent {
	// magpie's entry laid over the one in the models file at path (ompKept)
	ompProvider := func(path string) any { return ompKept(path, entry()) }
	// omp reads the .yml and falls back to the .yaml
	pick := func(name string) string {
		yml := filepath.Join(dir, name+".yml")
		if !at.exists(yml) && at.exists(filepath.Join(dir, name+".yaml")) {
			return filepath.Join(dir, name+".yaml")
		}
		return yml
	}
	path := pick("config")
	// the entries of enabledModels a sync took out (scope_gone.go)
	gone := at.key("omp.scope.gone")
	roleGet := func(name string) func() string {
		return func() string {
			k := "modelRoles." + name
			if v, ok := edit.GetYAML(path, k); ok {
				return v
			}
			// a YAML list of models omp falls back through reads as the
			// comma-separated string it takes for one too: what the stash
			// keeps, and a reset writes back
			return strings.Join(edit.GetYAMLList(path, k), ",")
		}
	}
	// onMagpie: a role, a fallback chain, … (ompRefKeys) still names one of
	// magpie's models
	onMagpie := func() (used bool, err error) {
		err = edit.EditYAMLStrings(path, ompRefKeys, func(v string) string {
			ompRefs(v, func(m string) string { used = used || usesMagpie(m); return m })
			return v
		})
		return used, err
	}
	writeMagpie := func() error {
		models := pick("models")
		if _, err := os.Stat(models); err != nil {
			// omp moves an older models.json to models.yml only while there is
			// no models.yml, so one magpie writes first has to carry it over
			if err := edit.JSONToYAML(filepath.Join(dir, "models.json"), models); err != nil {
				return err
			}
		}
		return edit.SetYAML(models, edit.KV{Path: "providers." + magpieID, Value: ompProvider(models)})
	}
	// dropMagpie takes magpie's provider out of models.yml once nothing in
	// the config is on magpie; while something is (a fallback chain, another
	// role), it writes it afresh instead, so a role set off magpie — applying
	// again what Check found — mends the wiring that is left.
	// The plain entries of enabledModels on magpie, which magpie adds there
	// (omp_scope.go), don't count: once nothing else is on magpie they go
	// with its provider (#1769). Those scoped to folders are the user's, and
	// count.
	dropMagpie := func() error {
		used := false
		err := edit.EditYAMLStrings(path, slices.DeleteFunc(slices.Clone(ompRefKeys), func(k string) bool { return k == "enabledModels" }), func(v string) string {
			ompRefs(v, func(m string) string { used = used || usesMagpie(m); return m })
			return v
		})
		if err == nil && !used {
			used, err = ompScopeFoldersOnMagpie(path)
		}
		switch {
		case err != nil:
			return err
		case used:
			return writeMagpie()
		}
		if err := ompScopeWithout(path); err != nil {
			return err
		}
		forget(gone)
		return edit.DelYAML(pick("models"), "providers."+magpieID)
	}
	// was are the stash's keys for the roles, one each
	var was []string
	// role is the field for one of omp's model roles: one of magpie's brings
	// magpie's provider into models.yml, and it goes once nothing is on it.
	// What the user had there before magpie took the role over is stashed,
	// and resetting the role puts it back: a list of models as the YAML it
	// was written in, a flow list flow and a block one block.
	role := func(key, label, name string, quiet bool) Field {
		k := "modelRoles." + name
		get := roleGet(name)
		w := "omp:" + path + ":" + k
		was = append(was, w)
		kept := func() string {
			if v, ok := edit.GetYAML(path, k); ok {
				return v
			}
			v, _ := edit.GetYAMLText(path, k)
			return v
		}
		return Field{
			Key: key, Label: label, Quiet: quiet,
			Get: get,
			Set: func(v string) error {
				cur := get()
				if v == "" {
					if ompOnMagpie(cur) {
						v = unstash(w)
					}
					if v == "" {
						if err := edit.DelYAML(path, k); err != nil {
							return err
						}
						return dropMagpie()
					}
					if ompYAMLList(v) {
						if err := edit.SetYAML(path, edit.KV{Path: k, Value: edit.YAMLText(v)}); err != nil {
							return err
						}
						return dropMagpie()
					}
				} else if _, level, one := ompSplit(v); one && level == "" {
					// another model keeps the role's thinking level; omp
					// clamps one the model lacks to the highest it has below
					// it (its lowest when none is) and drops it for a model
					// that doesn't reason (pi-catalog model-thinking.ts),
					// never fails.
					// The same model picked without one is the way to have
					// none: its option with the level beside it keeps it
					if model, level := ompLevel(cur); level != "" && model != v {
						v += level
					}
				}
				model, _, one := ompSplit(v)
				if ref, ok := strings.CutPrefix(model, magpieID+"/"); one && ok && isMagpie(ref) {
					if !ompOnMagpie(cur) {
						stash(map[string]string{w: kept()})
					}
					if err := writeMagpie(); err != nil {
						return err
					}
					return edit.SetYAML(path, edit.KV{Path: k, Value: v})
				}
				forget(w)
				if err := edit.SetYAML(path, edit.KV{Path: k, Value: v}); err != nil {
					return err
				}
				return dropMagpie()
			},
			Options: func(cur map[string]string) []Option {
				opts := append(ompOwnOptions(pick("models"), cur[key]), viaMagpieShared("omp", magpieID+"/")...)
				// a catalog model with a thinking level is offered as it
				// reads, beside the model: that keeps the level, the model
				// alone clears it
				if model, level := ompLevel(cur[key]); level != "" {
					if i := slices.IndexFunc(opts, func(o Option) bool { return o.Ref != "" && o.Value == model }); i >= 0 {
						o := opts[i]
						o.Value, o.Label = cur[key], o.Label+" · "+level[1:]
						opts = slices.Insert(opts, i+1, o)
					}
				}
				return opts
			},
		}
	}
	roles := []Field{
		role("model", "model", "default", false),
		// omp's own agents run on its roles (src/task/agents.ts,
		// prompts/agents): task on @task; scout and sonic on @smol;
		// reviewer on @slow, also the eval tool's "slow" tier. Unset,
		// task gives its agent the parent session's model, and smol
		// and slow take the default role's (model-resolver.ts,
		// shouldInheritDefaultBeforePriority). The designer role went
		// in omp 18.1.5. smol is labelled as omp names it: "small" is
		// other agents' picker of its own
		role("subagent", "subagents", "task", true),
		role("small", "smol", "smol", true),
		role("slow", "slow", "slow", true),
		// plan mode's role (omp's "Architect"), images handed to a model
		// that sees them, and the second opinion the advisor gives. Unset,
		// omp picks for each from its own list; the advisor takes the slow
		// role first (model-resolver.ts, ROLE_CONFIGURED_FALLBACK), never
		// the default's (#1397)
		role("plan", "plan", "plan", true),
		role("vision", "vision", "vision", true),
		role("advisor", "advisor", "advisor", true),
		// the rest of omp's chat roles (CHAT_MODEL_ROLE_IDS in pi-tui's
		// model-browser.ts, 18.6 to 18.8.8; #1397 named commit and tiny):
		// commit writes commit messages and falls to smol when unset
		// (commit/model-selection.ts), tiny names sessions and classifies
		// and takes smol's model, memory takes tiny's, else smol's
		// (ROLE_CONFIGURED_FALLBACK). omp's other roles (KIND_ROLE_IDS)
		// are for kinds of model other than chat: image making, web
		// search, speech, dictation, and the judge its find tool turns on
		// for a TypeSafe jev model. Of those magpie writes image models
		// for omp (ompImageModels), picked in omp's own Roles view
		role("commit", "commit", "commit", true),
		role("tiny", "tiny", "tiny", true),
		role("memory", "memory", "memory", true),
	}
	// the default role's model is what a new session starts on only while
	// enabledModels takes it in, so a model picked for it joins the list
	// there is (omp_scope.go, #1769); a reset puts back what the user had,
	// which is theirs to have scoped or not
	setDefault := roles[0].Set
	roles[0].Set = func(v string) error {
		if err := setDefault(v); err != nil || v == "" {
			return err
		}
		if model, _, one := ompSplit(roleGet("default")()); one && model != "" {
			return ompScopeWith(path, model)
		}
		return nil
	}
	// a model picked for the advisor is one the user wants reviewing: omp
	// runs none while advisor.enabled is unset (its default is off), so the
	// pick turns it on then (#6408, #5019), and the role reset takes back
	// what the pick turned on. One the user set either way stays theirs
	advisor := slices.IndexFunc(roles, func(f Field) bool { return f.Key == "advisor" })
	setAdvisor := roles[advisor].Set
	roles[advisor].Set = func(v string) error {
		if err := setAdvisor(v); err != nil {
			return err
		}
		if v == "" {
			if unstash(ompAdvisorTurnedOn(path)) != "" && ompAdvisorOn(path) == "true" {
				return edit.DelYAML(path, "advisor.enabled")
			}
			return nil
		}
		if ompAdvisorOn(path) != "" {
			return nil
		}
		if err := edit.SetYAML(path, edit.KV{Path: "advisor.enabled", Value: true}); err != nil {
			return err
		}
		stash(map[string]string{ompAdvisorTurnedOn(path): "1"})
		return nil
	}
	// each role but the default one has a thinking level of its own, the
	// ":level" omp reads off the end of the role's model
	// (splitThinkingSuffix, resolveModelRoleValue): #1397, the slow role at
	// high while sessions start at medium. It is the role's value written
	// again, so it goes through the role's own Set (magpie's provider, the
	// stash). Offered while the role is one model: a role that follows
	// another, or a list omp falls back through, has no level here. Unset,
	// the role runs at the session's level
	fields := []Field{roles[0]}
	for _, r := range roles[1:] {
		fields = append(fields, r, Field{
			Key: r.Key + "_thinking", Label: r.Label + " thinking", Quiet: true,
			Get: func() string {
				if _, level, one := ompSplit(r.Get()); one {
					return strings.TrimPrefix(level, ":")
				}
				return ""
			},
			Set: func(v string) error {
				cur := r.Get()
				model, _, one := ompSplit(cur)
				if cur == "" || !one {
					if v == "" {
						return nil
					}
					return fmt.Errorf("omp's %s role is not on one model: pick its model first", r.Label)
				}
				if v != "" && v != "off" && v != "auto" && !slices.Contains(ompEfforts, v) {
					return fmt.Errorf("%q is not one of omp's thinking levels", v)
				}
				if v != "" {
					model += ":" + v
				}
				return r.Set(model)
			},
			Options: func(cur map[string]string) []Option {
				if _, _, one := ompSplit(cur[r.Key]); cur[r.Key] == "" || !one {
					return nil
				}
				return static(append([]string{"auto", "off"}, ompEfforts...)...)
			},
		})
	}
	return &Agent{
		ID: "omp", Name: "omp", Icon: "omp", Aliases: []string{"oh-my-pi"}, Spelled: prefixed,
		UA:  []string{"oh-my-pi"},
		Bin: "omp", Dir: dir, Path: path,
		// a role's thinking level is omp's, after whichever model it is on;
		// a list of models is the user's own
		SplitSuffix: ompSplit,
		ListOnly:    true,
		Sync: func() error {
			models := pick("models")
			if err := syncYAML(models, "providers."+magpieID, func() any { return ompProvider(models) }); err != nil {
				return err
			}
			// an entry on a model magpie no longer gives omp names nothing
			// (#2055, scope_gone.go)
			return ompScopeSynced(path, models, gone)
		},
		// a provider renamed takes its models' ids in models.yml with it; a
		// name left on the old one omp would pass over, with a warning. So
		// does what a role had before magpie (a list may name magpie's
		// models), which a reset puts back
		RenameRefs: func(from, to string) (bool, error) {
			old, now := magpieID+"/"+from+"/", magpieID+"/"+to+"/"
			move := func(v string) string {
				return ompRefs(v, func(m string) string {
					if rest, ok := strings.CutPrefix(m, old); ok {
						return now + rest
					}
					return m
				})
			}
			moved := false
			if err := edit.EditYAMLStrings(path, ompRefKeys, func(v string) string {
				nv := move(v)
				moved = moved || nv != v
				return nv
			}); err != nil {
				return moved, err
			}
			if err := ompScopeOnce(path); err != nil {
				return moved, err
			}
			scopeGoneRename(gone, from, to)
			for _, w := range was {
				s := stashShared()[w]
				ns := move(s)
				if ompYAMLList(s) {
					t, err := edit.EditYAMLTextStrings(edit.YAMLText(s), move)
					if err != nil {
						return moved, err
					}
					ns = string(t)
				}
				if ns != s {
					stash(map[string]string{w: ns})
				}
			}
			return moved, nil
		},
		Notice: func() string {
			if Running(`(^|/)omp( |$)`, `@oh-my-pi/pi-coding-agent`) {
				return noticeOmp.String()
			}
			return ""
		},
		Check: func() string {
			// whatever keeps magpie's provider in models.yml has its wiring
			// checked, a fallback chain alone as much as a role
			if used, _ := onMagpie(); !used {
				return ""
			}
			models := pick("models")
			if d := wiringOff("omp", models, func(k string) (string, bool) { return edit.GetYAML(models, "providers."+magpieID+"."+k) },
				"baseUrl", at.v1()); d != "" {
				return d
			}
			// a default role on magpie that enabledModels leaves out never
			// starts; setting it again adds it there (#1769)
			if def := roleGet("default")(); ompOnMagpie(def) {
				if m := ompScopeLeavesOut(path, def); m != "" {
					return "omp's enabledModels (" + filepath.Base(path) + ") leaves out " + m + ", so new sessions start on another of its models"
				}
			}
			return ""
		},
		Fields: append(append(fields, Field{
			// the thinking level sessions start with, as omp's settings save
			// it; unset omp takes high. auto has omp pick a level each turn:
			// not a level a model lists, so it is offered here and kept out of
			// ompEfforts, which a model's thinking levels are filtered by. First,
			// as omp's own picker has it (16.3.5 and 18.4.4 alike)
			Key: "effort", Label: "thinking",
			Get: func() string { v, _ := edit.GetYAML(path, "defaultThinkingLevel"); return v },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAML(path, "defaultThinkingLevel")
				}
				return edit.SetYAML(path, edit.KV{Path: "defaultThinkingLevel", Value: v})
			},
			Options: func(map[string]string) []Option { return static(append([]string{"auto"}, ompEfforts...)...) },
		}), ompAdvisorFields(path)...),
	}
}

type ompModel struct {
	ID        string       `yaml:"id"`
	Name      string       `yaml:"name,omitempty"`
	API       string       `yaml:"api,omitempty"`
	BaseURL   string       `yaml:"baseUrl,omitempty"`
	Reasoning bool         `yaml:"reasoning"`
	Thinking  *ompThinking `yaml:"thinking,omitempty"`
	Context   int          `yaml:"contextWindow,omitempty"`
	MaxTokens int          `yaml:"maxTokens,omitempty"`
	Input     []string     `yaml:"input,omitempty"`
	// Kind is what the model is for when it isn't chat: "image" for one
	// that draws, which omp's image role takes and no chat role does
	// (ompImageModels)
	Kind string `yaml:"kind,omitempty"`
	// Cost is the model's price in USD per million tokens, the unit omp's
	// calculateUsageCost divides by (omp 18.9.1, pi-catalog
	// src/models.ts). omp's custom provider prices a call by it alone;
	// without it omp takes a bundled model's its fuzzy id match finds, and
	// a group/* id matches none, so its calls cost 0 (taotao7 on Discord).
	Cost *ompCost `yaml:"cost,omitempty"`
}

// ompCost is a model's cost in omp's models.yml (omp 18.9.1,
// models-config-schema-bundle.ts: input, output, cacheRead, cacheWrite).
type ompCost struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cacheRead"`
	CacheWrite float64 `yaml:"cacheWrite"`
}

type ompThinking struct {
	Mode    string   `yaml:"mode"`
	Efforts []string `yaml:"efforts"`
}

type ompProviderEntry struct {
	BaseURL string `yaml:"baseUrl"`
	API     string `yaml:"api"`
	// Auth is none on loopback, where the gateway asks for no key; APIKey,
	// for an omp that reaches it from beyond (a WSL distro under NAT), the
	// key the gateway is shared on the network with
	Auth   string `yaml:"auth,omitempty"`
	APIKey string `yaml:"apiKey,omitempty"`
	// Headers name omp to the gateway: omp 16.x asks with Bun's User-Agent
	// and only a later one with its own (omp/18.4.4), so its requests went
	// to "Bun" in usage and past omp's own rules and stand-ins
	Headers map[string]string `yaml:"headers,omitempty"`
	// Compat names the header omp sends its session in. omp names the
	// conversation on Anthropic's Messages API (X-Claude-Code-Session-Id),
	// and on Chat and Responses only to OpenAI itself, so a turn of omp's on
	// Chat or Responses reached the gateway with no session: its usage had
	// none and "Record gateway conversations" kept none of it (akic404 on
	// Discord). promptCacheSessionHeader has omp send its session id in it.
	Compat map[string]string `yaml:"compat,omitempty"`
	Models []ompModel        `yaml:"models"`
}

// ompSessionSince is the first omp whose compat takes
// promptCacheSessionHeader (pi-catalog 16.0.6).
const ompSessionSince = "16.0.6"

// ompMaxSince is the first omp whose models.yml takes max as a thinking
// effort (pi-ai 16.4.0); 16.3.5's schema stops at xhigh and turns the whole
// file away over a max.
const ompMaxSince = "16.4.0"

// ompVersion is the version of the omp on PATH, "" when not known; a var so
// tests can fake it.
var ompVersion = func() string { return (&Agent{ID: "omp", Bin: "omp"}).InstalledVersion() }

// ompTakesMax says whether omp at version v takes max in models.yml. One
// whose version isn't known is taken for an older one: a max it refuses
// costs every model magpie gives it, an xhigh in its place only the top
// level of a model that has both.
func ompTakesMax(v string) bool {
	return v != "" && !Newer(ompMaxSince, v)
}

// ompProvider is magpie's entry in models.yml. The thinking efforts are the
// levels omp offers for the model; on Chat it sends them as
// reasoning_effort.
//
// As for Pi, each model is asked on the API its provider speaks natively, so
// the gateway relays what omp sent as it is instead of translating Chat: a
// Claude over Chat lost its thinking's signatures between tool turns, as
// Chat has no place for them. One served on OpenAI's Responses API goes to
// baseUrl/responses; one on Anthropic's Messages API to the gateway's
// /v1/messages (omp adds the /v1). That one thinks adaptively when it takes
// nothing else, else on a budget: omp's anthropic-budget-effort would also
// send output_config.effort, which Sonnet 4.5 and Haiku 4.5 refuse. One on
// Gemini's API goes to the gateway's /v1beta on google-generative-ai
// (#4277, ompGeminiMode).
func ompProvider() ompProviderEntry { return ompProviderAt(gateway.URL(), ompVersion()) }

// ompProviderAt is ompProvider for an omp of version (as ompVersion) that
// reaches the gateway at gw.
func ompProviderAt(gw, version string) ompProviderEntry {
	takesMax := ompTakesMax(version)
	ms := []ompModel{}
	for _, m := range sharedModels("omp") {
		e := ompModel{ID: m.ID, Name: m.Name, Context: m.Context, MaxTokens: maxTokens(m)}
		if p := m.Price; p != nil {
			e.Cost = &ompCost{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
		}
		mode := "effort"
		switch {
		case slices.Contains(m.APIs, string(provider.Responses)):
			e.API = "openai-responses"
		case slices.Contains(m.APIs, string(provider.Anthropic)):
			e.API, e.BaseURL = "anthropic-messages", gw
			mode = "budget"
			if gateway.AdaptiveThinking(m.ID) {
				mode = "anthropic-adaptive"
			}
		case slices.Contains(m.APIs, string(provider.Gemini)):
			// #4277: a Gemini model on Gemini's own API at the gateway's
			// /v1beta, as OmO's; omp posts to baseUrl/models/<id>:
			// streamGenerateContent?alt=sse with x-goog-api-key, the key
			// being "N/A" (kNoAuth) under auth: none, which loopback takes
			// (pi-ai providers/google.ts, 16.0.0 to 18.14.0)
			e.API, e.BaseURL = "google-generative-ai", gw+"/v1beta"
			mode = ompGeminiMode(m.ID)
		}
		// Without input omp takes it from a bundled model its fuzzy id match
		// finds, else text only; a model the source never answered for is
		// left to that guess.
		switch {
		case m.Images:
			e.Input = []string{"text", "image"}
		case m.ImageInput != nil:
			e.Input = []string{"text"}
		}
		var efforts []string
		for _, x := range ompEfforts { // in omp's order
			// before omp 16.4.0 a model's efforts stop at xhigh (16.3.5
			// turns the whole file away over a max): a model whose top is
			// max offers xhigh, which the gateway fits to max when the
			// model has no xhigh of its own
			if x == "max" && !takesMax {
				continue
			}
			if slices.Contains(m.Efforts, x) || x == "xhigh" && !takesMax && slices.Contains(m.Efforts, "max") {
				efforts = append(efforts, x)
			}
		}
		if len(efforts) > 0 {
			e.Reasoning = true
			e.Thinking = &ompThinking{Mode: mode, Efforts: efforts}
		}
		ms = append(ms, e)
	}
	images := ompImageModels(version, ms)
	ms = append(ms, images...)
	e := ompProviderEntry{BaseURL: gw + "/v1", API: "openai-completions", Auth: "none",
		Headers: map[string]string{"User-Agent": "omp"}, Models: ms}
	if version != "" && !Newer(ompSessionSince, version) {
		e.Compat = map[string]string{"promptCacheSessionHeader": "x-session-affinity"}
	}
	// whqtian on Discord: a WSL omp under NAT asks Windows' address, where
	// the gateway turns a request without a named key away
	// on loopback too once an image model is listed: omp's generate_image
	// skips a model of a keyless provider as "credentials unavailable"
	// (image-gen.ts, isAuthenticated turns auth: none's kNoAuth away; omp
	// 18.15.0 run against a stand-in gateway), where chat goes without one
	if key := keyAt(gw); key != gateway.Token || len(images) > 0 {
		e.Auth, e.APIKey = "", key
	}
	return e
}

// ompKindSince is the first omp whose models.yml takes a model's kind and
// a runner API such as openai-images (pi-coding-agent 18.7.0,
// models-config-schema-bundle.ts); 18.6.3's schema knows neither and turns
// the whole file away over an api it doesn't list.
const ompKindSince = "18.7.0"

// ompImageModels are magpie's image models (Settings › Models › Images, as
// generate_image offers them) as omp's image role takes them (treeman_26897
// on Discord): kind image on openai-images, which omp's generate_image
// posts to the provider's baseUrl + /images/generations, or /images/edits
// with images to change (pi-ai images/openai-images.ts, omp 18.7.0 to
// 18.15.0), the gateway's /v1/images. The role accepts a model of kind
// image alone (model-roles.ts), and a chat model is of kind chat, so before
// these the role listed none of magpie's. With one listed, the provider
// takes the gateway's key in place of auth: none (ompProviderAt). None for
// an omp before ompKindSince, or one whose version isn't known, as
// ompTakesMax; nor one whose id a chat model in chat has already, which one
// id can't be both.
func ompImageModels(version string, chat []ompModel) []ompModel {
	if version == "" || Newer(ompKindSince, version) {
		return nil
	}
	var out []ompModel
	for _, m := range gateway.ImageModels() {
		if slices.ContainsFunc(chat, func(c ompModel) bool { return c.ID == m.ID }) {
			continue
		}
		in := []string{"text"}
		if m.Images {
			in = append(in, "image")
		}
		out = append(out, ompModel{ID: m.ID, Name: m.Name, API: "openai-images", Kind: "image", Input: in})
	}
	return out
}

// ompGeminiMode is how omp asks a Gemini model on google-generative-ai
// to think. Gemini 3 and later take thinkingConfig.thinkingLevel
// (google-level: omp sends minimal to high as MINIMAL to HIGH, xhigh and max
// as HIGH); Gemini 2.x and earlier take a thinkingBudget and refuse a level,
// so they go on budget, which omp sends as -1, the model's own (pi-ai
// stream.ts and pi-catalog's gemini.kdl, omp 18.14.0). "effort" there is a
// budget too, the same -1.
func ompGeminiMode(id string) string {
	name := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	if strings.HasPrefix(name, "gemini-1") || strings.HasPrefix(name, "gemini-2") {
		return "budget"
	}
	return "google-level"
}

// ompKept is magpie's entry e laid over the one in the models.yml at path.
// omp keeps keys its schema doesn't name and never writes the file itself,
// so what is there beside magpie's is the user's: modelOverrides (a model's
// context or cost), discovery, disableStrictTools, remoteCompaction, a
// header or compat flag of their own (models-config-schema-bundle.ts
// ProviderConfigSchema). Each stays; the address, API, auth, key and model
// list are magpie's, an auth or key it no longer writes gone with it, so a
// loopback's auth: none never sits beside the key a WSL omp needs (#2030).
func ompKept(path string, e ompProviderEntry) any {
	return yamlKept(path, "providers."+magpieID, e, "baseUrl", "api", "auth", "apiKey", "models")
}

// ompOwnOptions lists the models of the providers the user added to omp's
// models.yml, then the ones models.dev knows for the current value's
// provider, spelled as ownOptions spells them (the model's name as the
// note). A value offered twice is offered once, with the name and icon
// either one had. A provider in both stays one group, where it first comes:
// the picker draws a heading at each change of group. A provider with only
// discovery has its models listed by omp asking it at run time, out of
// magpie's sight, so it offers none here.
func ompOwnOptions(modelsFile, cur string) []Option {
	var f struct {
		Providers map[string]struct {
			Models []struct {
				ID   string `yaml:"id"`
				Name string `yaml:"name"`
			} `yaml:"models"`
		} `yaml:"providers"`
	}
	if b, err := os.ReadFile(modelsFile); err == nil {
		yaml.Unmarshal(b, &f)
	}
	providers := make([]string, 0, len(f.Providers))
	for p := range f.Providers {
		if p != magpieID {
			providers = append(providers, p)
		}
	}
	sort.Strings(providers)
	var opts []Option
	for _, p := range providers {
		name := catalog.ProviderName(p)
		if name == "" {
			name = p
		}
		for _, m := range f.Providers[p].Models {
			if m.ID != "" {
				opts = append(opts, Option{Value: p + "/" + m.ID, Note: m.Name, Icon: modelIcon(p, m.ID), Group: name, GroupIcon: providerIcon(p), Direct: name})
			}
		}
	}
	at := map[string]int{}
	var out []Option
	for _, o := range append(opts, ownOptionsFrom(ompRegistry, "", cur)...) {
		i, dup := at[o.Value]
		if !dup {
			at[o.Value] = len(out)
			out = append(out, o)
			continue
		}
		if out[i].Note == "" {
			out[i].Note = o.Note
		}
		if out[i].Icon == "" {
			out[i].Icon = o.Icon
		}
		if out[i].GroupIcon == "" {
			out[i].GroupIcon = o.GroupIcon
		}
	}
	first := map[string]int{}
	for i, o := range out {
		if _, ok := first[o.Group]; !ok {
			first[o.Group] = i
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return first[out[i].Group] < first[out[j].Group] })
	return out
}

// what omp says after a change (notice.go)
var (
	noticeOmp = newNotice("omp reads its settings at start-up — restart open omp sessions to use this.")
)
