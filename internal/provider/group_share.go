package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A routing group shared as text (laincatx on X): one group's routing,
// rules and members, copied from one magpie and pasted into another. It
// says what it is (ShareMark) and in which format (ShareVersion), so a
// magpie can tell it from any other text and one made by a newer magpie
// from one it can read.
const (
	ShareMark    = "routing-group"
	ShareVersion = 1
)

// SharedGroup is the text a group is shared as.
type SharedGroup struct {
	Magpie  string `json:"magpie"`
	Version int    `json:"version"`
	Group   Group  `json:"group"`
}

// ShareGroup is group id as text to paste into another magpie: its
// members as it names them, its patterns, routing, rules, classifier,
// effort, context, levels, fast and off members. A group holds no key or
// account of its own, only provider/model ids, so nothing secret goes with
// it; what is derived (the models its patterns match now, found, hidden,
// switched off) stays behind, and so do the rules that hold by a folder,
// in it or outside it:
// a folder is a path on the sharer's own machine, which names their
// projects and means nothing on another.
func ShareGroup(id string) (string, error) {
	all := Groups()
	i := slices.IndexFunc(all, func(g Group) bool { return g.ID == id && !g.Hidden })
	if i < 0 {
		return "", fmt.Errorf("no group %q", id)
	}
	// a copy shares nothing with the group read: the rules' time windows
	// are pointers
	raw, err := json.Marshal(all[i])
	if err != nil {
		return "", err
	}
	var g Group
	if err := json.Unmarshal(raw, &g); err != nil {
		return "", err
	}
	g.Members, g.Match = ownMembers(g)
	g.Matched = nil
	g.Auto, g.Hidden, g.Disabled = false, false, false
	g.Accounts = nil // this magpie's accounts: another has its own
	g.Seen = nil     // what this magpie found: another finds its own
	g.Rules = slices.DeleteFunc(g.Rules, Rule.ByFolder)
	out, err := json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: g})
	return string(out), err
}

// ShareError is why a text can't be imported as a group, by Why, for the
// GUI to say in the reader's language: "not_shared" (no group shared from
// magpie), "newer" (a format past ShareVersion, in Format), "no_format",
// "unreadable" (Err), "none_here" (none of its models here, in Models).
type ShareError struct {
	Why    string
	Format int
	Models []string
	Err    error
}

func (e *ShareError) Error() string {
	switch e.Why {
	case "newer":
		return fmt.Sprintf("this group was shared from a newer magpie (format %d): update magpie to import it", e.Format)
	case "no_format":
		return "this shared group says no format it is in: copy it again with Copy to share"
	case "unreadable":
		return fmt.Sprintf("this shared group can't be read: %v", e.Err)
	case "none_here":
		return "none of the group's models is in your magpie: " + strings.Join(e.Models, ", ")
	}
	return "this is not a routing group shared from magpie: copy one with Copy to share in a group's menu"
}

func (e *ShareError) Unwrap() error { return e.Err }

// Imported is what ImportGroup made of a shared group.
type Imported struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Missing are the models and groups the text names that this magpie
	// has none of, left out of the group: members, and the classifier.
	Missing []string `json:"missing,omitempty"`
	// Instead are the members this magpie has under another provider: the
	// same model (AutoGroupOf), "shared → here".
	Instead []string `json:"instead,omitempty"`
	// Rules is how many of the group's rules were left out with the
	// member they send to, or the classifier their intent needs, or for
	// holding by a folder of the sharer's.
	Rules int `json:"rules,omitempty"`
	// Replaced: the text's id was a group of the user's, which it
	// replaced (ImportGroupOver) instead of adding another.
	Replaced bool `json:"replaced,omitempty"`
}

