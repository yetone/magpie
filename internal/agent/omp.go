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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// ompEfforts are the thinking levels omp knows.
var ompEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// ompRefKeys are where omp's config names models: each role (a list to try
// in order, "a,b" or a sequence), the retry fallback chains (model keys and
// their entries), the models it cycles through (enabledModels, also scoped
// to paths) and the models of task agents. An entry may end in a thinking
// level ("magpie/deepseek/pro:max").
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
	get := func() string { v, _ := edit.GetYAML(path, "modelRoles.default"); return v }
	dropMagpie := func() error {
		// another role, a fallback chain, … may still go through magpie
		used := false
		if err := edit.EditYAMLStrings(path, ompRefKeys, func(v string) string {
			ompRefs(v, func(m string) string { used = used || usesMagpie(m); return m })
			return v
		}); err != nil || used {
			return err
		}
		return edit.DelYAML(pick("models"), "providers."+magpieID)
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
	return &Agent{
		ID: "omp", Name: "omp", Icon: "omp", Aliases: []string{"oh-my-pi"},
		UA:  []string{"oh-my-pi"},
		Bin: "omp", Dir: dir, Path: path,
		Sync: func() error {
			return syncYAML(pick("models"), "providers."+magpieID, func() any { return ompProvider() })
		},
		// a provider renamed takes its models' ids in models.yml with it; a
		// name left on the old one omp would pass over, with a warning
		RenameRefs: func(from, to string) (bool, error) {
			old, now := magpieID+"/"+from+"/", magpieID+"/"+to+"/"
			moved := false
			err := edit.EditYAMLStrings(path, ompRefKeys, func(v string) string {
				return ompRefs(v, func(m string) string {
					if rest, ok := strings.CutPrefix(m, old); ok {
						moved = true
						return now + rest
					}
					return m
				})
			})
			return moved, err
		},
		Notice: func() string {
			if Running(`(^|/)omp( |$)`, `@oh-my-pi/pi-coding-agent`) {
				return "omp reads its settings at start-up — restart open omp sessions to use this."
			}
			return ""
		},
		Check: func() string {
			if !usesMagpie(get()) {
				return ""
			}
			models := pick("models")
			return wiringOff("omp", models, func(k string) (string, bool) { return edit.GetYAML(models, "providers."+magpieID+"."+k) },
				"baseUrl", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				if v == "" {
					if err := edit.DelYAML(path, "modelRoles.default"); err != nil {
						return err
					}
					return dropMagpie()
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := writeMagpie(); err != nil {
						return err
					}
					return edit.SetYAML(path, edit.KV{Path: "modelRoles.default", Value: v})
				}
				if err := edit.SetYAML(path, edit.KV{Path: "modelRoles.default", Value: v}); err != nil {
					return err
				}
				return dropMagpie()
			},
			Options: func(cur map[string]string) []Option {
				return append(ompOwnOptions(pick("models"), cur["model"]), viaMagpie("omp", magpieID+"/")...)
			},
		}, {
			// the thinking level sessions start with, as omp's settings save
			// it; unset omp takes high (it also knows auto, its own pick)
			Key: "effort", Label: "thinking",
			Get: func() string { v, _ := edit.GetYAML(path, "defaultThinkingLevel"); return v },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAML(path, "defaultThinkingLevel")
				}
				return edit.SetYAML(path, edit.KV{Path: "defaultThinkingLevel", Value: v})
			},
			Options: func(map[string]string) []Option { return static(ompEfforts...) },
		}},
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
	BaseURL string     `yaml:"baseUrl"`
	API     string     `yaml:"api"`
	Auth    string     `yaml:"auth"`
	Models  []ompModel `yaml:"models"`
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
	return ompProviderEntry{BaseURL: gatewayV1(), API: "openai-completions", Auth: "none", Models: ms}
}

// ompListedModel is a model as `omp models --json` lists it; that lists chat
// models only, unless asked for another kind.
type ompListedModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Selector string `json:"selector"`
	Name     string `json:"name"`
}

