package agent

// Mistral Vibe (mistralai/mistral-vibe, Apache-2.0, the `vibe` CLI) keeps
// its settings in config.toml in $VIBE_HOME, else ~/.vibe (vibe/utils/
// vibe_home.py). A provider is a [[providers]] entry (name, api_base,
// api_style "openai", backend "generic", extra_headers), a model a
// [[models]] entry naming it (name, the string sent to the provider; alias,
// what the /model picker shows and active_model holds; thinking_levels;
// supports_images; max_context_length), and the model in use is the
// top-level active_model, an alias.
//
// magpie adds itself as the provider "magpie": the gateway's /v1 as chat
// completions, with one model entry per catalog model, aliased
// "magpie/<provider>/<model>" so the catalog is in Vibe's /model picker, and
// points active_model at the one picked. What active_model was is stashed
// and put back when magpie steps out. Every other key and entry of the file
// is the user's and stays as written.
//
// Vibe reads a provider's key only from the environment variable its
// api_key_env_var names (or the keyring), never from config.toml. A keyless
// provider sends its extra_headers as they are, so the key naming Vibe to
// the gateway goes there as an Authorization header: nothing of magpie's is
// put in the user's environment or in ~/.vibe/.env. Vibe's requests say
// Mistral-Vibe/<version> as their User-Agent, which the gateway tells apart.
//
// Vibe's own Library places are ~/.vibe/skills/<name>/SKILL.md and
// ~/.vibe/AGENTS.md (vibe/core/config/harness_files/_paths.py,
// vibe/core/system_prompt.py), in internal/library/targets.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
)

// vibeID is Mistral Vibe's id; Avibe (avibe.go) is another program whose
// CLI is also called vibe.
const vibeID = "vibe"

// what Vibe says after a change (notice.go)
var noticeVibe = newNotice("Vibe reads its settings at start-up — restart open vibe sessions to use this.")

// vibeContext is what Vibe is told of a model whose context magpie doesn't
// know: without one it takes a default window with a warning.
const vibeContext = 128000

// vibeAlias is the alias of a catalog ref's model entry, what active_model
// holds.
func vibeAlias(ref string) string { return magpieID + "/" + ref }

// vibeDir is Vibe's folder: $VIBE_HOME (~ expanded), else ~/.vibe.
func vibeDir(at place) string {
	if d := strings.TrimSpace(at.getenv("VIBE_HOME")); d != "" {
		if rest, ok := strings.CutPrefix(d, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
			return at.join(strings.TrimLeft(rest, "/\\"))
		}
		return d
	}
	return at.join(".vibe")
}

// vibeNotAvibe turns away Avibe's `vibe` command (a Python package that
// also installs one): a binary that lives under an avibe folder isn't
// Mistral Vibe's.
func vibeNotAvibe(bin string) bool {
	for _, p := range []string{bin, evalLink(bin)} {
		if strings.Contains(strings.ToLower(filepath.ToSlash(p)), "avibe") {
			return true
		}
	}
	return false
}

func evalLink(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// vibeModel is one [[models]] entry, as Vibe's ModelConfig reads it.
type vibeModel struct {
	Name           string   `toml:"name"`
	Provider       string   `toml:"provider"`
	Alias          string   `toml:"alias"`
	Thinking       string   `toml:"thinking"`
	ThinkingLevels []string `toml:"thinking_levels"`
	SupportsImages *bool    `toml:"supports_images"`
	MaxContext     int      `toml:"max_context_length"`
}

// vibeConf is what magpie reads of Vibe's config.toml.
type vibeConf struct {
	ActiveModel string `toml:"active_model"`
	Providers   []struct {
		Name         string            `toml:"name"`
		APIBase      string            `toml:"api_base"`
		ExtraHeaders map[string]string `toml:"extra_headers"`
	} `toml:"providers"`
	Models []vibeModel `toml:"models"`
}

// vibeRead reads Vibe's config.toml; a missing file is an empty one, and
// one that doesn't parse is an error, never taken as empty.
func vibeRead(path string) (vibeConf, error) {
	var c vibeConf
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("Vibe's %s isn't TOML magpie can read; it was left as it is: %w", path, err)
	}
	return c, nil
}

