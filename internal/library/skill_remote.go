package library

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// The skills the MCP router gives an agent on another computer
// (gateway/hub_skills.go, Michaelfpqc on X): an agent of a computer with
// no magpie, served by one in Docker or elsewhere. They are read from the
// Library as it is, on each ask: no list of the agents here (Targets) is
// made for them. Whether the router is on, and who is given them, is the
// gateway's (remoteSkillsFor): the pull script is given them with it off.

// remoteSkill says whether the agent agentID on another computer is given
// s: for "" (the address any app is given) every skill on here; for an
// agent, one ticked for it, or ticked for no agent at all (a magpie in
// Docker ticks none: it has no agent to tick). One taken from another
// magpie (Borrow) isn't passed on, as Shared leaves it out.
func (l *Library) remoteSkill(s *Skill, agentID string) bool {
	if s == nil || s.OffHere || s.Dangling || l.borrowedSkill(s.Name) {
		return false
	}
	id, _, _ := strings.Cut(agentID, gateway.HubProjectSep)
	return id == "" || len(s.Agents) == 0 || slices.Contains(s.Agents, id)
}

func (theHub) RemoteSkills(agentID string) ([]gateway.RemoteSkill, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []gateway.RemoteSkill
	for _, s := range l.Skills {
		if !l.remoteSkill(s, agentID) {
			continue
		}
		m, ok := readMeta(realDir(skillDir(s.Name)))
		if !ok {
			continue // no SKILL.md: nothing for an agent to read
		}
		out = append(out, gateway.RemoteSkill{Name: s.Name, About: m.Description, Manual: m.Manual})
	}
	slices.SortFunc(out, func(a, b gateway.RemoteSkill) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (theHub) RemoteSkill(agentID, name string) (gateway.RemoteSkill, gateway.RemoteSkillFiles, bool) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return gateway.RemoteSkill{}, gateway.RemoteSkillFiles{}, false
	}
	s := l.skill(name)
	if !l.remoteSkill(s, agentID) {
		return gateway.RemoteSkill{}, gateway.RemoteSkillFiles{}, false
	}
	dir := realDir(skillDir(s.Name))
	m, ok := readMeta(dir)
	if !ok {
		return gateway.RemoteSkill{}, gateway.RemoteSkillFiles{}, false
	}
	c := readSkillsMax([]string{dir}, maxSharedFile, maxSharedAll)[0]
	return gateway.RemoteSkill{Name: s.Name, About: m.Description, Manual: m.Manual},
		gateway.RemoteSkillFiles{Files: c.Files, Exec: c.Exec, Left: c.Left}, true
}
