package agent

// jcode (1jehuang/jcode, a coding agent in Rust) keeps its settings in
// config.toml in $JCODE_HOME, else ~/.jcode. A provider of the user's own
// making is a named profile, [providers.<name>], which `jcode provider add`
// writes: type "openai-compatible" (jcode's chat completions client), a
// base_url, a key (api_key inline, or api_key_env) and its models as
// [[providers.<name>.models]] entries (id, context_window, input,
// reasoning). jcode starts on [provider] default_provider and default_model,
// a profile's name and one of its models.
//
// magpie adds itself as the profile "magpie": the gateway, with one model
// entry per catalog model, so the catalog is in jcode's /model picker, and
// jcode is started on the model picked. What [provider] named before is
// stashed and put back when magpie steps out. Every other key of the file is
// the user's and stays as written, the user's own keys in magpie's profile
// too (headers, extra_body, a model's reasoning_effort).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
)

// jcodeProfile is magpie's profile's table in jcode's config.toml, and
// jcodeModels its models' array.
const (
	jcodeProfile = "providers." + magpieID
	jcodeModels  = jcodeProfile + ".models"
)

// what jcode says after a change (notice.go)
var noticeJcode = newNotice("jcode reads its settings at start-up — restart open jcode sessions to use this.")

// jcodeContext is what jcode is told of a model whose context magpie
// doesn't know; without one it takes 200k.
const jcodeContext = 128000

// jcodeDir is jcode's folder: $JCODE_HOME, else ~/.jcode (jcode-storage's
// jcode_dir).
func jcodeDir(at place) string {
	if d := strings.TrimSpace(at.getenv("JCODE_HOME")); d != "" {
		return d
	}
	return at.join(".jcode")
}

// jcodeModel is one [[providers.<name>.models]] entry, as jcode's
// NamedProviderModelConfig reads it.
type jcodeModel struct {
	ID              string   `toml:"id"`
	Reasoning       *bool    `toml:"reasoning,omitempty"`
	ReasoningEffort string   `toml:"reasoning_effort,omitempty"`
	ContextWindow   int      `toml:"context_window,omitempty"`
	Input           []string `toml:"input,omitempty"`
}

// jcodeConf is what magpie reads of jcode's config.toml.
type jcodeConf struct {
	Provider struct {
		DefaultProvider string `toml:"default_provider"`
		DefaultModel    string `toml:"default_model"`
	} `toml:"provider"`
	Providers map[string]struct {
		BaseURL string       `toml:"base_url"`
		APIKey  string       `toml:"api_key"`
		Models  []jcodeModel `toml:"models"`
	} `toml:"providers"`
}

// jcodeRead reads jcode's config.toml; a missing file is an empty one, and
// one that doesn't parse is an error, never taken as empty.
func jcodeRead(path string) (jcodeConf, error) {
	var c jcodeConf
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("jcode's %s isn't TOML magpie can read; it was left as it is: %w", path, err)
	}
	return c, nil
}

// jcodeWant is magpie's models as jcode is told them: the context window
// (one magpie doesn't know is jcodeContext), whether it takes images, and
// reasoning on for a model with effort levels, so jcode's /effort offers
// them. A reasoning_effort the user set on one of magpie's models stays
// while the model has that level.
func jcodeWant(was []jcodeModel) []jcodeModel {
	kept := map[string]string{}
	for _, m := range was {
		kept[m.ID] = m.ReasoningEffort
	}
	var out []jcodeModel
	for _, m := range sharedModels("jcode") {
		ctx := m.Context
		if ctx <= 0 {
			ctx = unknownWindow(jcodeContext)
		}
		e := jcodeModel{ID: m.ID, ContextWindow: ctx, Input: []string{"text"}}
		if m.Images {
			e.Input = append(e.Input, "image")
		}
		var levels []string
		for _, l := range m.Efforts {
			if l != "none" {
				levels = append(levels, l)
			}
		}
		if len(levels) > 0 {
			yes := true
			e.Reasoning = &yes
			e.ReasoningEffort = keptEffort(kept[m.ID], levels, "")
		}
		out = append(out, e)
	}
	return out
}

