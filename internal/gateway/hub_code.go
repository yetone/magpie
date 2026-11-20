package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/yetone/magpie/internal/settings"
)

// The router's code mode: one tool, run_code, runs a script that calls the
// router's tools and gives back only what the script returns and logs. A
// model that needs several calls, or one call whose answer it reads only
// part of, makes one call in place of many, and the answers it doesn't
// need never reach its context. The script runs in goja, a JavaScript
// interpreter in this process with nothing of the computer's: no files,
// network, processes, require or import. What it can reach is the tools
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

// hubCodeHeap is how far the heap may grow while a script runs before it
// is stopped: goja has no memory limit of its own, and a script that
// fills an array in a loop would take magpie's memory with it.
var hubCodeHeap uint64 = 512 << 20

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
	ctx, cancel := context.WithTimeout(r.Context(), hubCodeWait)
	defer cancel()
	return hubRunCode(ctx, p.Code, ts, call)
}

// hubCodeCall runs one tool by name for a script: its result as the tool
// gave it, or an error when no tool has that name.
type hubCodeCall func(ctx context.Context, name string, args json.RawMessage) (any, error)

// hubCodeDone is one tool call of a script, finished.
type hubCodeDone struct {
	res             any
	err             error
	resolve, reject func(any) error
}

// hubRunCode runs code, the body of an async function, with ts as the
// functions of `tools`, each run by call, and is the tool result of what
// it returned and logged.
func hubRunCode(ctx context.Context, code string, ts []hubTool, call hubCodeCall) any {
	vm := goja.New()
	vm.SetMaxCallStackSize(2000)
	var logs strings.Builder
	logged := 0
	log := func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, a := range c.Arguments {
			parts[i] = hubCodeShow(vm, a)
		}
		line := strings.Join(parts, " ") + "\n"
		if logged < hubCodeMax {
			logs.WriteString(line)
		}
		logged += len(line)
		return goja.Undefined()
	}
	console := vm.NewObject()
	for _, k := range []string{"log", "info", "warn", "error", "debug"} {
		console.Set(k, log)
	}
	vm.Set("console", console)

	done := make(chan hubCodeDone)
	slots := make(chan struct{}, hubCodeAtOnce)
	pending, calls := 0, 0
	var wg sync.WaitGroup
	defer wg.Wait()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))

	tool := func(name string) func(goja.FunctionCall) goja.Value {
		return func(c goja.FunctionCall) goja.Value {
			p, resolve, reject := vm.NewPromise()
			args := json.RawMessage(`{}`)
			if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
				s, err := stringify(goja.Undefined(), a)
				if err != nil {
					panic(err)
				}
				args = json.RawMessage(s.String())
			}
			if calls++; calls > hubCodeCalls {
				reject(vm.NewGoError(fmt.Errorf("more than %d tool calls in one script", hubCodeCalls)))
				return vm.ToValue(p)
			}
			pending++
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := hubCodeDone{resolve: resolve, reject: reject}
				select {
				case slots <- struct{}{}:
					d.res, d.err = call(runCtx, name, args)
					<-slots
				case <-runCtx.Done():
					d.err = runCtx.Err()
				}
				select {
				case done <- d:
				case <-runCtx.Done():
				}
			}()
			return vm.ToValue(p)
		}
	}
	tools := vm.NewObject()
	for _, t := range ts {
		f := tool(t.name)
		tools.Set(t.name, f)
		if alt := strings.ReplaceAll(t.name, "-", "_"); alt != t.name && tools.Get(alt) == nil {
			tools.Set(alt, f)
		}
	}
	// a name no tool listed is still asked for, as call_tool asks: a
	// server's list can change after it was listed
	vm.Set("__magpieTool", func(c goja.FunctionCall) goja.Value {
		return tool(c.Argument(0).String())(goja.FunctionCall{Arguments: c.Arguments[min(1, len(c.Arguments)):]})
	})
	vm.Set("__magpieTools", tools)
	if _, err := vm.RunString(`globalThis.tools = new Proxy(__magpieTools, {get(o, k) {
		if (typeof k !== "string" || k in o || k === "then" || k === "toJSON") return o[k];
		return (args) => __magpieTool(k, args);
	}}); delete globalThis.__magpieTools;`); err != nil {
		return toolResult("code mode couldn't start: "+err.Error(), true)
	}

	// stopped: the time is up, or the heap grew past hubCodeHeap
	var whyStopped error
	var stopMu sync.Mutex
	halt := func(why error) {
		stopMu.Lock()
		if whyStopped == nil {
			whyStopped = why
		}
		stopMu.Unlock()
		vm.Interrupt(why)
		stop()
	}
	watch := make(chan struct{})
	defer close(watch)
	go func() {
		base := hubHeap()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-watch:
				return
			case <-ctx.Done():
				halt(fmt.Errorf("it didn't finish in %s", hubCodeWait))
				return
			case <-tick.C:
				if h := hubHeap(); h > base && h-base > hubCodeHeap {
					halt(fmt.Errorf("it used more than %d MB of memory", hubCodeHeap>>20))
					return
				}
			}
		}
	}()

	var result goja.Value
	var thrown error
	settled := false
	finish := func(c goja.FunctionCall) goja.Value {
		result, settled = c.Argument(0), true
		return goja.Undefined()
	}
	fail := func(c goja.FunctionCall) goja.Value {
		thrown, settled = hubCodeError(vm, c.Argument(0)), true
		return goja.Undefined()
	}
	vm.Set("__magpieDone", finish)
	vm.Set("__magpieFail", fail)
	stopped := func(err error) any {
		stopMu.Lock()
		why := whyStopped
		stopMu.Unlock()
		if why == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			why = fmt.Errorf("it didn't finish in %s", hubCodeWait)
		}
		if why == nil {
			why = err
		}
		return hubCodeOut(logs.String(), logged, "The code was stopped: "+why.Error()+".", true)
	}
	prog, err := goja.Compile("code.js", "(async () => {\n"+code+"\n})().then(__magpieDone, __magpieFail);", false)
	if err != nil {
		return hubCodeOut("", 0, "The code doesn't parse: "+err.Error(), true)
	}
	if _, err := vm.RunProgram(prog); err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			return stopped(err)
		}
		if v := hubCodeThrown(err); v != nil {
			err = hubCodeError(vm, v)
		}
		return hubCodeOut(logs.String(), logged, "The code threw: "+err.Error(), true)
	}
	// the event loop: each finished call settles its promise here, on the
	// one goroutine the runtime is used from, until the script has settled
	// and no call it started is still running
	for pending > 0 || !settled {
		if pending == 0 {
			// settled can't come now: nothing is left to wake it
			return hubCodeOut(logs.String(), logged, "The code's promise never settled: await each tool call.", true)
		}
		var d hubCodeDone
		select {
		case d = <-done:
		case <-runCtx.Done():
			return stopped(runCtx.Err())
		}
		pending--
		var err error
		if d.err != nil {
			err = d.reject(vm.NewGoError(d.err))
		} else if v, failed := hubCodeValue(vm, d.res); failed {
			err = d.reject(v)
		} else {
			err = d.resolve(v)
		}
		if err != nil {
			return stopped(err)
		}
	}
	if thrown != nil {
		return hubCodeOut(logs.String(), logged, "The code threw: "+thrown.Error(), true)
	}
	return hubCodeResult(vm, logs.String(), logged, result)
}

