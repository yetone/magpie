package library

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/coderun"
	"github.com/yetone/magpie/internal/gateway"
)

// A skill with code: a code.js beside its SKILL.md whose default export is
// an async function of one object of args. The MCP router's run_code runs
// it as skills.<name>(args), with the agent's tools to call
// (gateway/hub_code.go); the skill router's SKILL.md says which skills
// have one. A script run_code ran without an error can be saved as one
// (save_as): a skill of the user's, given to the agent that saved it.

// SkillCode is the file a skill's code is in.
const SkillCode = "code.js"

// maxSkillCode is how large a code.js is read: a script, not a bundle.
const maxSkillCode = 256 << 10

// skillCode is the code.js of the skill in dir, "" when it has none or
// it can't be read.
func skillCode(dir string) string {
	f, err := os.Open(filepath.Join(dir, SkillCode))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSkillCode+1))
	if err != nil || len(b) > maxSkillCode {
		return ""
	}
	return string(b)
}

// hasCode is whether the skill in dir has a code.js.
func hasCode(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, SkillCode))
	return err == nil && fi.Mode().IsRegular()
}

// hubSkills are the skills the hub gives agentID with code, by name: those
// the agent is given (in a project, as the project gives them), every one
// for "" (the address any other app is given).
func (l *Library) hubSkills(agentID string) []gateway.HubSkill {
	id, key, scoped := strings.Cut(agentID, gateway.HubProjectSep)
	var p *Project
	var given map[string][]string
	if scoped {
		p = l.projectByKey(key)
	}
	if p != nil {
		given, _, _ = l.withPlugins(p)
	}
	var out []gateway.HubSkill
	for _, s := range l.Skills {
		on := id == "" && !s.OffHere && !s.Dangling || s.gives(id)
		if p != nil {
			if slices.Contains(p.SkillsOff[s.Name], id) {
				on = false
			} else if !s.OffHere && !s.Dangling && slices.Contains(given[s.Name], id) {
				on = true
			}
		}
		if !on {
			continue
		}
		dir := skillDir(s.Name)
		code := skillCode(dir)
		if code == "" {
			continue
		}
		m, _ := readMeta(dir)
		out = append(out, gateway.HubSkill{Name: s.Name, About: m.Description, Code: code})
	}
	slices.SortFunc(out, func(a, b gateway.HubSkill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (theHub) Skills(agentID string) []gateway.HubSkill {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil || !l.MCPHub {
		return nil
	}
	return l.hubSkills(agentID)
}

// SaveSkill makes a skill of the user's from code a run_code script ran:
// CreateSkill's SKILL.md, saying how to run it, and code as its code.js,
// given to agentID (none for "", the address any other app is given).
func (theHub) SaveSkill(agentID, name, about, code string) error {
	if _, err := coderun.SkillSource(code); err != nil {
		return err
	}
	var agents []string
	if agentID != "" {
		if err := checkAgent(agentID); err != nil {
			return err
		}
		agents = []string{agentID}
	}
	body := fmt.Sprintf("Run it with magpie's MCP router: its run_code tool calls it as `skills.%s(args)`, "+
		"with the args this skill's description names, and it calls the router's tools itself. "+
		"Its code is %s, in this folder.\n", strings.ReplaceAll(name, "-", "_"), SkillCode)
	_, err := createSkill(name, about, agents, body, map[string]string{SkillCode: code})
	return err
}
