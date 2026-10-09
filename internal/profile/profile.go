// Package profile stores named snapshots of every agent's settings, and of
// who gets what from the library, so a whole setup can be switched in one
// move.
package profile

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// Profile is every agent's fields, and the library's setup.
type Profile struct {
	// Fields maps "agent.field" to a value.
	Fields map[string]string
	// Library is which agents got which servers and skills, and the
	// instructions; nil for a profile saved while the library was empty,
	// or before profiles kept it, which leaves the library as it is.
	Library *library.Setup
	// Models are the agents' model lists (#1368), by the id their lists are
	// kept under (agent.ListsFor): which of magpie's models each shows and
	// in what order. An agent not named, every one in a profile saved
	// before profiles kept them, keeps the list it has.
	Models map[string]provider.ModelList
}

// libraryKey holds the library's setup among a profile's "agent.field"
// keys: it has no dot, so it is no agent's field, and a profile saved
// before it is read as it always was.
const libraryKey = "library"

// modelsKey holds the agents' model lists, as libraryKey the library's.
const modelsKey = "models"

// MarshalJSON writes the fields and the library's setup side by side.
func (p Profile) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(p.Fields)+1)
	for k, v := range p.Fields {
		m[k] = v
	}
	if p.Library != nil {
		m[libraryKey] = p.Library
	}
	if p.Models != nil {
		m[modelsKey] = p.Models
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads what MarshalJSON writes, and a profile of before,
// which was the fields alone.
func (p *Profile) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*p = Profile{Fields: map[string]string{}}
	for k, raw := range m {
		if k == libraryKey {
			if string(raw) == "null" {
				continue
			}
			p.Library = &library.Setup{}
			if err := json.Unmarshal(raw, p.Library); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			continue
		}
		if k == modelsKey {
			if string(raw) == "null" {
				continue
			}
			if err := json.Unmarshal(raw, &p.Models); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		p.Fields[k] = v
	}
	return nil
}

// Path is the profiles file.
func Path() string { return filepath.Join(appdir.Config(), "profiles.json") }

// Load reads every profile.
func Load() (map[string]Profile, error) {
	b, err := edit.Read(Path())
	if err != nil {
		return nil, err
	}
	out := map[string]Profile{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	return out, nil
}

// Names lists profiles alphabetically.
func Names(ps map[string]Profile) []string {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func store(ps map[string]Profile) error {
	b, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(Path(), append(b, '\n'))
}

// Fields captures the current value of every detected agent's fields,
// including empty values that restore the agent's own defaults.
func Fields() map[string]string {
	p := map[string]string{}
	for _, a := range agent.Detected() {
		for k, v := range a.Values() {
			p[a.ID+"."+k] = v
		}
	}
	return p
}

// ModelLists captures every detected agent's model list, one the user never
// picked for included: applied, it shows that agent every model again.
func ModelLists() map[string]provider.ModelList {
	out := map[string]provider.ModelList{}
	for _, a := range agent.Detected() {
		id := strings.ToLower(a.ListsFor())
		out[id] = provider.ModelListOf(id)
	}
	return out
}

// Snapshot captures every detected agent's fields and model list, and the
// library's setup unless the library is empty.
func Snapshot() (Profile, error) {
	p := Profile{Fields: Fields(), Models: ModelLists()}
	s, err := library.Snapshot()
	if err != nil {
		return p, err
	}
	if !s.Empty() {
		p.Library = s
	}
	return p, nil
}

// Save stores p under name, replacing any existing profile.
func Save(name string, p Profile) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("profile name is empty")
	}
	ps, err := Load()
	if err != nil {
		return err
	}
	ps[name] = p
	return store(ps)
}

// Delete removes a profile.
func Delete(name string) error {
	ps, err := Load()
	if err != nil {
		return err
	}
	if _, ok := ps[name]; !ok {
		return fmt.Errorf("no profile named %q", name)
	}
	delete(ps, name)
	return store(ps)
}

// Applied is what applying a profile did.
type Applied struct {
	Changed int // agent fields changed, and model lists
	// Library is what bringing the library's setup back wrote into the
	// agents; nil for a profile that carries none.
	Library *library.Result
}

// Apply puts back the agents' model lists p carries, writes every field in
// p that differs from what is set now, then brings the library's setup back
// when p carries one and writes it into the agents (their files kept aside
// first). It stops at the first error. The lists go first, so the files
// that list an agent's models are written for the list it is switched to,
// and its model is set last, over them.
func Apply(p Profile) (Applied, error) {
	var out Applied
	if len(p.Models) > 0 {
		lists, err := provider.SetModelLists(p.Models)
		out.Changed = len(lists)
		if err != nil {
			return out, err
		}
	}
	n, err := ApplyFields(p.Fields)
	out.Changed += n
	if err != nil || p.Library == nil {
		return out, err
	}
	out.Library, err = library.Restore(p.Library)
	return out, err
}

// ApplyFields writes every value in p that differs from what is set now.
// It returns the number of changes made and the first error encountered.
func ApplyFields(p map[string]string) (int, error) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	// providers first: switching one re-settles the model behind it; then
	// models, which settle what the other fields (Claude Code's tiers) hang on
	rank := func(k string) int {
		switch {
		case strings.HasSuffix(k, ".provider"):
			return 0
		case strings.HasSuffix(k, ".model"):
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	changed := 0
	for _, k := range keys {
		// the last dot: a WSL distro's name may have one (codex@wsl:Ubuntu-24.04)
		i := strings.LastIndex(k, ".")
		if i < 0 {
			continue
		}
		id, field := k[:i], k[i+1:]
		a := agents[id]
		if a == nil {
			continue
		}
		f := a.Field(field)
		if f == nil {
			continue
		}
		v := p[k]
		// a model of a provider renamed since the profile was saved is
		// the same model by its new id
		if r := provider.RenamedRef(v); r != v {
			if s, err := a.Spell(f.Key, r); err == nil {
				v = s
			}
		}
		if f.Get() == v {
			continue
		}
		if err := a.Apply(f.Key, v); err != nil {
			return changed, fmt.Errorf("%s: %w", k, err)
		}
		changed++
	}
	return changed, nil
}

// Summary renders a profile's non-default models as a short one-line description.
func Summary(p Profile) string {
	keys := make([]string, 0, len(p.Fields))
	for k := range p.Fields {
		if strings.HasSuffix(k, ".model") && p.Fields[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strings.TrimSuffix(k, ".model")+" "+p.Fields[k])
	}
	return strings.Join(parts, " · ")
}

// LongSummary is Summary, and what the profile gives out from the library.
func LongSummary(p Profile) string {
	s := Summary(p)
	if p.Library == nil {
		return s
	}
	if s == "" {
		return "+ " + p.Library.Summary()
	}
	return s + " · + " + p.Library.Summary()
}

// Report is what applying a profile did to the library, a line each, for
// the terminal: none for a profile without the library's setup.
func Report(a Applied) []string {
	r := a.Library
	if r == nil {
		return nil
	}
	var out []string
	if len(r.Changed) > 0 {
		out = append(out, "library written into "+strings.Join(r.Changed, ", "))
	}
	if len(r.Missing) > 0 {
		out = append(out, "skipped, no longer in the library: "+strings.Join(r.Missing, ", "))
	}
	for _, p := range r.Problems {
		out = append(out, p.Agent+" "+p.What+": "+p.Error)
	}
	return out
}
