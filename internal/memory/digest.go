package memory

import (
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The digest is the part of memory an agent is given in its instructions,
// so it knows the user's preferences and rules from its first word, before
// it thinks to search. It is held to a budget, pinned memories and the
// kinds that hold longest first, and changes only when Publish is called:
// when the user changes memory, and once a day for what agents saved
// meanwhile. An agent's instructions change mid-session only then, so the
// prompt caches built on them aren't thrown away at every save.
//
// Its text goes to every agent's model vendor: it names neither magpie nor
// any vendor, only the memories and how to read them.

// Budgets, in characters, of the global part and of each project's.
var (
	globalBudget  = 4000
	projectBudget = 1500
	agentBudget   = 1000
	allBudget     = 8000
)

var kindTitles = map[string]string{
	Profile:    "About the user",
	Preference: "Preferences",
	Rule:       "Rules",
	Reference:  "References",
	Fact:       "Facts",
	Procedure:  "How things are done",
	Episode:    "Recent decisions",
}

// render is the digest of one scope's memories within budget characters:
// a heading per kind, a line per memory, pinned and newer ones first, and
// none cut in the middle. "" when the scope has none.
func render(ms []*Memory, scope string, budget int) string {
	pool := []*Memory{}
	for _, m := range ms {
		if m.Scope == scope && m.Status == Active {
			pool = append(pool, m)
		}
	}
	slices.SortStableFunc(pool, func(a, b *Memory) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if d := kindRank(a.Kind) - kindRank(b.Kind); d != 0 {
			return d
		}
		return b.Updated.Compare(a.Updated)
	})
	// the lines picked within budget, then put back in kind order
	picked := map[string][]string{}
	used := 0
	for _, m := range pool {
		line := "- " + oneLine(m.Text)
		if m.Why != "" {
			line += " (why: " + oneLine(m.Why) + ")"
		}
		n := utf8.RuneCountInString(line) + 1
		if used+n > budget {
			continue
		}
		used += n
		picked[m.Kind] = append(picked[m.Kind], line)
	}
	var b strings.Builder
	for _, k := range Kinds {
		if len(picked[k]) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(kindTitles[k] + ":\n")
		b.WriteString(strings.Join(picked[k], "\n"))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// publish renders every scope's digest into f.
func publish(f *file) {
	scopes := map[string]bool{}
	for _, m := range f.Memories {
		if m.Status == Active {
			scopes[m.Scope] = true
		}
	}
	d := map[string]string{}
	for s := range scopes {
		budget := globalBudget
		switch {
		case strings.HasPrefix(s, "project:"):
			budget = projectBudget
		case strings.HasPrefix(s, "agent:"):
			budget = agentBudget
		}
		if t := render(f.Memories, s, budget); t != "" {
			d[s] = t
		}
	}
	f.Digest = d
	f.Published = now().UTC()
}

// Publish makes what memory holds now the digest agents are given. It
// reports whether any digest changed, for the caller to write it into the
// agents' instructions.
func Publish() (changed bool, err error) {
	err = change(func(f *file) error {
		old := f.Digest
		publish(f)
		changed = !maps.Equal(old, f.Digest)
		return nil
	})
	return changed, err
}

// PublishDue publishes when the digest was last published more than a day
// ago, and memory holds something or held something then. It reports
// whether any digest changed.
func PublishDue() (bool, error) {
	f, err := view()
	if err != nil {
		return false, err
	}
	if now().Sub(f.Published) < 24*time.Hour || (len(f.Memories) == 0 && len(f.Digest) == 0) {
		// nothing kept yet: no file written for it
		return false, nil
	}
	return Publish()
}

// Digest is the published digest an agent is given: the global part, the
// agent's own, and each project's under the folders it was seen in, held
// to the whole budget, the projects with the newest memories first. ""
// when there is nothing, or memory.json can't be read (unknown: the
// caller keeps what the agent has).
func Digest(agent string) (string, error) {
	f, err := cached()
	if err != nil {
		return "", err
	}
	return digestOf(f, agent), nil
}

func digestOf(f *file, agent string) string {
	if len(f.Digest) == 0 {
		return ""
	}
	parts := []string{}
	used := 0
	add := func(s string) bool {
		n := utf8.RuneCountInString(s)
		if used+n > allBudget {
			return false
		}
		used += n
		parts = append(parts, s)
		return true
	}
	if g := f.Digest[Global]; g != "" {
		add(g)
	}
	if a := f.Digest["agent:"+agent]; agent != "" && a != "" {
		add("Only for you:\n" + a)
	}
	// projects, newest change first
	latest := map[string]time.Time{}
	for _, m := range f.Memories {
		if m.Status == Active && strings.HasPrefix(m.Scope, "project:") && m.Updated.After(latest[m.Scope]) {
			latest[m.Scope] = m.Updated
		}
	}
	scopes := []string{}
	for s := range f.Digest {
		if strings.HasPrefix(s, "project:") {
			scopes = append(scopes, s)
		}
	}
	slices.SortFunc(scopes, func(a, b string) int {
		if c := latest[b].Compare(latest[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	for _, s := range scopes {
		key := strings.TrimPrefix(s, "project:")
		where := key
		if p := f.Projects[key]; p != nil && len(p.Roots) > 0 {
			where = strings.Join(p.Roots, ", ")
		}
		add("When working in " + where + ":\n" + f.Digest[s])
	}
	if len(parts) == 0 {
		return ""
	}
	return "## Memory\n\nWhat is known from earlier conversations with the user. Follow it unless the user says otherwise now. When a memory_search tool is at hand, search it for more before asking the user again, and save what they ask to be remembered with memory_save.\n\n" +
		strings.Join(parts, "\n\n")
}
