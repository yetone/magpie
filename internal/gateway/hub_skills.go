package gateway

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
)

// The Library's skills for an agent on another computer (Michaelfpqc on
// X): a magpie in Docker, or on another computer, serves an agent there
// its models and its MCP router, and that computer has no magpie to link
// the Library's skills into the agent's folder. The router gives such an
// agent the skills too: its instructions name them, and get_skill gives a
// skill's SKILL.md and each of its other files. An agent on this computer
// has them in its own folder already (the Library links them there), and
// another magpie's router takes them as a whole from /v1/magpie/library
// (borrow.go), so neither is given them here. A copy on that computer's
// disk, for an agent to run a skill's scripts, is the pull script's
// (skills_pull.go).

// hubSkillTool is the tool that gives a skill; a server's tool is
// <server>__<tool>, so it is never one of their names.
const hubSkillTool = "get_skill"

// RemoteSkill is a skill of the Library's an agent on another computer is
// given: its name, its description, and Manual for one its SKILL.md says
// the model doesn't start itself (disable-model-invocation).
type RemoteSkill struct {
	Name, About string
	Manual      bool
}

// RemoteSkillFiles are a skill's files, by their path in its folder with
// /: Exec those that run, Left those too big to give.
type RemoteSkillFiles struct {
	Files      map[string][]byte
	Exec, Left []string
}

// HubRemoteSkills is a Hub that also gives an agent on another computer
// the Library's skills (library/skill_remote.go).
type HubRemoteSkills interface {
	// RemoteSkills are the skills agent is given, by name: every one for
	// agent "" (HubMCPAll's). An error is a Library it couldn't read,
	// never told as one with no skills (the pull script would take that
	// as every skill gone).
	RemoteSkills(agent string) ([]RemoteSkill, error)
	// RemoteSkill is the skill called name agent is given, with its
	// files; false when it isn't given one by that name.
	RemoteSkill(agent, name string) (RemoteSkill, RemoteSkillFiles, bool)
}

// hubRemoteSkills is the hub's skills for another computer, nil when it
// gives none.
func hubRemoteSkills() HubRemoteSkills {
	h, _ := MCPHub.(HubRemoteSkills)
	return h
}

// hubSkillsOfAgent are the skills the router gives agent: none when the
// Library can't be read.
func hubSkillsOfAgent(agent string) []RemoteSkill {
	ks, _ := hubRemoteSkills().RemoteSkills(agent)
	return ks
}

// hubSkillsHeld is why a key held to a budget, models, accounts or some
// MCP servers is given no skill: what it may use is bounded, and a skill
// is the user's own words and scripts.
const hubSkillsHeld = "This gateway key is held to a budget, models, accounts or some MCP servers, so it is given none of the skills in magpie's Library."

// hubSkillsFor is whether r is given the Library's skills through the
// router, and why not when it is an agent on another computer that isn't:
// remoteSkillsFor, with the router on.
func hubSkillsFor(r *http.Request) (bool, string) {
	if hubRemoteSkills() == nil || !MCPHub.On() {
		return false, ""
	}
	return remoteSkillsFor(r)
}

// remoteSkillsFor is whether r is given the Library's skills, by the
// router's get_skill or the pull script (skills_pull.go), and why not
// when it is an agent on another computer that isn't: a request from
// another computer, with a gateway key (the hub and the pull take no
// other from there: lanGuard), not a web page's or another magpie's
// router, its key held to nothing. A key for MCP servers only
// (ModelsOff) is held to no model, as hubVisitor reads it. This computer
// is given none: its agents have the skills in their own folders.
func remoteSkillsFor(r *http.Request) (bool, string) {
	// The header too, not FromRouter alone: only the hub's handler turns
	// it into the context, and the pull's routes are outside it.
	if hubRemoteSkills() == nil || local(r) || hubFromPage(r) || FromRouter(r.Context()) || r.Header.Get(RouterHeader) != "" {
		return false, ""
	}
	who := access.Caller(r.Context())
	if who.KeyID == "" || !sharedWith(r) {
		return false, ""
	}
	if who.ModelsOff() {
		who.Models = nil
	}
	if heldKey(who) || who.MCPHeld() {
		return false, hubSkillsHeld
	}
	return true, ""
}

// hubSkillsSayMax is how many characters the instructions' line of skills
// takes at most: beside hubSayMax, still under the 4096 Claude Code keeps.
const hubSkillsSayMax = 1500

// hubSkillsLineFor is what the instructions say of ks, the skills an agent
// on another computer is given: those its model may start itself, each
// with as much of its description as fits, and get_skill.
func hubSkillsLineFor(ks []RemoteSkill) string {
	ks = slices.DeleteFunc(slices.Clone(ks), func(k RemoteSkill) bool { return k.Manual })
	if len(ks) == 0 {
		return ""
	}
	head := "Skills in magpie's Library, for this agent: before a task one of them is for, call " + hubSkillTool + " with its name and follow the SKILL.md it gives."
	tail := ""
	per := min(300, max(40, (hubSkillsSayMax-runeLen(head))/len(ks)-8))
	var b strings.Builder
	for i, k := range ks {
		line := " " + k.Name
		if about := k.About; about != "" {
			if r := []rune(about); len(r) > per {
				about = string(r[:per]) + "…"
			}
			line += " (" + about + ")"
		}
		line += ";"
		if runeLen(head)+runeLen(b.String())+runeLen(line) > hubSkillsSayMax-20 {
			tail = fmt.Sprintf(" and %d more (%s lists them).", len(ks)-i, hubSkillTool)
			break
		}
		b.WriteString(line)
	}
	out := strings.TrimSuffix(b.String(), ";")
	if tail == "" {
		out += "."
	}
	return head + out + tail
}