// ReadShared reads a shared group's text, as copied or pasted from a chat:
// the object may have words or a code fence around it.
func ReadShared(text string) (SharedGroup, error) {
	var s SharedGroup
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	notOne := &ShareError{Why: "not_shared"}
	if start < 0 || end < start {
		return s, notOne
	}
	var probe struct {
		Magpie  string          `json:"magpie"`
		Version int             `json:"version"`
		Group   json.RawMessage `json:"group"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &probe); err != nil || probe.Magpie != ShareMark {
		return s, notOne
	}
	switch {
	case probe.Version > ShareVersion:
		return s, &ShareError{Why: "newer", Format: probe.Version}
	case probe.Version < 1:
		return s, &ShareError{Why: "no_format"}
	}
	if err := json.Unmarshal(probe.Group, &s.Group); err != nil {
		return s, &ShareError{Why: "unreadable", Err: err}
	}
	s.Magpie, s.Version = probe.Magpie, probe.Version
	return s, nil
}

// ImportGroup adds the group a shared text holds as a group of the
// user's, named as it was shared and numbered past a name or id in use,
// so it never replaces a group there is. It is listed last. A member this
// magpie has no provider of is the same model under one it has, where it
// serves it (Instead), else left out (Missing), and with it the rules that
// send to it and its place among the fast and off members. Nothing is
// added when none of its members is left.
func ImportGroup(text string) (Imported, error) { return importGroup(text, false) }

// ImportGroupOver is ImportGroup that, when the text's group id is a group
// of the user's own (not one magpie found, not one removed), replaces that
// group by what the text holds, in its place in the list, rather than
// adding another. Only that group changes. What the text can't carry stays
// as the user has it: the group's accounts, its rules that hold by a
// folder, whether it is switched on and its place among the groups. With
// no such group it adds one, as ImportGroup does.
func ImportGroupOver(text string) (Imported, error) { return importGroup(text, true) }

func importGroup(text string, over bool) (Imported, error) {
	s, err := ReadShared(text)
	if err != nil {
		return Imported{}, err
	}
	g := s.Group
	g.Accounts = nil // another magpie's accounts, never this one's
	entries := providerEntries()
	all := Groups()
	var res Imported
	// what each member the text names is here: itself, another provider's
	// same model, or "" for none
	here := map[string]string{}
	place := func(m string) string {
		m = strings.TrimSpace(m)
		if to, ok := here[m]; ok {
			return to
		}
		to := memberHere(entries, all, m)
		here[m] = to
		switch {
		case to == "":
			res.Missing = append(res.Missing, m)
		case to != m:
			res.Instead = append(res.Instead, m+" → "+to)
		}
		return to
	}
	members, match := ownMembers(g)
	g.Members = nil
	for _, m := range members {
		if to := place(m); to != "" && !slices.Contains(g.Members, to) {
			g.Members = append(g.Members, to)
		}
	}
	g.Match, g.Matched = match, nil
	if len(g.Members) == 0 && len(g.Match) == 0 {
		return res, &ShareError{Why: "none_here", Models: res.Missing}
	}
	kept := func(list []string) []string {
		var out []string
		for _, m := range list {
			if to := here[strings.TrimSpace(m)]; to != "" && !slices.Contains(out, to) {
				out = append(out, to)
			}
		}
		return out
	}
	g.Off, g.Fast = kept(g.Off), slices.DeleteFunc(kept(g.Fast), func(f string) bool {
		// the same model under another provider may have no fast mode
		model, _ := memberEffortIn(entries, f)
		p, id, ok := resolveIn(entries, model)
		return ok && !CanFast(p, id)
	})
	g.Weights = renamedWeights(g.Weights, func(m string) string { return here[strings.TrimSpace(m)] })
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string { return here[strings.TrimSpace(m)] })
	if g.Routing != Manual && len(g.Off) == len(g.Members) {
		g.Off = nil // the ones it sent to are gone: those left are on
	}
	g.Pick = here[strings.TrimSpace(g.Pick)]
	if c := strings.TrimSpace(g.Classifier); c != "" {
		if g.Classifier = place(c); g.Classifier == GroupPrefix+g.ID {
			g.Classifier = ""
		}
	}
	// a rule sends to a model the group names, or one its patterns match
	// here
	in := append(slices.Clone(g.Members), matchesIn(entries, g)...)
	var rules []Rule
	for _, r := range g.Rules {
		// each model it sends to as this magpie has it; one it hasn't is
		// left out of the rule, and a rule left with none goes
		r.MapUses(func(u string) string {
			use := here[strings.TrimSpace(u)]
			if _, named := here[strings.TrimSpace(u)]; !named && !IsPattern(u) {
				// a rule may send to a model a pattern matches: one this
				// magpie has, or none
				use = memberHere(entries, all, u)
			}
			if !slices.Contains(in, use) {
				return ""
			}
			return use
		})
		// a folder is the sharer's path, never this machine's
		if r.Use == "" || (r.Intent != "" && g.Classifier == "") || r.ByFolder() {
			res.Rules++
			continue
		}
		rules = append(rules, r)
	}
	g.Rules = rules
	if g.Classifier == "" {
		g.Effort = "" // nothing to rate a turn with
	}
	name := strings.TrimSpace(g.Name)
	if name == "" {
		name = "Shared group"
	}
	id := strings.ToLower(strings.TrimSpace(g.ID))
	if i := slices.IndexFunc(all, func(o Group) bool { return over && id != "" && o.ID == id && !o.Auto && !o.Hidden }); i >= 0 {
		// the user's group of that id: its name stays free of the others'
		// names, and what the text can't carry stays as it is
		old := all[i]
		g.Name, _ = freeGroupName(slices.DeleteFunc(slices.Clone(all), func(o Group) bool { return o.ID == id }), name, id)
		g.ID = old.ID
		g.Accounts = old.Accounts
		for _, r := range old.Rules {
			if !r.ByFolder() {
				continue
			}
			r.MapUses(func(u string) string {
				if slices.Contains(in, u) {
					return u
				}
				return ""
			})
			if r.Use != "" && (r.Intent == "" || g.Classifier != "") {
				g.Rules = append(g.Rules, r)
			}
		}
		g.Auto, g.Hidden, g.Disabled = false, false, false
		if err := SaveGroup(g); err != nil {
			return res, err
		}
		res.ID, res.Name, res.Replaced = g.ID, g.Name, true
		return res, nil
	}
	g.Name, g.ID = freeGroupName(all, name, "")
	g.Auto, g.Hidden, g.Disabled = false, false, false
	if err := SaveGroup(g); err != nil {
		return res, err
	}
	res.ID, res.Name = g.ID, g.Name
	return res, nil
}

// memberHere is what member m of a shared group is in this magpie: m
// itself when it has the provider (or the group), else the same model
// (AutoGroupOf) under the first provider here that serves it, in the
// Providers order; "" when none does. A member's own effort goes with it.
func memberHere(entries []Entry, all []Group, m string) string {
	m = CanonicalRef(strings.TrimPrefix(strings.TrimSpace(m), "magpie/"))
	model, effort := memberEffortIn(entries, m)
	if gid, ok := strings.CutPrefix(model, GroupPrefix); ok {
		if slices.ContainsFunc(all, func(g Group) bool { return g.ID == gid && !g.Hidden }) {
			return m
		}
		return ""
	}
	pid, id, ok := strings.Cut(model, "/")
	if !ok {
		return ""
	}
	if _, err := Find(pid); err == nil || isModel(entries, model) {
		return m
	}
	same := AutoGroupOf(pid, id)
	for _, e := range entries {
		if e.Group == "" && AutoGroupOf(e.Provider.ID, e.Model) == same {
			return WithMemberEffort(e.ID, effort)
		}
	}
	return ""
}