// hubHeap is the bytes the Go heap's objects take now.
func hubHeap() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// hubCodeThrown is the value err, a script's exception, threw.
func hubCodeThrown(err error) goja.Value {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return ex.Value()
	}
	return nil
}

// hubCodeError is v, what a script threw, as an error: an Error's message,
// with the line it was thrown at; anything else as it shows.
func hubCodeError(vm *goja.Runtime, v goja.Value) error {
	if v == nil || goja.IsUndefined(v) {
		return errors.New("undefined")
	}
	if o, ok := v.(*goja.Object); ok {
		if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
			if s := strings.TrimSpace(st.String()); s != "" {
				return errors.New(s)
			}
		}
		if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
			return errors.New(m.String())
		}
	}
	return errors.New(hubCodeShow(vm, v))
}

// hubCodeShow is v as console.log shows it: a string as it is, an Error
// by its message, anything else as JSON when it has some.
func hubCodeShow(vm *goja.Runtime, v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if s, ok := v.Export().(string); ok {
		return s
	}
	if o, ok := v.(*goja.Object); ok && o.ClassName() == "Error" {
		return hubCodeError(vm, v).Error()
	}
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if s, err := stringify(goja.Undefined(), v); err == nil && !goja.IsUndefined(s) {
		return s.String()
	}
	return v.String()
}

