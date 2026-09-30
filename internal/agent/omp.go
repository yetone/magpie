package agent

// omp (oh-my-pi, a fork of Pi) keeps its settings in ~/.omp/agent/config.yml
// (or where ompDir says its variables move it),
// the model of each role under modelRoles as "provider/model", and providers
// of the user's own in models.yml beside it. magpie adds itself there as the
// provider "magpie", keyless (auth: none), with the catalog as its models; a
// model through magpie is "magpie/<provider>/<model>", which omp matches
// whole against provider/id.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// ompEfforts are the thinking levels omp knows.
var ompEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

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
		// another role may still go through magpie
		for _, v := range edit.GetYAMLMap(path, "modelRoles") {
			if usesMagpie(v) {
				return nil
			}
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
				return append(ownOptions("", cur["model"]), viaMagpie("omp", magpieID+"/")...)
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
// levels omp offers for the model; it sends them as reasoning_effort.
func ompProvider() ompProviderEntry {
	ms := []ompModel{}
	for _, m := range magpieModels("omp") {
		e := ompModel{ID: m.ID, Name: m.Name, Context: m.Context, MaxTokens: m.Output}
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
			if slices.Contains(m.Efforts, x) {
				efforts = append(efforts, x)
			}
		}
		if len(efforts) > 0 {
			e.Reasoning = true
			e.Thinking = &ompThinking{Mode: "effort", Efforts: efforts}
		}
		ms = append(ms, e)
	}
	return ompProviderEntry{BaseURL: gatewayV1(), API: "openai-completions", Auth: "none", Models: ms}
}
