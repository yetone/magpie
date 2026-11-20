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
// the 60s an MCP client gives a call, as hubCallWait is.
var hubCodeWait = hubCallWait

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

// hubCodeSpec is run_code as tools/list gives it, its description after
// where; onDemand says the tools' parameters come from find_tools.
func hubCodeSpec(where string, onDemand bool) map[string]any {
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
			fmt.Sprintf("At most %d tool calls and %s in all.", hubCodeCalls, hubCodeWait),
		"inputSchema": json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"JavaScript, the body of an async function; return what you need"}},"required":["code"]}`),
	}
}

// hubCodeRun answers run_code: the script in args run against ts, the
// agent's tools.
func (s *Server) hubCodeRun(r *http.Request, agent string, ts []hubTool, args json.RawMessage, meta hubCallMeta) any {
	var p struct {
		Code string `json:"code"`
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
	return coderun.Run(r.Context(), exe, p.Code, names, coderun.Limits{
		Calls: hubCodeCalls, AtOnce: hubCodeAtOnce, Max: hubCodeMax, Heap: hubCodeHeap, Wait: hubCodeWait,
	}, call)
}