// hubCodeValue is res, a tool's result, as the script is given it: its
// structuredContent, else its text (parsed when it is JSON; texts joined
// by a line), else its content as it is, for an image; failed for a
// result that is an error, then the Error to throw.
func hubCodeValue(vm *goja.Runtime, res any) (v goja.Value, failed bool) {
	b, err := json.Marshal(res)
	if err != nil {
		return vm.NewGoError(err), true
	}
	var r struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           bool              `json:"isError"`
	}
	json.Unmarshal(b, &r)
	var texts []string
	other := false
	for _, c := range r.Content {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		json.Unmarshal(c, &part)
		if part.Type == "text" {
			texts = append(texts, part.Text)
		} else {
			other = true
		}
	}
	text := strings.Join(texts, "\n")
	if r.IsError {
		if text == "" {
			text = "the tool failed"
		}
		return vm.NewGoError(errors.New(text)), true
	}
	parse := func(raw []byte) goja.Value {
		var x any
		if json.Unmarshal(raw, &x) != nil {
			return nil
		}
		parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
		v, err := parse(goja.Undefined(), vm.ToValue(string(raw)))
		if err != nil {
			return nil
		}
		return v
	}
	if len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		if v := parse(r.StructuredContent); v != nil {
			return v, false
		}
	}
	if other {
		content, _ := json.Marshal(r.Content)
		return parse(content), false
	}
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if v := parse([]byte(t)); v != nil {
			return v, false
		}
	}
	return vm.ToValue(text), false
}

// hubCodeResult is the tool result of what a script returned, v, after
// what it logged: content of MCP's as it is (an image a tool gave), a
// string as text, anything else as JSON.
func hubCodeResult(vm *goja.Runtime, logs string, logged int, v goja.Value) any {
	if v == nil || goja.IsUndefined(v) {
		if logs == "" {
			return hubCodeOut("", 0, "The code returned nothing and logged nothing: return what you need.", false)
		}
		return hubCodeOut(logs, logged, "", false)
	}
	if s, ok := v.Export().(string); ok {
		return hubCodeOut(logs, logged, s, false)
	}
	shown := hubCodeShow(vm, v)
	var parts []map[string]any
	if json.Unmarshal([]byte(shown), &parts) == nil && len(parts) > 0 && hubIsContent(parts) {
		out := []any{}
		if logs != "" {
			out = append(out, map[string]any{"type": "text", "text": hubCodeCut(logs, logged)})
		}
		for _, p := range parts {
			if p["type"] == "text" {
				s, _ := p["text"].(string)
				p["text"] = hubCodeCut(s, len(s))
			}
			out = append(out, p)
		}
		return map[string]any{"content": out}
	}
	if b := []byte(shown); json.Valid(b) {
		var x any
		json.Unmarshal(b, &x)
		if pretty, err := json.MarshalIndent(x, "", " "); err == nil && len(pretty) <= hubCodeMax {
			shown = string(pretty)
		}
	}
	return hubCodeOut(logs, logged, shown, false)
}

// hubIsContent is whether parts are MCP content blocks, each with a type
// MCP names.
func hubIsContent(parts []map[string]any) bool {
	for _, p := range parts {
		switch p["type"] {
		case "text", "image", "audio", "resource", "resource_link":
		default:
			return false
		}
	}
	return true
}

// hubCodeOut is a run_code result: what the script logged, then what it
// gave, each cut to hubCodeMax.
func hubCodeOut(logs string, logged int, said string, isErr bool) any {
	var b strings.Builder
	if logs != "" {
		b.WriteString(hubCodeCut(logs, logged))
		if said != "" && !strings.HasSuffix(logs, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(hubCodeCut(said, len(said)))
	return toolResult(b.String(), isErr)
}

// hubCodeCut is s cut to hubCodeMax bytes, saying how much more there
// was: n is s's whole length when only part of it was kept.
func hubCodeCut(s string, n int) string {
	if len(s) <= hubCodeMax && n <= len(s) {
		return s
	}
	kept := strings.ToValidUTF8(s[:min(len(s), hubCodeMax)], "")
	return kept + fmt.Sprintf("\n… cut: %d more characters. Return only what you need.", max(n-len(kept), 0))
}
