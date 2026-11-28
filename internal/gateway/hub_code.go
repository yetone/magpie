package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/coderun"
	"github.com/yetone/magpie/internal/settings"
)

// The router's code mode: one tool, run_code, runs a script that calls the
// router's tools and gives back only what the script returns and logs. A
// model that needs several calls, or one call whose answer it reads only
// part of, makes one call in place of many, and the answers it doesn't
// need never reach its context. The script runs in goja, a JavaScript
// interpreter with nothing of the computer's: no files, network,
// processes, require or import, in a process of its own (coderun: a
// builtin is past goja's interrupt, and could take magpie's memory or a
// core with it). What it can reach is the tools
// the agent's router gives it, each run as call_tool runs one (hubCall:
// the key's limit, HereOnly, the Library's off tools, a row of the request
// log each). settings' RouterNoCode turns it off.

// hubCodeTool is code mode's tool; a tool of the hub's is <server>__<tool>,
// so it is never one of its tools' names.
const hubCodeTool = "run_code"

// hubCodeWait is how long a script runs in all, its calls included: under
// the 60s an MCP client gives a call, as hubCallWait is. A script with a
// tool of a server given longer (HubTool.Wait) runs as long as that
// server's call may (hubCodeBudget).
var hubCodeWait = hubCallWait

// hubCodeBudget is how long a script against ts runs in all: hubCodeWait,
// or the longest call timeout among their servers, so a script is never
// ended before a call of its own tools may be.
func hubCodeBudget(ts []hubTool) time.Duration {
	d := hubCodeWait
	for _, t := range ts {
		d = max(d, t.Wait)
	}
	return d
}

// hubCodeCalls is how many tool calls one script makes at most, and
// hubCodeAtOnce how many run at the same time.
const (
	hubCodeCalls  = 100
	hubCodeAtOnce = 8
)

// hubCodeMax is how many characters of what a script returns and logs go
// back to the model.
const hubCodeMax = 40000

// hubCodeHeap is how far the heap of a script's process may grow before
// it is ended: goja has no memory limit of its own.
var hubCodeHeap uint64 = 512 << 20

// hubCodeHelper is the program a script runs in, started as
// coderun.Command: magpie itself when empty (coderun.Run).
var hubCodeHelper string

// hubCodeOn is whether the router gives agent run_code beside its tools.
func hubCodeOn(n int) bool {
	return n > 0 && !settings.Shared().RouterNoCode
}

// hubCodeSay is what the instructions add when the router gives run_code.
const hubCodeSay = " " + hubCodeTool + " runs JavaScript that calls these tools and gives back only what it returns: several calls, or the part of an answer you need, in one step."

// HubSkill is a skill of the Library's with code, given to an agent: a
// code.js beside its SKILL.md whose default export is an async function.
// run_code runs it as skills.<name>(args), calling the same tools.
type HubSkill struct {
	Name, About string
	Code        string
}

// HubSkills is a Hub that also gives an agent's skills with code and
// saves a script that ran as a new one (library/mcp_hub.go).
type HubSkills interface {
	// Skills are agent's skills with code: those it is given, every one
	// for agent "" (HubMCPAll's)
	Skills(agent string) []HubSkill
	// SaveSkill makes a skill of the user's called name in the Library,
	// given to agent, with about as its description and code as its
	// code.js. It never writes over a skill or a folder that is there.
	SaveSkill(agent, name, about, code string) error
}

// hubSkillsOf are agent's skills with code: none when the hub keeps no
// skills.
func hubSkillsOf(agent string) []HubSkill {
	if h, ok := MCPHub.(HubSkills); ok {
		return h.Skills(agent)
	}
	return nil
}

// hubSkillKey is skill name n as a script calls it: - as _.
func hubSkillKey(n string) string { return strings.ReplaceAll(n, "-", "_") }

// hubSkillsSay is how many skills run_code's description names, and
// hubSkillsAbout how much of each one's description it gives.
const (
	hubSkillsSay   = 30
	hubSkillsAbout = 200
)

// hubSkillFile is the code.js of a script saved as a skill (save_as): an
// async function of args whose body is the script.
func hubSkillFile(code string) string {
	return "// Saved from magpie's run_code. It runs there as skills.<name>(args),\n" +
		"// calling the MCP router's tools as tools.<server>__<tool>(args).\n" +
		"export default async (args) => {\n" + strings.TrimRight(code, "\n") + "\n}\n"
}

// hubCodeSpec is run_code as tools/list gives it, its description after
// where; onDemand says the tools' parameters come from find_tools, and
// skills are the agent's skills with code.
func hubCodeSpec(where string, onDemand bool, skills []HubSkill, wait time.Duration) map[string]any {
	pre := ""
	if where != "" {
		pre = "[" + where + "] "
	}
	params := "Each tool's parameters are its inputSchema."
	if onDemand {
		params = hubFindTool + " gives each tool's name and parameters."
	}
	return map[string]any{
		"name": hubCodeTool,
		"description": pre + "Runs JavaScript that calls this server's tools, and gives back only what the code returns and console.logs. " +
			"Use it for several tool calls in one step (one after another, in a loop, or at once with Promise.all), or to keep only the part of a tool's answer you need. " +
			"Each tool is an async function on `tools`, by its name: `const r = await tools[\"github__search_issues\"]({q: \"bug\"})` (also tools.github__search_issues, with - as _). " + params + " " +
			"A tool gives back its structured result, else its text (parsed when it is JSON); a tool's error is thrown. " +
			"The code is the body of an async function: `return` the result. Plain JavaScript only: no require, import, fetch, files or timers. " +
			fmt.Sprintf("At most %d tool calls and %s in all.", hubCodeCalls, hubSecs(wait)) +
			hubSkillsLine(skills) +
			" To keep code that worked for next time, run it with save_as: it is saved as a skill of the user's, skills.<name>(args), that reads its inputs from `args`.",
		"inputSchema": json.RawMessage(`{"type":"object","properties":{` +
			`"code":{"type":"string","description":"JavaScript, the body of an async function; return what you need"},` +
			`"args":{"type":"object","description":"Inputs the code reads as the object args"},` +
			`"save_as":{"type":"object","description":"Also save the code, when it runs without an error, as a skill of the user's for skills.<name>(args) later","properties":{"name":{"type":"string","description":"lowercase words joined by -"},"description":{"type":"string","description":"what it does and when to use it, and the args it takes"}},"required":["name","description"]}` +
			`},"required":["code"]}`),
	}
}