// ompOwnOptions lists the models omp says it can use, by provider, then the
// ones models.dev knows for the current value's provider, spelled as
// ownOptions spells them (the model's name as the note). omp's own list has
// the providers built into it that are signed in (an Anthropic or ChatGPT
// sign-in, Cursor), the user's from models.yml and the models it
// discovered; magpie's entry is left out, its models are offered as
// magpie's. With no answer from omp (not yet, not installed, failed,
// something else printed) the providers of models.yml are read instead,
// where one with only discovery offers none. A value offered twice is
// offered once, with the name and icon either one had.
func ompOwnOptions(modelsFile, cur string) []Option {
	ms, ok := ompListed(modelsFile)
	if !ok {
		ms = ompFileModels(modelsFile)
	}
	var opts []Option
	for _, m := range ms {
		p, v := m.Provider, m.Selector
		if v == "" {
			v = p + "/" + m.ID
		}
		if p == magpieID || m.ID == "" {
			continue
		}
		name := catalog.ProviderName(p)
		if name == "" {
			name = p
		}
		opts = append(opts, Option{Value: v, Note: m.Name, Icon: modelIcon(p, m.ID), Group: name, GroupIcon: providerIcon(p)})
	}
	at := map[string]int{}
	var out []Option
	for _, o := range append(opts, ownOptions("", cur)...) {
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
	return out
}

// ompFileModels are the models of the providers in models.yml, by provider.
func ompFileModels(modelsFile string) []ompListedModel {
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
		providers = append(providers, p)
	}
	sort.Strings(providers)
	var out []ompListedModel
	for _, p := range providers {
		for _, m := range f.Providers[p].Models {
			out = append(out, ompListedModel{Provider: p, ID: m.ID, Name: m.Name})
		}
	}
	return out
}

// ompLists keeps what `omp models --json` said, by agent folder (the one
// models.yml is in). omp takes a few seconds to answer (it starts Bun and
// loads every provider) and the picker's options are read at every look at
// the agents, so, as with the CLIs' sign-ins (provider/cli_identity.go),
// what was said is served at once and omp is asked again behind it: once
// models.yml changes (a provider added or taken out), else ten minutes on (a
// sign-in; it lands in agent.db, which omp writes at every run, this one's
// too, so its time tells nothing). Until omp first answers the picker has
// models.yml's models; an ask that fails leaves the last answer.
var ompLists struct {
	sync.Mutex
	m map[string]*ompList
}

type ompList struct {
	models []ompListedModel
	ok     bool // omp answered once
	stamp  string
	at     time.Time
	asking chan struct{} // closed when the ask under way has answered
}

const ompListFor = 10 * time.Minute

func ompListed(modelsFile string) ([]ompListedModel, bool) {
	dir := filepath.Dir(modelsFile)
	stamp := ""
	if st, err := os.Stat(modelsFile); err == nil {
		stamp = fmt.Sprint(st.Size(), st.ModTime().UnixNano())
	}
	ompLists.Lock()
	if ompLists.m == nil {
		ompLists.m = map[string]*ompList{}
	}
	l := ompLists.m[dir]
	if l == nil {
		l = &ompList{}
		ompLists.m[dir] = l
	}
	if l.asking == nil && (l.at.IsZero() || l.stamp != stamp || time.Since(l.at) > ompListFor) {
		done := make(chan struct{})
		l.asking = done
		run := runOmpModels // the one of when it was asked, a test's fake too
		go func() {
			ms, err := askOmpModels(run)
			ompLists.Lock()
			if err == nil {
				l.models, l.ok = ms, true
			}
			l.stamp, l.at, l.asking = stamp, time.Now(), nil
			ompLists.Unlock()
			close(done)
		}()
	}
	ms, ok := l.models, l.ok
	ompLists.Unlock()
	return ms, ok
}

// runOmpModels runs `omp models --json` and gives what it printed; a var so
// tests can fake it, and none under test runs a real omp.
var runOmpModels = func() ([]byte, error) {
	if testing.Testing() {
		return nil, errors.New("no omp under test")
	}
	bin, err := exec.LookPath("omp")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// in the environment magpie has, so omp takes the same OMP_PROFILE,
	// PI_CONFIG_DIR and PI_CODING_AGENT_DIR ompDir followed, and from the
	// home folder rather than magpie's (omp adds the settings of the project
	// it runs in)
	cmd := proc.CommandContext(ctx, bin, "models", "--json")
	cmd.Stdin = nil
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = netproxy.Env(nil) // it may fetch a provider's model list
	return cmd.Output()
}

func askOmpModels(run func() ([]byte, error)) ([]ompListedModel, error) {
	out, err := run()
	if err != nil {
		return nil, err
	}
	var r struct {
		Models []ompListedModel `json:"models"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	return r.Models, nil
}
