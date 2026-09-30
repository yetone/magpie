package agent

// omp (oh-my-pi, a fork of Pi) keeps its settings in ~/.omp/agent/config.yml
// (or where ompDir says its variables move it),
// the model of each role under modelRoles as "provider/model", and providers
// of the user's own in models.yml beside it. magpie adds itself there as the
// provider "magpie", keyless (auth: none), with the catalog as its models; a
// model through magpie is "magpie/<provider>/<model>", which omp matches
// whole against provider/id. The other roles, the fallback chains and the
// like may name them as well (ompRefKeys); magpie stays while any does.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
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
var ompProfileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ompDir is omp's agent folder, found as omp's pi-utils (dirs.ts) finds it:
// under ~/.omp, or ~/$PI_CONFIG_DIR; a profile's (OMP_PROFILE, else
// PI_PROFILE; "default" is none) is profiles/<name>/agent there; with none,
// PI_CODING_AGENT_DIR — the variable Pi reads — moves it, else it is
// agent there. omp takes that variable as given, without expanding "~".
func ompDir(home string) string {
	root := filepath.Join(home, ".omp")
	if d := os.Getenv("PI_CONFIG_DIR"); d != "" {
		root = filepath.Join(home, d)
	}
	p, set := os.LookupEnv("OMP_PROFILE")
	if !set {
		p = os.Getenv("PI_PROFILE")
	}
	if p = strings.TrimSpace(p); p != "" && p != "default" && ompProfileName.MatchString(p) && !strings.HasSuffix(p, ".") {
		return filepath.Join(root, "profiles", p, "agent")
	}
	if d := os.Getenv("PI_CODING_AGENT_DIR"); filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	return filepath.Join(root, "agent")
}

func omp(home string) *Agent {
	dir := ompDir(home)
	// omp reads the .yml and falls back to the .yaml
	pick := func(name string) string {
		yml := filepath.Join(dir, name+".yml")
		if _, err := os.Stat(yml); err != nil {
			if _, err := os.Stat(filepath.Join(dir, name+".yaml")); err == nil {
				return filepath.Join(dir, name+".yaml")
			}
		}
		return yml
	}
	path := pick("config")
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
		return edit.SetYAML(models, edit.KV{Path: "providers." + magpieID, Value: ompProvider()})
	}
	// dropMagpie takes magpie's provider out of models.yml once nothing in
	// the config is on magpie; while something is (a fallback chain, another
	// role), it writes it afresh instead, so a role set off magpie — applying
	// again what Check found — mends the wiring that is left.
	dropMagpie := func() error {
		used, err := onMagpie()
		switch {
		case err != nil:
			return err
		case used:
			return writeMagpie()
		}
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
				opts := append(ownOptions("", cur[key]), viaMagpie("omp", magpieID+"/")...)
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
	return &Agent{
		ID: "omp", Name: "omp", Icon: "omp", Aliases: []string{"oh-my-pi"},
		UA:  []string{"oh-my-pi"},
		Bin: "omp", Dir: dir, Path: path,
		// a role's thinking level is omp's, after whichever model it is on;
		// a list of models is the user's own
		SplitSuffix: ompSplit,
		Sync: func() error {
			return syncYAML(pick("models"), "providers."+magpieID, func() any { return ompProvider() })
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
			for _, w := range was {
				s := stashLoad()[w]
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
				return "omp reads its settings at start-up — restart open omp sessions to use this."
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
			return wiringOff("omp", models, func(k string) (string, bool) { return edit.GetYAML(models, "providers."+magpieID+"."+k) },
				"baseUrl", gatewayV1())
		},
		Fields: []Field{
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
			{
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
			},
		},
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
}

type ompThinking struct {
	Mode    string   `yaml:"mode"`
	Efforts []string `yaml:"efforts"`
}

type ompProviderEntry struct {
	BaseURL string `yaml:"baseUrl"`
	API     string `yaml:"api"`
	Auth    string `yaml:"auth"`
	// Headers name omp to the gateway: omp 16.x asks with Bun's User-Agent
	// and only a later one with its own (omp/18.4.4), so its requests went
	// to "Bun" in usage and past omp's own rules and stand-ins
	Headers map[string]string `yaml:"headers,omitempty"`
	Models  []ompModel        `yaml:"models"`
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
// send output_config.effort, which Sonnet 4.5 and Haiku 4.5 refuse.
func ompProvider() ompProviderEntry {
	ms := []ompModel{}
	for _, m := range magpieModels("omp") {
		e := ompModel{ID: m.ID, Name: m.Name, Context: m.Context, MaxTokens: maxTokens(m)}
		mode := "effort"
		switch {
		case slices.Contains(m.APIs, string(provider.Responses)):
			e.API = "openai-responses"
		case slices.Contains(m.APIs, string(provider.Anthropic)):
			e.API, e.BaseURL = "anthropic-messages", gateway.URL()
			mode = "budget"
			if gateway.AdaptiveThinking(m.ID) {
				mode = "anthropic-adaptive"
			}
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
			// a model's efforts stop at xhigh (omp 16.3.5 turns the whole
			// file away over a max): a model whose top is max offers xhigh,
			// which the gateway fits to max
			if x == "xhigh" && slices.Contains(m.Efforts, "max") || x != "max" && slices.Contains(m.Efforts, x) {
				efforts = append(efforts, x)
			}
		}
		if len(efforts) > 0 {
			e.Reasoning = true
			e.Thinking = &ompThinking{Mode: mode, Efforts: efforts}
		}
		ms = append(ms, e)
	}
	return ompProviderEntry{BaseURL: gatewayV1(), API: "openai-completions", Auth: "none",
		Headers: map[string]string{"User-Agent": "omp"}, Models: ms}
}