// hubSkillSpec is get_skill as tools/list gives it, its description after
// where. It is loaded with the agent's own tools, not behind a search of
// its tools (hubAlwaysClaude): the instructions name it.
func hubSkillSpec(where string) map[string]any {
	pre := ""
	if where != "" {
		pre = "[" + where + "] "
	}
	return map[string]any{
		"name": hubSkillTool,
		"description": pre + "Gives a skill of the user's from magpie's Library: with name, its SKILL.md and the list of its other files; with name and file, that file. " +
			"With no name, every skill given to this agent, with what each is for. Read a skill before a task it is for, and follow it. " +
			"Its files are in magpie's Library, not on this computer: a script it runs is read with file, and written to a folder of your own to run.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill's name; empty lists every skill"},
				"file": map[string]any{"type": "string", "description": "A file of the skill's folder, by its path there (scripts/run.py); empty gives SKILL.md and the list"},
			},
		},
		"_meta": map[string]any{hubAlwaysClaude: true, hubAlwaysAgy: true},
	}
}

// hubSkillFileMax is how many bytes of one file get_skill gives.
const hubSkillFileMax = 100_000

// hubSkillGet answers get_skill for agent.
func hubSkillGet(agent string, args []byte) any {
	var p struct{ Name, File string }
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return toolResult(hubSkillTool+" takes {\"name\": \"<skill>\", \"file\": \"<path in its folder>\"}: "+err.Error(), true)
		}
	}
	h := hubRemoteSkills()
	name := strings.TrimSpace(p.Name)
	if name == "" {
		ks, err := h.RemoteSkills(agent)
		if err != nil {
			return toolResult("magpie couldn't read its Library: "+err.Error(), true)
		}
		if len(ks) == 0 {
			return toolResult("No skill in magpie's Library is given to this agent.", false)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d skills in magpie's Library for this agent; %s with a name gives one:\n", len(ks), hubSkillTool)
		for _, k := range ks {
			b.WriteString("- " + k.Name)
			if k.About != "" {
				b.WriteString(": " + k.About)
			}
			if k.Manual {
				b.WriteString(" (used only when the user asks for it)")
			}
			b.WriteString("\n")
		}
		return toolResult(b.String(), false)
	}
	k, fs, ok := h.RemoteSkill(agent, name)
	if !ok {
		why := "no skill called " + name + " is given to this agent"
		var near []string
		for _, k := range hubSkillsOfAgent(agent) {
			if strings.Contains(strings.ToLower(k.Name), strings.ToLower(name)) || strings.Contains(strings.ToLower(name), strings.ToLower(k.Name)) {
				near = append(near, k.Name)
			}
		}
		if len(near) > 0 {
			why += "; close to it: " + strings.Join(near, ", ")
		} else {
			why += "; " + hubSkillTool + " with no name lists them"
		}
		return toolResult(why, true)
	}
	file := strings.TrimSpace(strings.ReplaceAll(p.File, "\\", "/"))
	if file != "" {
		file = strings.TrimPrefix(path.Clean("/"+file), "/")
	}
	if file == "" || file == "SKILL.md" {
		return toolResult(hubSkillMain(k, fs), false)
	}
	b, ok := fs.Files[file]
	if !ok {
		if slices.Contains(fs.Left, file) {
			return toolResult(file+" of the skill "+k.Name+" is too big to give here.", true)
		}
		return toolResult("the skill "+k.Name+" has no file "+file+"; its files: "+strings.Join(hubSkillFileList(fs, false), ", "), true)
	}
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return toolResult(fmt.Sprintf("%s of the skill %s is not text (%d bytes), so it isn't given here.", file, k.Name, len(b)), true)
	}
	text := string(b)
	if len(b) > hubSkillFileMax {
		cut := hubSkillFileMax
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		text = string(b[:cut]) + fmt.Sprintf("\n\n[%s goes on: only the first %d of its %d bytes are given]", file, cut, len(b))
	}
	return toolResult(text, false)
}

// hubSkillMain is a skill's SKILL.md as get_skill gives it, after where
// its files are and what they are.
func hubSkillMain(k RemoteSkill, fs RemoteSkillFiles) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The skill %s, from magpie's Library. Paths in it are of the skill's folder, which is not on this computer: %s with name %q and file gives each.", k.Name, hubSkillTool, k.Name)
	if others := hubSkillFileList(fs, true); len(others) > 0 {
		b.WriteString(" Its other files: " + strings.Join(others, ", ") + ".")
	}
	if len(fs.Left) > 0 {
		b.WriteString(" Too big to give: " + strings.Join(fs.Left, ", ") + ".")
	}
	b.WriteString("\n\n")
	if md, ok := fs.Files["SKILL.md"]; ok {
		b.Write(md)
	} else {
		b.WriteString("(It has no SKILL.md.)")
	}
	return b.String()
}

// hubSkillFileList names fs's files but SKILL.md, sorted, with each one's
// size and whether it runs when sized.
func hubSkillFileList(fs RemoteSkillFiles, sized bool) []string {
	var out []string
	for _, f := range slices.Sorted(maps.Keys(fs.Files)) {
		if f == "SKILL.md" {
			continue
		}
		if !sized {
			out = append(out, f)
			continue
		}
		n := fmt.Sprintf("%s (%d bytes", f, len(fs.Files[f]))
		if slices.Contains(fs.Exec, f) {
			n += ", runs"
		}
		out = append(out, n+")")
	}
	return out
}