// hubSkillsLine is what run_code's description says of skills, the
// agent's skills with code.
func hubSkillsLine(skills []HubSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" The user's skills with code are async functions on `skills`, each given one object of args, calling the same tools:")
	for i, k := range skills {
		if i == hubSkillsSay {
			fmt.Fprintf(&b, " and %d more", len(skills)-i)
			break
		}
		if i > 0 {
			b.WriteString(";")
		}
		fmt.Fprintf(&b, " skills.%s", hubSkillKey(k.Name))
		about := strings.Join(strings.Fields(k.About), " ")
		if r := []rune(about); len(r) > hubSkillsAbout {
			about = string(r[:hubSkillsAbout]) + "…"
		}
		if about != "" {
			b.WriteString(" (" + about + ")")
		}
	}
	b.WriteString(".")
	return b.String()
}

// hubCodeRun answers run_code: the script in args run against ts, the
// agent's tools.
func (s *Server) hubCodeRun(r *http.Request, agent string, ts []hubTool, args json.RawMessage, meta hubCallMeta) any {
	var p struct {
		Code   string          `json:"code"`
		Args   json.RawMessage `json:"args"`
		SaveAs *struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"save_as"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return toolResult(hubCodeTool+" takes {\"code\": \"<JavaScript>\"}: "+err.Error(), true)
	}
	if strings.TrimSpace(p.Code) == "" {
		return toolResult(hubCodeTool+" needs code: the body of an async function that calls tools[\"<server>__<tool>\"](args) and returns what you need.", true)
	}
	call := func(ctx context.Context, name string, inner json.RawMessage) (any, error) {
		t, ok := hubCallNamed(ctx, ts, agent, name)
		if !ok {
			why := "no tool is called " + name
			if ms := hubNear(ts, name); len(ms) > 0 {
				near := make([]string, 0, hubFindFull)
				for _, m := range ms[:min(len(ms), hubFindFull)] {
					near = append(near, m.name)
				}
				why += "; close to it: " + strings.Join(near, ", ")
			}
			return nil, errors.New(why)
		}
		// each call is a row of its own, as call_tool's is; the script's
		// own row stays run_code's
		use := &mcpUse{}
		start := time.Now()
		cr := r.WithContext(context.WithValue(ctx, mcpUseKey{}, use))
		res := s.hubCall(cr, agent, t, inner, meta)
		ua, server, prov, model := use.get()
		logMCPCall(r, ua, server, r.URL.Path, t.name, prov, model, start, time.Since(start), mcpFailure(res, nil))
		return res, nil
	}
	mcpUseOf(r.Context()).as(HubAgentOf(agent))
	exe := hubCodeHelper
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return toolResult("code mode couldn't start: "+err.Error(), true)
		}
	}
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.name
	}
	var skills []coderun.Skill
	for _, k := range hubSkillsOf(agent) {
		skills = append(skills, coderun.Skill{Name: k.Name, Code: k.Code})
	}
	res := coderun.Run(r.Context(), exe, coderun.Script{Code: p.Code, Args: p.Args, Tools: names, Skills: skills}, coderun.Limits{
		Calls: hubCodeCalls, AtOnce: hubCodeAtOnce, Max: hubCodeMax, Heap: hubCodeHeap, Wait: hubCodeBudget(ts),
	}, call)
	if p.SaveAs == nil {
		return res
	}
	return hubCodeSave(res, agent, strings.TrimSpace(p.SaveAs.Name), p.SaveAs.Description, p.Code)
}

// hubCodeSave is res, what a script gave back, with what became of its
// save_as: the script saved as the skill name when it ran without an
// error.
func hubCodeSave(res any, agent, name, about, code string) any {
	m, _ := res.(map[string]any)
	if m == nil {
		return res
	}
	var say string
	h, ok := MCPHub.(HubSkills)
	if failed, _ := m["isError"].(bool); failed {
		say = "Not saved as a skill: the code didn't run without an error."
	} else if !ok {
		say = "Not saved as a skill: this router keeps no skills."
	} else if err := h.SaveSkill(HubAgentOf(agent), name, about, hubSkillFile(code)); err != nil {
		say = "Not saved as a skill: " + err.Error()
	} else {
		say = fmt.Sprintf("Saved as the skill %s: skills.%s(args) runs it from now on.", name, hubSkillKey(name))
	}
	content, _ := m["content"].([]any)
	m["content"] = append(content, map[string]any{"type": "text", "text": say})
	return m
}
