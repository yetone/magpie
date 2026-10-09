package profile

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// Group is what a profile holds for one agent, to be read before it is
// applied (#467): its fields, in the order the agent lists them, and what
// the library's setup gives it.
type Group struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Fields []Item `json:"fields"`
	// Servers and Skills are the library's it gets, by name; Instructions
	// whether it gets the instructions (their text is left out)
	Servers      []string `json:"servers,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	Instructions bool     `json:"instructions,omitempty"`
	// Models is the model list it is switched to (#1368); nil when the
	// profile keeps none for it, which leaves its list as it is
	Models *Models `json:"models,omitempty"`
}

// Models is a model list as a profile's details tell it: how many models
// are taken out, or, Only, how many are the only ones picked.
type Models struct {
	Hidden int  `json:"hidden"`
	Only   bool `json:"only,omitempty"`
	Picked int  `json:"picked,omitempty"`
}

// Item is one saved field. Value "" is the agent's own default, or, with
// Follows, the key of the field it takes after (a Claude Code tier that
// follows the main model, #480). Hidden is a value that looked like a key or
// a token: it is left out, never shown.
type Item struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Follows string `json:"follows,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
}

// secretName is a field named for a credential; secretValue a value that
// reads as one (a known key prefix, or a long run of letters and digits no
// model or provider id has). No agent field holds one today, but
// profiles.json is a file anyone can edit.
var (
	secretName  = regexp.MustCompile(`(?i)(key|token|secret|passw|credential|cookie)`)
	secretValue = regexp.MustCompile(`^(sk-|sk_|ghp_|gho_|ghu_|github_pat_|xox[abpr]-|AIza|eyJ)|[A-Za-z0-9]{32,}`)
)

func secret(key, label, value string) bool {
	return value != "" && (secretName.MatchString(key) || secretName.MatchString(label) || secretValue.MatchString(value))
}

// Details is what p holds, by agent: the agents magpie knows in the order it
// lists them, then any it no longer does by id. A quiet field left empty is
// left out, as the agent's row leaves it out, unless it says which field it
// follows: Claude Code's four tiers are all there, one that follows the main
// model saying so (#480: opus and fable, following it, were missing, and
// read as not set). What applying the profile writes is unchanged.
func Details(p Profile) []Group {
	known := map[string]*agent.Agent{}
	var order []string
	for _, a := range agent.All() {
		known[a.ID] = a
		order = append(order, a.ID)
	}
	byID := map[string]map[string]string{}
	take := func(id string) {
		if byID[id] == nil {
			byID[id] = map[string]string{}
		}
	}
	for k, v := range p.Fields {
		i := strings.LastIndex(k, ".") // codex@wsl:Ubuntu-24.04.model
		if i < 0 {
			continue
		}
		take(k[:i])
		byID[k[:i]][k[i+1:]] = v
	}
	if l := p.Library; l != nil {
		for _, m := range []map[string][]string{l.MCP, l.Skills} {
			for _, ids := range m {
				for _, id := range ids {
					take(id)
				}
			}
		}
		for _, id := range l.Instructions.Agents {
			take(id)
		}
		for id := range l.Instructions.Extra {
			take(id)
		}
	}
	// a list nobody picked for is told only beside the agent's model: an
	// agent of no fields and no list of its own is no line of the details
	for id, ml := range p.Models {
		if !ml.Default() {
			take(id)
		}
	}
	var unknown []string
	for id := range byID {
		if known[id] == nil {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)

	var out []Group
	for _, id := range append(order, unknown...) {
		vals, ok := byID[id]
		if !ok {
			continue
		}
		g := Group{ID: id, Name: id, Fields: []Item{}}
		seen := map[string]bool{}
		add := func(key, label, v string) {
			it := Item{Key: key, Label: label, Value: v}
			if secret(key, label, v) {
				it.Value, it.Hidden = "", true
			}
			g.Fields = append(g.Fields, it)
		}
		if a := known[id]; a != nil {
			g.Name, g.Icon = a.Name, a.Icon
			for _, f := range a.Fields {
				v, ok := vals[f.Key]
				if !ok {
					continue
				}
				seen[f.Key] = true
				if f.Quiet && v == "" {
					if f.Follows != "" {
						g.Fields = append(g.Fields, Item{Key: f.Key, Label: f.Label, Follows: f.Follows})
					}
					continue
				}
				add(f.Key, f.Label, v)
			}
		}
		var rest []string
		for k := range vals {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range rest {
			add(k, k, vals[k])
		}
		if l := p.Library; l != nil {
			g.Servers = givenTo(l.MCP, id)
			g.Skills = givenTo(l.Skills, id)
			i := l.Instructions
			g.Instructions = i.Extra[id] != "" || i.Shared != "" && slices.Contains(i.Agents, id)
		}
		if ml, ok := p.Models[id]; ok && (!ml.Default() || len(g.Fields) > 0) {
			g.Models = &Models{Hidden: len(ml.Hidden), Only: ml.Only, Picked: len(ml.Picked)}
		}
		if len(g.Fields) == 0 && len(g.Servers) == 0 && len(g.Skills) == 0 && !g.Instructions && g.Models == nil {
			continue
		}
		out = append(out, g)
	}
	return out
}

// givenTo is the names in m given to the agent id, sorted.
func givenTo(m map[string][]string, id string) []string {
	var out []string
	for name, ids := range m {
		if slices.Contains(ids, id) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
