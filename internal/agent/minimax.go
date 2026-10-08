package agent

// MiniMax Code — MiniMax's mcode CLI and the desktop app, which run the same
// runtime (#362) — keeps its settings in config.yaml in its data folder,
// $MINIMAX_DATA_DIR or ~/.minimax. The model sessions start on is
// defaultModel, "<provider>/<model>" split at the first slash, where a
// provider the user added (Add 3rd-party provider…, mcode provider add) is
// "custom_provider:<key>", an entry of custom_provider. magpie adds itself
// there as custom_provider.magpie — the gateway, spoken to as Anthropic
// messages, with the catalog as its models — so the catalog joins MiniMax
// Code's /model, and a model through magpie is
// "custom_provider:magpie/<provider>/<model>". The default the user had is
// stashed and put back when magpie steps out. MiniMax Code rewrites the file
// itself (its /model, its provider commands), so magpie's entry is edited in
// place: a key the user or MiniMax Code put on it, or on one of its models,
// stays.

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"gopkg.in/yaml.v3"
)

// mcodeProvider is magpie's provider id in MiniMax Code, and mcodeEntry its
// entry in config.yaml.
const (
	mcodeProvider = "custom_provider:" + magpieID
	mcodeEntry    = "custom_provider." + magpieID
)

// mcodeUA is the User-Agent magpie's entry sends: MiniMax Code's own is the
// Anthropic SDK's ("L/JS 0.91.1"), or node's for its token counts.
const mcodeUA = "minimax-code"

// MiniMaxDir is the folder MiniMax Code keeps its config.yaml in.
func MiniMaxDir(home string) string {
	if d := strings.TrimSpace(appdir.Getenv("MINIMAX_DATA_DIR")); d != "" {
		return d
	}
	return filepath.Join(home, ".minimax")
}

func miniMax(home string) *Agent { return miniMaxAt(here(home), MiniMaxDir(home)) }

// miniMaxIn is MiniMax Code's mcode in a WSL distro (see wsl.go): ~/.minimax,
// as MINIMAX_DATA_DIR there isn't read.
func miniMaxIn(at place) *Agent { return miniMaxAt(at, filepath.Join(at.home, ".minimax")) }

// miniMaxAt is MiniMax Code with its data folder at dir, reaching the
// gateway as at does.
func miniMaxAt(at place, dir string) *Agent {
	writeMiniMaxEntry := func(path string) error { return writeMiniMaxEntryAt(path, at.gw()) }
	path := filepath.Join(dir, "config.yaml")
	key := "minimax-code:" + path + ":"
	get := func(k string) string { v, _ := edit.GetYAML(path, k); return v }
	onMagpie := func() bool { return strings.HasPrefix(get("defaultModel"), mcodeProvider+"/") }
	// setDefault sets defaultModel and its variant, taking out each that is ""
	setDefault := func(model, variant string) error {
		var del []string
		var kvs []edit.KV
		for _, kv := range []edit.KV{{Path: "defaultModel", Value: model}, {Path: "defaultModelVariant", Value: variant}} {
			if kv.Value == "" {
				del = append(del, kv.Path)
			} else {
				kvs = append(kvs, kv)
			}
		}
		if err := edit.DelYAML(path, del...); err != nil || len(kvs) == 0 {
			return err
		}
		return edit.SetYAML(path, kvs...)
	}
	return atomic(&Agent{
		ID: "minimax-code", Name: "MiniMax Code", Icon: "minimax-color", Aliases: []string{"mcode"}, Spelled: prefixed,
		UA:  []string{mcodeUA},
		Bin: "mcode", Dir: dir, Path: path,
		Sync: func() error {
			if _, ok := edit.GetYAMLText(path, mcodeEntry); !ok {
				return nil
			}
			return writeMiniMaxEntry(path)
		},
		Notice: func() string {
			if Running(`(^|/)mcode( |$)`) {
				return "MiniMax Code reads its settings at start-up — restart open mcode sessions to use this."
			}
			return ""
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			if _, ok := edit.GetYAMLText(path, mcodeEntry); !ok {
				return "MiniMax Code's " + mcodeEntry + " (config.yaml) is gone, so it no longer reaches magpie"
			}
			if get(mcodeEntry+".enabled") == "false" {
				return "MiniMax Code's " + mcodeEntry + " (config.yaml) is turned off, so it no longer reaches magpie"
			}
			return wiringOff("MiniMax Code", path, func(k string) (string, bool) { return edit.GetYAML(path, mcodeEntry+".options."+k) },
				"baseURL", at.gw(), "apiKey", at.gwKey())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				v := get("defaultModel")
				if ref, ok := strings.CutPrefix(v, mcodeProvider+"/"); ok {
					return magpieID + "/" + ref
				}
				return v
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onMagpie() {
						stash(map[string]string{
							key + "defaultModel":        get("defaultModel"),
							key + "defaultModelVariant": get("defaultModelVariant"),
						})
					}
					if err := writeMiniMaxEntry(path); err != nil {
						return err
					}
					// a variant is the last model's ("thinking"), none of magpie's
					return setDefault(mcodeProvider+"/"+ref, "")
				}
				// the variant goes with the model it was picked for
				variant := ""
				if onMagpie() {
					was, wasVariant := unstash(key+"defaultModel"), unstash(key+"defaultModelVariant")
					if v == "" {
						v = was // back to the default the user had
					}
					if v == was {
						variant = wasVariant
					}
				} else if v == get("defaultModel") {
					variant = get("defaultModelVariant")
				}
				if err := setDefault(v, variant); err != nil {
					return err
				}
				if err := edit.DelYAML(path, mcodeEntry); err != nil {
					return err
				}
				// nor an empty custom_provider left where magpie's was the one
				if t, ok := edit.GetYAMLText(path, "custom_provider"); ok && strings.TrimSpace(t) == "{}" {
					return edit.DelYAML(path, "custom_provider")
				}
				return nil
			},
			Options: func(cur map[string]string) []Option {
				return append(miniMaxOwnOptions(path, cur["model"]), viaMagpie("minimax-code", magpieID+"/")...)
			},
		}},
	}, path)
}