// jcodeArray is models as one TOML array of inline tables, one per line.
func jcodeArray(models []jcodeModel) edit.Raw {
	if len(models) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteString("[\n")
	for _, m := range models {
		parts := []string{"id = " + strconv.Quote(m.ID)}
		if m.ContextWindow > 0 {
			parts = append(parts, "context_window = "+strconv.Itoa(m.ContextWindow))
		}
		if len(m.Input) > 0 {
			var q []string
			for _, in := range m.Input {
				q = append(q, strconv.Quote(in))
			}
			parts = append(parts, "input = ["+strings.Join(q, ", ")+"]")
		}
		if m.Reasoning != nil {
			parts = append(parts, "reasoning = "+strconv.FormatBool(*m.Reasoning))
		}
		if m.ReasoningEffort != "" {
			parts = append(parts, "reasoning_effort = "+strconv.Quote(m.ReasoningEffort))
		}
		b.WriteString("  { " + strings.Join(parts, ", ") + " },\n")
	}
	b.WriteString("]")
	return edit.Raw(b.String())
}

// jcodeSame reports whether two model lists say the same to jcode.
func jcodeSame(a, b []jcodeModel) bool {
	return slices.EqualFunc(a, b, func(x, y jcodeModel) bool {
		return x.ID == y.ID && x.ContextWindow == y.ContextWindow && slices.Equal(x.Input, y.Input) &&
			(x.Reasoning == nil) == (y.Reasoning == nil) && (x.Reasoning == nil || *x.Reasoning == *y.Reasoning) &&
			x.ReasoningEffort == y.ReasoningEffort
	})
}

// jcodePick is what [provider] named before magpie: a profile (or one of
// jcode's providers) and a model, "" each for none.
type jcodePick struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func jcode(home string) *Agent { return jcodeAt(here(home)) }

