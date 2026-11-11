package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/memory"
)

// MemorySkill makes the procedure memory with id a skill of the library's
// own, as Memory's suggestion does once the agents search for it often:
// a SKILL.md holding its text (and why), named from its words, given to
// the agents ticked for instructions (the ones memory is published to),
// or to every agent with skills when none is. The memory is archived
// then, noted as that skill's (memory.MadeSkill). It answers with the
// skill's name.
func MemorySkill(id string) (*Result, string, error) {
	m, err := memory.Get(id)
	if err != nil {
		return nil, "", err
	}
	if m.Kind != memory.Procedure {
		return nil, "", errors.New("only a procedure is made a skill")
	}
	if m.Skill != "" {
		return nil, "", fmt.Errorf("it is the skill %s already", m.Skill)
	}
	var name string
	res, err := change(func(l *Library) error {
		name = freeOwnName(l, skillNameOf(m))
		dir := skillDir(name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillText(name, m)), 0o644); err != nil {
			os.RemoveAll(dir)
			return err
		}
		l.Skills = append(l.Skills, &Skill{Name: name, Agents: skillAgentsOf(l)})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if err := memory.MadeSkill(id, name); err != nil {
		return res, name, fmt.Errorf("the skill %s is made, but the memory couldn't be set aside: %w", name, err)
	}
	return res, name, nil
}

// skillAgentsOf is who a skill made from memory goes to.
func skillAgentsOf(l *Library) []string {
	var ticked, all []string
	for _, t := range Targets() {
		if t.Skills == "" {
			continue
		}
		all = append(all, t.Agent.ID)
		for _, a := range l.Instructions.Agents {
			if a == t.Agent.ID {
				ticked = append(ticked, a)
			}
		}
	}
	if len(ticked) > 0 {
		return ticked
	}
	if all == nil {
		all = []string{}
	}
	return all
}

// skillNameOf is a skill's name from a memory's first words in Latin
// letters, or from its id when it has fewer than two (a memory in Chinese).
func skillNameOf(m *memory.Memory) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(m.Text), func(r rune) bool {
		return r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(words) == 5 {
			break
		}
		words = append(words, w)
	}
	name := strings.Join(words, "-")
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-")
	}
	if len(words) < 2 || checkName("skill", name) != nil { // one word in a Chinese text names nothing
		name = "procedure-" + strings.TrimPrefix(m.ID, "m_")
	}
	return name
}

// freeOwnName is name, or name-2, -3… when the library or its folder has
// one by that name already.
func freeOwnName(l *Library, name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		if l.skill(n) != nil {
			continue
		}
		if _, err := os.Lstat(skillDir(n)); err == nil {
			continue
		}
		return n
	}
}

// skillText is the SKILL.md of a skill made from m: its first line says
// when to use it, as a description does, and the rest is the procedure.
func skillText(name string, m *memory.Memory) string {
	desc, _, _ := strings.Cut(strings.TrimSpace(m.Text), "\n")
	if r := []rune(desc); len(r) > 200 {
		desc = string(r[:200]) + "…"
	}
	q, _ := json.Marshal(desc) // a JSON string is a YAML one
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n", name, q)
	b.WriteString(strings.TrimSpace(m.Text))
	b.WriteString("\n")
	if m.Why != "" {
		b.WriteString("\nWhy: " + strings.TrimSpace(m.Why) + "\n")
	}
	return b.String()
}