// writeMiniMaxEntryAt puts magpie's entry in MiniMax Code's config.yaml,
// for a MiniMax Code reaching the gateway at gw, as the catalog is now, over
// the one there: what magpie sets is set, a model gone from the catalog
// goes, and every other key — the user's, MiniMax Code's — stays with its
// comments. The file is written only when this changes it.
func writeMiniMaxEntryAt(path, gw string) error {
	cur, _ := edit.GetYAMLText(path, mcodeEntry)
	entry := &yaml.Node{Kind: yaml.MappingNode}
	if cur != "" {
		var doc yaml.Node
		if yaml.Unmarshal([]byte(cur), &doc) == nil && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
			entry = doc.Content[0]
		}
	}
	yamlSet(entry, "name", magpieID)
	yamlSet(entry, "kind", "custom")
	yamlSet(entry, "enabled", true)
	// the gateway takes every model as Anthropic messages, as Claude Code
	// sends them; MiniMax Code's own API is that too
	yamlSet(entry, "api", "anthropic-messages")
	opts := yamlMap(entry, "options")
	yamlSet(opts, "apiKey", keyAt(gw))
	yamlSet(opts, "baseURL", gw)
	yamlSet(opts, "authMode", "api-key")
	yamlSet(yamlMap(opts, "headers"), "User-Agent", mcodeUA)

	old := yamlGet(entry, "models")
	models := &yaml.Node{Kind: yaml.MappingNode}
	for _, m := range magpieModels("minimax-code") {
		mn := yamlGet(old, m.ID)
		if mn == nil || mn.Kind != yaml.MappingNode {
			mn = &yaml.Node{Kind: yaml.MappingNode}
		}
		yamlSet(mn, "name", m.Name)
		yamlDel(mn, "limit")
		if m.Context > 0 || m.Output > 0 {
			limit := yamlMap(mn, "limit")
			if m.Context > 0 {
				yamlSet(limit, "context", m.Context)
			}
			if out := maxTokens(m); out > 0 {
				yamlSet(limit, "output", out)
			}
		}
		// a thinking model's levels, for its effort picker: none is left
		// out, MiniMax Code would ask for it as a level; high where the
		// model has it, as MiniMax Code starts its own models on
		var levels []string
		for _, e := range m.Efforts {
			if e != "none" {
				levels = append(levels, e)
			}
		}
		yamlSet(mn, "reasoning", len(levels) > 0)
		// a default effort set by hand stays while the model has it
		var was string
		if d := yamlGet(yamlGet(mn, "thinking"), "defaultEffort"); d != nil && d.Kind == yaml.ScalarNode {
			was = d.Value
		}
		yamlDel(mn, "thinking")
		if len(levels) > 0 {
			th := yamlMap(mn, "thinking")
			yamlSet(th, "effortOptions", levels)
			def := ""
			if slices.Contains(levels, "high") {
				def = "high"
			}
			if def = keptEffort(was, levels, def); def != "" {
				yamlSet(th, "defaultEffort", def)
			}
		}
		if caps := yamlGet(mn, "capabilities"); m.Images {
			yamlSet(yamlMap(mn, "capabilities"), "support_image", true)
		} else if caps != nil {
			yamlDel(caps, "support_image")
			if len(caps.Content) == 0 {
				yamlDel(mn, "capabilities")
			}
		}
		models.Content = append(models.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: m.ID}, mn)
	}
	if old != nil {
		models.HeadComment, models.LineComment, models.FootComment = old.HeadComment, old.LineComment, old.FootComment
	}
	yamlSet(entry, "models", models)

	b, err := yaml.Marshal(entry)
	if err != nil {
		return err
	}
	text := strings.TrimSuffix(string(b), "\n")
	if text == cur {
		return nil
	}
	return edit.SetYAML(path, edit.KV{Path: mcodeEntry, Value: edit.YAMLText(text)})
}