// vibeMine reports whether a [[models]] entry is magpie's.
func vibeMine(m vibeModel) bool { return m.Provider == magpieID }

// vibeWant is magpie's models as Vibe is told them. A model with effort
// levels offers them beside off; one without offers off alone. A thinking
// level the user chose on one of magpie's models stays while the model
// still has it.
func vibeWant(was []vibeModel) []vibeModel {
	kept := map[string]string{}
	for _, m := range was {
		if vibeMine(m) {
			kept[m.Name] = m.Thinking
		}
	}
	var out []vibeModel
	for _, m := range sharedModels(vibeID) {
		ctx := m.Context
		if ctx <= 0 {
			ctx = unknownWindow(vibeContext)
		}
		e := vibeModel{Name: m.ID, Provider: magpieID, Alias: vibeAlias(m.ID), MaxContext: ctx, ThinkingLevels: []string{"off"}}
		var levels []string
		for _, l := range m.Efforts {
			if l != "none" && l != "off" {
				levels = append(levels, l)
			}
		}
		if len(levels) > 0 {
			e.ThinkingLevels = append(e.ThinkingLevels, levels...)
			e.Thinking = keptEffort(kept[m.ID], e.ThinkingLevels, "")
		}
		img := m.Images
		e.SupportsImages = &img
		out = append(out, e)
	}
	return out
}

// vibeSame reports whether two model lists say the same to Vibe.
func vibeSame(a, b []vibeModel) bool {
	return slices.EqualFunc(a, b, func(x, y vibeModel) bool {
		return x.Name == y.Name && x.Provider == y.Provider && x.Alias == y.Alias && x.Thinking == y.Thinking &&
			x.MaxContext == y.MaxContext && slices.Equal(x.ThinkingLevels, y.ThinkingLevels) &&
			(x.SupportsImages == nil) == (y.SupportsImages == nil) &&
			(x.SupportsImages == nil || *x.SupportsImages == *y.SupportsImages)
	})
}

// vibeBlocks is each model as one [[models]] entry.
func vibeBlocks(models []vibeModel) []string {
	var out []string
	for _, m := range models {
		var b strings.Builder
		b.WriteString("[[models]]\n")
		b.WriteString("name = " + strconv.Quote(m.Name) + "\n")
		b.WriteString("provider = " + strconv.Quote(m.Provider) + "\n")
		b.WriteString("alias = " + strconv.Quote(m.Alias) + "\n")
		if m.Thinking != "" {
			b.WriteString("thinking = " + strconv.Quote(m.Thinking) + "\n")
		}
		q := make([]string, len(m.ThinkingLevels))
		for i, l := range m.ThinkingLevels {
			q[i] = strconv.Quote(l)
		}
		b.WriteString("thinking_levels = [" + strings.Join(q, ", ") + "]\n")
		if m.SupportsImages != nil {
			b.WriteString("supports_images = " + strconv.FormatBool(*m.SupportsImages) + "\n")
		}
		if m.MaxContext > 0 {
			b.WriteString("max_context_length = " + strconv.Itoa(m.MaxContext) + "\n")
		}
		out = append(out, b.String())
	}
	return out
}

func vibeProviderBlock(base, key string) string {
	return "[[providers]]\n" +
		"name = " + strconv.Quote(magpieID) + "\n" +
		"api_base = " + strconv.Quote(base) + "\n" +
		"api_style = \"openai\"\n" +
		"backend = \"generic\"\n" +
		"extra_headers = { Authorization = " + strconv.Quote("Bearer "+key) + " }\n"
}

func vibe(home string) *Agent { return vibeAt(here(home)) }

