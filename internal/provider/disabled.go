package provider

import (
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Disabled models (#1867): models the user turned off for every request
// through magpie. Settings' DisabledModels holds them as patterns, an exact
// id or a glob ("*fable*", "anthropic/claude-fable-5*"), each matched,
// ignoring case, against a model's "<provider>/<model>", its bare id and
// the bare id's last segment. A disabled model is left out of the catalog
// (so every list, agent file and routing group goes without it), and the
// gateway turns away a request for it on every route, before any upstream
// call: a request that names it, a group member, a fallback, a sub-agent's
// model, count_tokens, a decision model.

// lastDisabled is the list as last read whole. settings.json that can't be
// read, or doesn't parse with no last good generation, holds a list nobody
// knows: it is never taken for "none disabled". The one read last stays in
// force; with none read since magpie started, Disabled says it is unknown.
var lastDisabled struct {
	sync.Mutex
	list   []string
	read   bool
	warned bool
}

// disabledList is the patterns in force, and unknown when settings.json
// couldn't be read and none was read before.
func disabledList() (list []string, unknown bool) {
	// read once while a request holds the catalog: each group's members
	// (membersIn) asked for it, and parsed settings.json each time
	r := heldOf("disabled", func() disabledRead {
		list, unknown := readDisabled()
		return disabledRead{slices.Clip(list), unknown}
	})
	return r.list, r.unknown
}

type disabledRead struct {
	list    []string
	unknown bool
}

// readDisabled is disabledList read from settings.json now.
func readDisabled() (list []string, unknown bool) {
	s, err := settings.LoadChecked()
	lastDisabled.Lock()
	defer lastDisabled.Unlock()
	if err == nil {
		lastDisabled.list, lastDisabled.read, lastDisabled.warned = s.DisabledModels, true, false
		return s.DisabledModels, false
	}
	if !lastDisabled.warned {
		lastDisabled.warned = true
		log.Printf("disabled models: %v; the list read last stays in force", err)
	}
	return lastDisabled.list, !lastDisabled.read
}

// DisabledModels are the patterns the user disabled, as saved.
func DisabledModels() []string {
	list, _ := disabledList()
	return slices.Clone(list)
}

// DisabledUnknown reports that settings.json can't be read and no list was
// read before it: the gateway then refuses every model, since any of them
// may be one the user disabled.
func DisabledUnknown() bool {
	_, unknown := disabledList()
	return unknown
}

// matchDisabled is the pattern of list that disables a model, if any.
// names are what the model answers to.
func matchDisabled(list []string, names ...string) (string, bool) {
	for _, pat := range list {
		lp := strings.ToLower(strings.TrimSpace(pat))
		if lp == "" {
			continue
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			if globMatch(lp, strings.ToLower(n)) {
				return pat, true
			}
		}
	}
	return "", false
}

// globMatch is a simple glob: * is any run of characters, "/" among them
// (a model id has slashes of its own, openrouter's anthropic/claude-fable-5,
// and "*fable*" must cross them), ? is any one, and everything else is
// itself.
func globMatch(pat, s string) bool {
	if !strings.ContainsAny(pat, "*?") {
		return pat == s
	}
	star, mark := -1, 0
	pi, si := 0, 0
	for si < len(s) {
		switch {
		case pi < len(pat) && pat[pi] == '*':
			star, mark = pi, si
			pi++
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == s[si]):
			pi++
			si++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

// namesOf are what a provider's model answers to: "<provider>/<model>",
// its bare id, and the bare id's last segment (claude-fable-5 for
// anthropic/claude-fable-5 at OpenRouter).
func namesOf(pid, model string) []string {
	model = strings.TrimSpace(model)
	out := []string{model}
	if pid != "" {
		out = append(out, pid+"/"+model)
	}
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		out = append(out, model[i+1:])
	}
	return out
}

// ModelDisabled reports whether the user disabled pid's model, and the
// pattern that did. A list nobody knows (DisabledUnknown) disables none
// here: the gateway asks DisabledUnknown itself.
func ModelDisabled(pid, model string) (string, bool) {
	list, _ := disabledList()
	if len(list) == 0 {
		return "", false
	}
	return matchDisabled(list, namesOf(pid, model)...)
}

// disabledNow is ModelDisabled with the list read once, for a loop over
// many models.
func disabledNow() func(pid, model string) bool {
	list, _ := disabledList()
	if len(list) == 0 {
		return func(string, string) bool { return false }
	}
	return func(pid, model string) bool {
		_, off := matchDisabled(list, namesOf(pid, model)...)
		return off
	}
}

// DisabledPattern is ModelDisabled's pattern with the list read once, for
// a loop over many models: "" for a model that isn't disabled.
func DisabledPattern() func(pid, model string) string {
	list, _ := disabledList()
	return func(pid, model string) string {
		pat, _ := matchDisabled(list, namesOf(pid, model)...)
		return pat
	}
}

// IDDisabled reports whether a model id as asked ("<provider>/<model>" or a
// bare id) is one the user disabled, and the pattern that did.
func IDDisabled(id string) (string, bool) {
	list, _ := disabledList()
	if len(list) == 0 {
		return "", false
	}
	id = strings.TrimSpace(id)
	names := []string{id}
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		names = append(names, id[i+1:])
	}
	if pid, m, ok := strings.Cut(id, "/"); ok && pid != "" && m != "" {
		names = append(names, m)
	}
	return matchDisabled(list, names...)
}

// normalDisabled trims, drops empties and repeats (ignoring case), and
// keeps the order given.
func normalDisabled(list []string) []string {
	var out []string
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, p) }) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// SetDisabledModels saves the disabled list whole.
func SetDisabledModels(list []string) error {
	s, err := settings.LoadChecked()
	if err != nil {
		return err // never write a list over one nobody can read
	}
	keep := normalDisabled(list)
	if slices.Equal(s.DisabledModels, keep) {
		return nil
	}
	s.DisabledModels = keep
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed()
	catalog.Touched()
	return nil
}

