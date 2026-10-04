package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
)

// A Pi-family agent's built-in providers are the ones its own model
// registry ships, not models.dev's: models.dev has no openai-codex (Pi's
// "OpenAI Codex (legacy)" sign-in, the ChatGPT backend), so a Pi signed in
// to it was offered none of its models (#709). The registry is read from
// the package the agent was installed from, so it is the list that agent
// itself offers in its /model, and moves with it on every update.

// nodeModulesOf is where a CLI installed from one of pkgs resolves its
// packages: its own package's node_modules, then the node_modules it sits
// in (npm, bun and Homebrew's npm link the command into it). nil when the
// command isn't on PATH or wasn't installed from npm. A var for tests.
var nodeModulesOf = findNodeModules

func findNodeModules(bin string, pkgs []string) []string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	slash := filepath.ToSlash(real)
	for _, pkg := range pkgs {
		modules := ""
		if i := strings.Index(slash, "/node_modules/"+pkg+"/"); i >= 0 {
			modules = filepath.FromSlash(slash[:i] + "/node_modules")
		} else if d := filepath.Join(filepath.Dir(path), "node_modules"); isDir(filepath.Join(d, filepath.FromSlash(pkg))) {
			modules = d // npm's shim on Windows sits beside its node_modules
		}
		if modules != "" {
			return []string{filepath.Join(modules, filepath.FromSlash(pkg), "node_modules"), modules}
		}
	}
	return nil
}

// safeProvider says p can be a file name: auth.json's keys come from the
// user's file.
func safeProvider(p string) bool {
	return p != "" && p != "." && p != ".." && !strings.ContainsAny(p, `/\:`)
}

// registryModel is a model as Pi's and omp's registries list it.
type registryModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // Pi: chat, image, classifier
	Kind    string   `json:"kind"` // omp: "" for chat, image, embedding…
	Input   []string `json:"input"`
	Context int      `json:"contextWindow"`
	Output  int      `json:"maxTokens"`
}

func (m registryModel) chat() bool {
	return m.ID != "" && (m.Type == "" || m.Type == "chat") && (m.Kind == "" || m.Kind == "tiny")
}

func (m registryModel) model(provider string) catalog.Model {
	name := m.Name
	if name == "" {
		name = m.ID
	}
	images := false
	for _, in := range m.Input {
		images = images || in == "image"
	}
	return catalog.Model{ID: m.ID, Name: name, Provider: provider, Images: images, Context: m.Context, Output: m.Output}
}

// orderedObject calls fn with each key and value of a JSON object, in the
// order the file has them: the registry's order is the agent's own.
func orderedObject(b []byte, fn func(key string, v json.RawMessage) error) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not an object")
	}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return err
		}
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// modelsIn is the chat models of an object of models by id, in its order.
func modelsIn(b []byte, provider string) []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	orderedObject(b, func(_ string, v json.RawMessage) error {
		var m registryModel
		if json.Unmarshal(v, &m) == nil && m.chat() && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m.model(provider))
		}
		return nil
	})
	return out
}

// piPackages are the npm packages Pi is installed from, newest name first.
var piPackages = []string{"@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"}

// piRegistry is the models Pi's installed registry has for provider p:
// pi-ai's dist/providers/data/<p>.json (Pi 1.0), groups by API of models
// by "type:id". nil when Pi or the provider isn't found there.
func piRegistry(bin string) func(p string) []catalog.Model {
	return func(p string) []catalog.Model {
		if !safeProvider(p) {
			return nil
		}
		for _, modules := range nodeModulesOf(bin, piPackages) {
			for _, ai := range []string{"@earendil-works/pi-ai", "@mariozechner/pi-ai"} {
				file := filepath.Join(modules, filepath.FromSlash(ai), "dist", "providers", "data", p+".json")
				ms, err := filememo.Read("pi models", file, func(b []byte) ([]catalog.Model, error) {
					var out []catalog.Model
					err := orderedObject(b, func(_ string, group json.RawMessage) error {
						out = append(out, modelsIn(group, p)...)
						return nil
					})
					return out, err
				})
				if err == nil && len(ms) > 0 {
					return ms
				}
			}
		}
		return nil
	}
}

// ompRegistry is the models omp's installed catalog has for provider p:
// @oh-my-pi/pi-catalog's src/models.json, models by id under each provider.
func ompRegistry(p string) []catalog.Model {
	for _, modules := range nodeModulesOf("omp", []string{"@oh-my-pi/pi-coding-agent"}) {
		file := filepath.Join(modules, "@oh-my-pi", "pi-catalog", "src", "models.json")
		all, err := filememo.Read("omp models", file, func(b []byte) (map[string][]catalog.Model, error) {
			out := map[string][]catalog.Model{}
			err := orderedObject(b, func(provider string, v json.RawMessage) error {
				out[provider] = modelsIn(v, provider)
				return nil
			})
			return out, err
		})
		if err == nil {
			if ms := all[p]; len(ms) > 0 {
				return ms
			}
		}
	}
	return nil
}

// codexServed is, for openai-codex when the agent's registry isn't found,
// the models Codex CLI last listed for a ChatGPT account: the same backend.
func codexServed(p string) []catalog.Model {
	if p != "openai-codex" {
		return nil
	}
	ms := catalog.Codex()
	for i := range ms {
		ms[i].Provider = p
	}
	return ms
}

// nativeProviderNames are the names of built-in providers models.dev
// doesn't list, as their agents call them.
var nativeProviderNames = map[string]string{"openai-codex": "OpenAI Codex"}
