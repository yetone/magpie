package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The warning's "All agents" (zysicyj on X: ten agents of forty skills
// each, settled a click an agent): every agent's own skills by the
// library's names settled the same way in one change. Keep leaves each
// agent's own and takes it off the skills; Use sets each one's own aside
// and gives it the library's. An agent or a skill gone since the page
// listed them is passed over.
func TestSettleOwnSkills(t *testing.T) {
	h := sandbox(t)
	agents := map[string]string{
		"claude": filepath.Join(h, ".claude/skills"),
		"codex":  filepath.Join(h, ".codex/skills"),
		"gemini": filepath.Join(h, ".gemini/skills"),
	}
	names := []string{"alpha", "beta"}
	for _, n := range names {
		if _, err := createSkill(n, "the library's", nil, "body\n", nil); err != nil {
			t.Fatal(err)
		}
	}
	give := func() map[string][]string {
		t.Helper()
		for id, d := range agents {
			for _, n := range names {
				skill(t, filepath.Join(d, n), n, "own of "+id)
			}
		}
		own := map[string][]string{}
		for _, n := range names {
			r, err := SkillAgents(n, []string{"claude", "codex", "gemini"})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range r.Problems {
				if p.Own && p.What == "skill:"+n && !slices.Contains(own[p.Agent], n) {
					own[p.Agent] = append(own[p.Agent], n)
				}
			}
		}
		for id := range agents {
			if len(own[id]) != len(names) {
				t.Fatalf("own skills in the way: %v", own)
			}
		}
		return own
	}
	ownLeft := func(r *Result) (n int) {
		for _, p := range r.Problems {
			if p.Own {
				n++
			}
		}
		return n
	}

	// keep: each agent's own stays, and the library no longer gives it
	own := give()
	own["nobody"] = names                              // an agent gone since
	own["claude"] = append(own["claude"], "taken-out") // a skill gone since
	r, err := SettleOwnSkills(own, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := ownLeft(r); n != 0 {
		t.Errorf("%d own skills still in the way: %+v", n, r.Problems)
	}
	for _, n := range names {
		if a := skillAgents(t, n); len(a) != 0 {
			t.Errorf("%s still given to %v", n, a)
		}
		for id, d := range agents {
			if linked(filepath.Join(d, n)) || !strings.Contains(read(t, filepath.Join(d, n, "SKILL.md")), "own of "+id) {
				t.Errorf("%s's own %s was touched", id, n)
			}
		}
	}

	// use: each agent's own is set aside, the library's in its place
	own = give()
	r, err = SettleOwnSkills(own, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := ownLeft(r); n != 0 {
		t.Errorf("%d own skills still in the way: %+v", n, r.Problems)
	}
	for _, n := range names {
		if a := skillAgents(t, n); !slices.Contains(a, "claude") || !slices.Contains(a, "codex") || !slices.Contains(a, "gemini") {
			t.Errorf("%s given to %v", n, a)
		}
		for id, d := range agents {
			if !ours(filepath.Join(d, n), n) {
				t.Errorf("%s hasn't the library's %s", id, n)
			}
			m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", id, "skills", n, "SKILL.md"))
			if !slices.ContainsFunc(m, func(p string) bool { return strings.Contains(read(t, p), "own of "+id) }) {
				t.Errorf("%s's own %s isn't kept aside: %v", id, n, m)
			}
		}
	}

	if _, err := SettleOwnSkills(nil, true); err == nil {
		t.Error("no agent, and no error")
	}
}