func jcodeAt(at place) *Agent {
	dir := jcodeDir(at)
	path := filepath.Join(dir, "config.toml")
	keyPick := at.key("jcode:" + path + ":default")
	key := func() string { return agentKeyAt("jcode", at.gw()) }
	read := func() jcodeConf { c, _ := jcodeRead(path); return c }
	ours := func(c jcodeConf) bool { return c.Provider.DefaultProvider == magpieID }
	get := func() string {
		c := read()
		if ours(c) && c.Provider.DefaultModel != "" {
			return magpieID + "/" + c.Provider.DefaultModel
		}
		return c.Provider.DefaultModel
	}
	// setPick names the profile and model jcode starts on, a key taken out
	// for ""
	setPick := func(p jcodePick) error {
		for _, kv := range [][2]string{{"default_provider", p.Provider}, {"default_model", p.Model}} {
			var err error
			if kv[1] == "" {
				err = edit.DelTOMLKey(path, "provider", kv[0])
			} else {
				err = edit.SetTOMLKey(path, "provider", kv[0], kv[1])
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	// wire writes magpie's profile, its own keys set and the user's others
	// left as they are, with model, if given, the profile's default; it
	// writes nothing when the file has it all already
	wire := func(model string) error {
		c, err := jcodeRead(path)
		if err != nil {
			return err
		}
		have, on := c.Providers[magpieID]
		want := jcodeWant(have.Models)
		t, err := edit.GetTOMLTable(path, jcodeProfile)
		if err != nil {
			return err
		}
		kvs := [][2]any{
			{"type", "openai-compatible"},
			{"base_url", at.v1()},
			{"api_key", key()},
			// a gateway on loopback is taken by jcode as needing no key,
			// and then it sends none: the key names jcode to the gateway
			{"requires_api_key", edit.Raw("true")},
		}
		if model != "" {
			kvs = append(kvs, [2]any{"default_model", model})
		}
		same := on && t != nil && jcodeSame(have.Models, want)
		for _, kv := range kvs {
			v := fmt.Sprint(kv[1])
			if t[kv[0].(string)] != v {
				same = false
			}
		}
		if same {
			return nil
		}
		// jcode writes the models as [[providers.magpie.models]] when it
		// saves its settings itself: those go, for the one array below
		if err := edit.SetTOMLTablesMatching(path, []string{jcodeModels}, nil, nil); err != nil {
			return err
		}
		for _, kv := range kvs {
			if err := edit.SetTOMLKey(path, jcodeProfile, kv[0].(string), kv[1]); err != nil {
				return err
			}
		}
		return edit.SetTOMLKey(path, jcodeProfile, "models", jcodeArray(want))
	}
	// unwire takes magpie's profile out, and jcode back to what it started
	// on before — when it still starts on magpie: a pick the user made in
	// jcode since is theirs and stands
	unwire := func(own string) error {
		c, err := jcodeRead(path)
		if err != nil {
			return err
		}
		if ours(c) {
			var was jcodePick
			json.Unmarshal([]byte(unstash(keyPick)), &was)
			if own != "" && own != was.Model {
				was.Model = own
			}
			if err := setPick(was); err != nil {
				return err
			}
		} else {
			forget(keyPick)
			if own != "" {
				if err := edit.SetTOMLKey(path, "provider", "default_model", own); err != nil {
					return err
				}
			}
		}
		// the profile whole: its models' tables and the user's own in it
		// (headers, extra_body) are magpie's profile's, and go with it
		return edit.SetTOMLTablesMatching(path, []string{jcodeProfile}, []string{jcodeProfile + "."}, nil)
	}
	return atomic(&Agent{
		ID: "jcode", Name: "jcode", Icon: "jcode", Spelled: prefixed, NoNames: true,
		Bin: "jcode", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)jcode( |$)`) {
				return noticeJcode.String()
			}
			return ""
		},
		ListOnly: true,
		Sync: func() error {
			c, err := jcodeRead(path)
			if err != nil {
				return err
			}
			if _, on := c.Providers[magpieID]; !on {
				return nil
			}
			return wire("")
		},
		Check: func() string {
			c, err := jcodeRead(path)
			if err != nil {
				return err.Error()
			}
			if !ours(c) {
				return ""
			}
			p, on := c.Providers[magpieID]
			if !on {
				return "jcode's [" + jcodeProfile + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			return wiringOff("jcode", path, func(k string) (string, bool) {
				switch k {
				case "base_url":
					return p.BaseURL, p.BaseURL != ""
				case "api_key":
					return p.APIKey, p.APIKey != ""
				}
				return "", false
			}, "base_url", at.v1(), "api_key", key())
		},
		Fields: []Field{{
			// the model jcode starts on: one of magpie's is
			// magpie/<provider>/<model>, magpie's profile with the model's
			// id in it
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				c, err := jcodeRead(path)
				if err != nil {
					return err
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !ours(c) {
						b, _ := json.Marshal(jcodePick{c.Provider.DefaultProvider, c.Provider.DefaultModel})
						stash(map[string]string{keyPick: string(b)})
					}
					if err := wire(ref); err != nil {
						return err
					}
					return setPick(jcodePick{magpieID, ref})
				}
				if _, on := c.Providers[magpieID]; on || ours(c) {
					return unwire(v)
				}
				return setPick(jcodePick{c.Provider.DefaultProvider, v})
			},
			Options: func(cur map[string]string) []Option {
				return withVia(jcodeOwn(path, keyPick, cur["model"]), "jcode", magpieID+"/")
			},
		}},
	}, path)
}

// jcodeOwn is the model jcode started on before magpie, or starts on now,
// for the picker: jcode's own providers are its sign-ins, which magpie
// can't list.
func jcodeOwn(path, keyPick, cur string) []Option {
	var was jcodePick
	json.Unmarshal([]byte(stashShared()[keyPick]), &was)
	var out []Option
	for _, m := range []string{was.Model, cur} {
		if m == "" || prefixed(m) || slices.ContainsFunc(out, func(o Option) bool { return o.Value == m }) {
			continue
		}
		out = append(out, Option{Value: m, Icon: modelIcon("", m), Note: "jcode's own"})
	}
	return group("jcode", out)
}