// DisableModels adds patterns to the disabled list.
func DisableModels(pats ...string) error {
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	return SetDisabledModels(append(slices.Clone(s.DisabledModels), pats...))
}

// EnableModels takes patterns off the disabled list (ignoring case); it
// says which of them weren't on it.
func EnableModels(pats ...string) (missing []string, err error) {
	s, err := settings.LoadChecked()
	if err != nil {
		return nil, err
	}
	keep := slices.Clone(s.DisabledModels)
	for _, p := range pats {
		p = strings.TrimSpace(p)
		n := len(keep)
		keep = slices.DeleteFunc(keep, func(o string) bool { return strings.EqualFold(o, p) })
		if len(keep) == n && p != "" {
			missing = append(missing, p)
		}
	}
	return missing, SetDisabledModels(keep)
}

// GroupDisabledOut is the group id names when it has no model left only
// because the user disabled every one it would have (#1867), and those
// models: the gateway says so, not that the group is empty.
func GroupDisabledOut(id string) (Group, []string, bool) {
	g, ms, ok := FindGroup(id)
	if !ok || len(ms) > 0 {
		return Group{}, nil, false
	}
	none := func(string, string) bool { return false }
	entries := entriesOff(none)
	all := groupsIn(entries)
	whole, ok := groupOf(all, g.ID)
	if !ok {
		return Group{}, nil, false
	}
	var off []string
	for _, m := range membersOff(entries, all, whole, none) {
		off = append(off, m.Provider.ID+"/"+m.Model)
	}
	if len(off) == 0 {
		return Group{}, nil, false
	}
	return g, off, true
}

// DisabledBy reports whether the one pattern pat disables pid's model.
func DisabledBy(pat, pid, model string) bool {
	_, off := matchDisabled([]string{pat}, namesOf(pid, model)...)
	return off
}

// DisabledHits are the providers' models, on and listed, that pat turns
// off now, as "<provider>/<model>".
func DisabledHits(pat string) []string {
	var out []string
	for _, p := range All() {
		if !p.On() {
			continue
		}
		for _, m := range p.Exposed() {
			if DisabledBy(pat, p.ID, m.ID) {
				out = append(out, p.ID+"/"+m.ID)
			}
		}
	}
	return out
}

// BareDisabled reports whether a bare id (no provider in it) is the id of
// a model of a provider that is on and the user disabled, and the pattern
// that did: the catalog leaves it out, so Resolve doesn't find it.
func BareDisabled(id string) (string, bool) {
	list, _ := disabledList()
	id = strings.TrimSpace(id)
	if len(list) == 0 || id == "" {
		return "", false
	}
	for _, p := range All() {
		if !p.On() {
			continue
		}
		for _, m := range p.Exposed() {
			if !strings.EqualFold(m.ID, id) {
				continue
			}
			if pat, off := matchDisabled(list, namesOf(p.ID, m.ID)...); off {
				return pat, true
			}
		}
	}
	return "", false
}