func vibeAt(at place) *Agent {
	dir := vibeDir(at)
	path := filepath.Join(dir, "config.toml")
	keyPick := at.key("vibe:" + path + ":active_model")
	key := func() string { return agentKeyAt(vibeID, at.gw()) }
	ours := func(c vibeConf) bool {
		return strings.HasPrefix(c.ActiveModel, magpieID+"/") && slices.ContainsFunc(c.Models, func(m vibeModel) bool {
			return vibeMine(m) && m.Alias == c.ActiveModel
		})
	}
	get := func() string {
		c, _ := vibeRead(path)
		return c.ActiveModel
	}
	setPick := func(v string) error {
		if v == "" {
			return edit.DelTOMLTop(path, "active_model")
		}
		return edit.SetTOMLTop(path, edit.KV{Path: "active_model", Value: v})
	}
	// wire writes magpie's provider and models; it writes nothing when the
	// file has them all already
	wire := func() error {
		c, err := vibeRead(path)
		if err != nil {
			return err
		}
		want := vibeWant(c.Models)
		var have []vibeModel
		for _, m := range c.Models {
			if vibeMine(m) {
				have = append(have, m)
			}
		}
		if err := edit.SetTOMLArrayTables(path, "providers", "name", magpieID, []string{vibeProviderBlock(at.v1(), key())}); err != nil {
			return err
		}
		if vibeSame(have, want) {
			return nil
		}
		return edit.SetTOMLArrayTables(path, "models", "provider", magpieID, vibeBlocks(want))
	}
	unwire := func(own string) error {
		c, err := vibeRead(path)
		if err != nil {
			return err
		}
		if ours(c) {
			was := unstash(keyPick)
			if own != "" {
				was = own
			}
			if err := setPick(was); err != nil {
				return err
			}
		} else {
			forget(keyPick)
			if own != "" {
				if err := setPick(own); err != nil {
					return err
				}
			}
		}
		if err := edit.SetTOMLArrayTables(path, "models", "provider", magpieID, nil); err != nil {
			return err
		}
		return edit.SetTOMLArrayTables(path, "providers", "name", magpieID, nil)
	}
	return atomic(&Agent{
		ID: vibeID, Name: "Mistral Vibe", Icon: "mistral-color", Aliases: []string{"mistral-vibe"},
		Spelled: prefixed, NoNames: true,
		UA:  []string{"mistral-vibe"},
		Bin: "vibe", Dir: dir, Path: path, binNot: vibeNotAvibe,
		Notice: func() string {
			if Running(`(^|/)vibe( |$)`) {
				return noticeVibe.String()
			}
			return ""
		},
		ListOnly: true,
		Sync: func() error {
			c, err := vibeRead(path)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(c.Models, vibeMine) {
				return nil
			}
			return wire()
		},
		Check: func() string {
			c, err := vibeRead(path)
			if err != nil {
				return err.Error()
			}
			if !ours(c) {
				return ""
			}
			for _, p := range c.Providers {
				if p.Name != magpieID {
					continue
				}
				return wiringOff("Vibe", path, func(k string) (string, bool) {
					switch k {
					case "api_base":
						return p.APIBase, p.APIBase != ""
					case "extra_headers":
						h := p.ExtraHeaders["Authorization"]
						return strings.TrimPrefix(h, "Bearer "), h != ""
					}
					return "", false
				}, "api_base", at.v1(), "extra_headers", key())
			}
			return "Vibe's [[providers]] entry named magpie (config.toml) is gone, so it no longer reaches magpie"
		},
		Fields: []Field{{
			// the model Vibe starts on: one of magpie's is
			// magpie/<provider>/<model>, its alias
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				c, err := vibeRead(path)
				if err != nil {
					return err
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !ours(c) {
						stash(map[string]string{keyPick: c.ActiveModel})
					}
					if err := wire(); err != nil {
						return err
					}
					return setPick(v)
				}
				if slices.ContainsFunc(c.Models, vibeMine) || ours(c) {
					return unwire(v)
				}
				return setPick(v)
			},
			Options: func(cur map[string]string) []Option {
				return withVia(vibeOwn(path, keyPick, cur["model"]), "vibe", magpieID+"/")
			},
		}},
	}, path)
}

// vibeOwn is the model Vibe started on before magpie, or starts on now, for
// the picker: Vibe's own models are its sign-in's, which magpie can't list.
func vibeOwn(path, keyPick, cur string) []Option {
	var out []Option
	for _, m := range []string{stashShared()[keyPick], cur} {
		if m == "" || prefixed(m) || slices.ContainsFunc(out, func(o Option) bool { return o.Value == m }) {
			continue
		}
		out = append(out, Option{Value: m, Icon: modelIcon("", m), Note: "Vibe's own"})
	}
	return group("Vibe", out)
}