// yamlGet is the value of k in the mapping n, nil if there is none.
func yamlGet(n *yaml.Node, k string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			return n.Content[i+1]
		}
	}
	return nil
}

// yamlSet sets k in the mapping n to v (a *yaml.Node, or a value to encode),
// in place where k is there, its comments kept.
func yamlSet(n *yaml.Node, k string, v any) {
	vn, ok := v.(*yaml.Node)
	if !ok {
		vn = &yaml.Node{}
		vn.Encode(v)
		if vn.Kind == yaml.SequenceNode {
			vn.Style = yaml.FlowStyle
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			cur := n.Content[i+1]
			vn.HeadComment, vn.LineComment, vn.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
			n.Content[i+1] = vn
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, vn)
}

// yamlMap is the mapping at k in n, made where k is missing or not one.
func yamlMap(n *yaml.Node, k string) *yaml.Node {
	if m := yamlGet(n, k); m != nil && m.Kind == yaml.MappingNode {
		return m
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	yamlSet(n, k, m)
	return m
}

// yamlDel takes k out of the mapping n.
func yamlDel(n *yaml.Node, k string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

// miniMaxOwnOptions are the models of the user's own in MiniMax Code's
// config: those under provider (its MiniMax sign-in's, a models.dev
// provider's) and custom_provider (one added by hand), and the current one.
// Model ids hold dots ("MiniMax-M2.7"), so the file is read whole.
func miniMaxOwnOptions(path, cur string) []Option {
	var c struct {
		Provider       map[string]mcodeProviderModels `yaml:"provider"`
		CustomProvider map[string]mcodeProviderModels `yaml:"custom_provider"`
	}
	if raw, err := edit.Read(path); err == nil && raw != nil {
		yaml.Unmarshal(raw, &c)
	}
	seen := map[string]bool{}
	var out []Option
	add := func(prefix string, ps map[string]mcodeProviderModels) {
		ids := make([]string, 0, len(ps))
		for id := range ps {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			p := ps[id]
			if p.Enabled != nil && !*p.Enabled || prefix+id == mcodeProvider {
				continue
			}
			ms := make([]string, 0, len(p.Models))
			for m, e := range p.Models {
				if e.Enabled == nil || *e.Enabled {
					ms = append(ms, m)
				}
			}
			slices.Sort(ms)
			for _, m := range ms {
				v := prefix + id + "/" + m
				if !seen[v] {
					seen[v] = true
					out = append(out, Option{Value: v, Icon: miniMaxIcon(id, m)})
				}
			}
		}
	}
	add("", c.Provider)
	add("custom_provider:", c.CustomProvider)
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		p, m, _ := strings.Cut(cur, "/")
		out = append([]Option{{Value: cur, Icon: miniMaxIcon(p, m)}}, out...)
	}
	return group("MiniMax Code", out)
}

type mcodeProviderModels struct {
	Enabled *bool `yaml:"enabled"`
	Models  map[string]struct {
		Enabled *bool `yaml:"enabled"`
	} `yaml:"models"`
}

// miniMaxIcon is a model's logo: its vendor's by its name, else MiniMax's
// for one of MiniMax Code's own providers.
func miniMaxIcon(provider, model string) string {
	if ic := modelIcon("", model); ic != "" {
		return ic
	}
	if strings.HasPrefix(provider, "minimax") {
		return "minimax-color"
	}
	return ""
}
