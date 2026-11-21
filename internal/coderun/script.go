// Package coderun runs the MCP router's code mode scripts (run_code) in a
// process of their own: magpie started again as `magpie run-code-helper`,
// which the gateway gives the script and serves its tool calls to.
//
// The script runs in goja, a JavaScript interpreter with nothing of the
// computer's. goja stops a script that is interrupted only between its own
// steps: a builtin is Go, and runs to its end however long it takes or
// however much it allocates. "x".repeat(1e10) asks for 10 GB at once, a
// regular expression with a backreference can backtrack for ever, and
// [].join or fill over an array of length 4e9 loops 4e9 times. Run in
// magpie's own process, such a script took magpie's memory or a core with
// it, and the request waited on it past its time. A process of its own is
// ended whatever it is doing: at its time, by the gateway, and past its
// memory, by itself.
package coderun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/yetone/magpie/internal/lazyre"
)

// Limits are what one script may do.
type Limits struct {
	// Calls is how many tool calls it makes at most, AtOnce how many run
	// at the same time.
	Calls  int `json:"calls"`
	AtOnce int `json:"atOnce"`
	// Max is how many characters of what it returns and logs go back.
	Max int `json:"max"`
	// Heap is how far its process's heap may grow before it is ended.
	Heap uint64 `json:"heap"`
	// Wait is how long it runs in all, its calls included.
	Wait time.Duration `json:"wait"`
}

// Script is one script to run: code, the body of an async function, with
// Args as its `args`, Tools the names on `tools` and Skills the functions
// on `skills`.
type Script struct {
	Code   string          `json:"code"`
	Args   json.RawMessage `json:"args,omitempty"`
	Tools  []string        `json:"tools"`
	Skills []Skill         `json:"skills,omitempty"`
}

// Skill is a skill with code: its code.js, whose default export is an
// async function of one object of args. A script calls it as
// skills.<name>(args) (- as _), and its code calls the same tools.
type Skill struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// exportDefault finds a code.js's `export default`, at the start of a
// line.
var exportDefault = lazyre.New(`(?m)^[ \t]*export[ \t]+default\b[ \t]*`)

// SkillSource is code, a skill's code.js, as an expression whose value is
// its default export: the file run as a function's body, with its
// `export default` made that function's return. It is an error when the
// file has no `export default`, or another import or export.
func SkillSource(code string) (string, error) {
	loc := exportDefault.FindStringIndex(code)
	if loc == nil {
		return "", errors.New("its code.js has no `export default async (args) => { … }`")
	}
	if other.MatchString(code[:loc[0]]) || other.MatchString(code[loc[1]:]) {
		return "", errors.New("its code.js imports or exports more than its default: it runs with nothing but tools and skills")
	}
	// const, not return: functions declared after it are hoisted to it
	return "(function () {\n" + code[:loc[0]] + "const __magpieDefault = " + code[loc[1]:] + "\n;return __magpieDefault;\n})()", nil
}

// other is an import or export statement at a line's start.
var other = lazyre.New(`(?m)^[ \t]*(import|export)\b[ \t]*[{*\w'"]`)

// Call runs one tool by name for a script: its result as the tool gave
// it, or an error when no tool has that name.
type Call func(ctx context.Context, name string, args json.RawMessage) (any, error)

// Result is a tool result of text.
func Result(text string, isErr bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}
}

// done is one tool call of a script, finished.
type done struct {
	res             any
	err             error
	resolve, reject func(any) error
}

// script runs sc's code, the body of an async function, with sc's tools
// as the functions of `tools`, each run by call, and its skills as those
// of `skills`, and is the tool result of what it returned and logged. It
// runs in this process: only the helper calls it.
func script(ctx context.Context, sc Script, call Call, lim Limits) any {
	code, names := sc.Code, sc.Tools
	vm := goja.New()
	vm.SetMaxCallStackSize(2000)
	var logs strings.Builder
	logged := 0
	log := func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, a := range c.Arguments {
			parts[i] = show(vm, a)
		}
		line := strings.Join(parts, " ") + "\n"
		if logged < lim.Max {
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

	finished := make(chan done)
	slots := make(chan struct{}, max(lim.AtOnce, 1))
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
			if calls++; calls > lim.Calls {
				reject(vm.NewGoError(fmt.Errorf("more than %d tool calls in one script", lim.Calls)))
				return vm.ToValue(p)
			}
			pending++
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := done{resolve: resolve, reject: reject}
				select {
				case slots <- struct{}{}:
					d.res, d.err = call(runCtx, name, args)
					<-slots
				case <-runCtx.Done():
					d.err = runCtx.Err()
				}
				select {
				case finished <- d:
				case <-runCtx.Done():
				}
			}()
			return vm.ToValue(p)
		}
	}
	tools := vm.NewObject()
	for _, name := range names {
		f := tool(name)
		tools.Set(name, f)
		if alt := strings.ReplaceAll(name, "-", "_"); alt != name && tools.Get(alt) == nil {
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
		return Result("code mode couldn't start: "+err.Error(), true)
	}
	if err := setSkills(vm, sc.Skills); err != nil {
		return Result("code mode couldn't start: "+err.Error(), true)
	}
	if err := setArgs(vm, sc.Args); err != nil {
		return Result("args isn't a JSON object: "+err.Error(), true)
	}

	// stopped: the time is up. A builtin running is past an interrupt's
	// reach; the gateway ends this process when it doesn't stop.
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
		select {
		case <-watch:
		case <-ctx.Done():
			halt(fmt.Errorf("it didn't finish in %s", lim.Wait))
		}
	}()

	var result goja.Value
	var thrown error
	settled := false
	vm.Set("__magpieDone", func(c goja.FunctionCall) goja.Value {
		result, settled = c.Argument(0), true
		return goja.Undefined()
	})
	vm.Set("__magpieFail", func(c goja.FunctionCall) goja.Value {
		thrown, settled = scriptError(vm, c.Argument(0)), true
		return goja.Undefined()
	})
	stopped := func(err error) any {
		stopMu.Lock()
		why := whyStopped
		stopMu.Unlock()
		if why == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			why = fmt.Errorf("it didn't finish in %s", lim.Wait)
		}
		if why == nil {
			why = err
		}
		return out(logs.String(), logged, "The code was stopped: "+why.Error()+".", true, lim.Max)
	}
	prog, err := goja.Compile("code.js", "(async () => {\n"+code+"\n})().then(__magpieDone, __magpieFail);", false)
	if err != nil {
		return out("", 0, "The code doesn't parse: "+err.Error(), true, lim.Max)
	}
	if _, err := vm.RunProgram(prog); err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			return stopped(err)
		}
		if v := thrownValue(err); v != nil {
			err = scriptError(vm, v)
		}
		return out(logs.String(), logged, "The code threw: "+err.Error(), true, lim.Max)
	}
	// the event loop: each finished call settles its promise here, on the
	// one goroutine the runtime is used from, until the script has settled
	// and no call it started is still running
	for pending > 0 || !settled {
		if pending == 0 {
			// settled can't come now: nothing is left to wake it
			return out(logs.String(), logged, "The code's promise never settled: await each tool call.", true, lim.Max)
		}
		var d done
		select {
		case d = <-finished:
		case <-runCtx.Done():
			return stopped(runCtx.Err())
		}
		pending--
		var err error
		if d.err != nil {
			err = d.reject(vm.NewGoError(d.err))
		} else if v, failed := value(vm, d.res); failed {
			err = d.reject(v)
		} else {
			err = d.resolve(v)
		}
		if err != nil {
			return stopped(err)
		}
	}
	if thrown != nil {
		return out(logs.String(), logged, "The code threw: "+thrown.Error(), true, lim.Max)
	}
	return returned(vm, logs.String(), logged, result, lim.Max)
}

// thrownValue is the value err, a script's exception, threw.
func thrownValue(err error) goja.Value {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return ex.Value()
	}
	return nil
}

// scriptError is v, what a script threw, as an error: an Error's message,
// with the line it was thrown at; anything else as it shows.
func scriptError(vm *goja.Runtime, v goja.Value) error {
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
	return errors.New(show(vm, v))
}

// show is v as console.log shows it: a string as it is, an Error by its
// message, anything else as JSON when it has some.
func show(vm *goja.Runtime, v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if s, ok := v.Export().(string); ok {
		return s
	}
	if o, ok := v.(*goja.Object); ok && o.ClassName() == "Error" {
		return scriptError(vm, v).Error()
	}
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if s, err := stringify(goja.Undefined(), v); err == nil && !goja.IsUndefined(s) {
		return s.String()
	}
	return v.String()
}

// value is res, a tool's result, as the script is given it: its
// structuredContent, else its text (parsed when it is JSON; texts joined
// by a line), else its content as it is, for an image; failed for a
// result that is an error, then the Error to throw.
func value(vm *goja.Runtime, res any) (v goja.Value, failed bool) {
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

// returned is the tool result of what a script returned, v, after what it
// logged: content of MCP's as it is (an image a tool gave), a string as
// text, anything else as JSON.
func returned(vm *goja.Runtime, logs string, logged int, v goja.Value, most int) any {
	if v == nil || goja.IsUndefined(v) {
		if logs == "" {
			return out("", 0, "The code returned nothing and logged nothing: return what you need.", false, most)
		}
		return out(logs, logged, "", false, most)
	}
	if s, ok := v.Export().(string); ok {
		return out(logs, logged, s, false, most)
	}
	shown := show(vm, v)
	var parts []map[string]any
	if json.Unmarshal([]byte(shown), &parts) == nil && len(parts) > 0 && isContent(parts) {
		res := []any{}
		if logs != "" {
			res = append(res, map[string]any{"type": "text", "text": cut(logs, logged, most)})
		}
		for _, p := range parts {
			if p["type"] == "text" {
				s, _ := p["text"].(string)
				p["text"] = cut(s, len(s), most)
			}
			res = append(res, p)
		}
		return map[string]any{"content": res}
	}
	if b := []byte(shown); json.Valid(b) {
		var x any
		json.Unmarshal(b, &x)
		if pretty, err := json.MarshalIndent(x, "", " "); err == nil && len(pretty) <= most {
			shown = string(pretty)
		}
	}
	return out(logs, logged, shown, false, most)
}

// isContent is whether parts are MCP content blocks, each with a type MCP
// names.
func isContent(parts []map[string]any) bool {
	for _, p := range parts {
		switch p["type"] {
		case "text", "image", "audio", "resource", "resource_link":
		default:
			return false
		}
	}
	return true
}

// out is a run_code result: what the script logged, then what it gave,
// each cut to most.
func out(logs string, logged int, said string, isErr bool, most int) any {
	var b strings.Builder
	if logs != "" {
		b.WriteString(cut(logs, logged, most))
		if said != "" && !strings.HasSuffix(logs, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(cut(said, len(said), most))
	return Result(b.String(), isErr)
}

// cut is s cut to most bytes, saying how much more there was: n is s's
// whole length when only part of it was kept.
func cut(s string, n, most int) string {
	if len(s) <= most && n <= len(s) {
		return s
	}
	kept := strings.ToValidUTF8(s[:min(len(s), most)], "")
	return kept + fmt.Sprintf("\n… cut: %d more characters. Return only what you need.", max(n-len(kept), 0))
}

// setSkills puts skills on vm's `skills`, by name and with - as _. A
// skill's code.js runs the first time it is called, by an indirect eval:
// in the global scope, with tools and skills. One that can't be run
// throws when it is called, saying why, and a name no skill has throws
// too.
func setSkills(vm *goja.Runtime, skills []Skill) error {
	srcs := map[string]string{}
	keys := vm.NewObject()
	for _, k := range skills {
		src, err := SkillSource(k.Code)
		if err != nil {
			q, _ := json.Marshal("the skill " + k.Name + " can't be run: " + err.Error())
			src = "(() => { throw new Error(" + string(q) + ") })"
		}
		srcs[k.Name] = src
		keys.Set(k.Name, k.Name)
		if alt := strings.ReplaceAll(k.Name, "-", "_"); alt != k.Name && keys.Get(alt) == nil {
			keys.Set(alt, k.Name)
		}
	}
	vm.Set("__magpieSkillKeys", keys)
	vm.Set("__magpieSkillSrc", func(c goja.FunctionCall) goja.Value { return vm.ToValue(srcs[c.Argument(0).String()]) })
	_, err := vm.RunString(`(() => {
	const src = __magpieSkillSrc, made = {}, skills = {};
	for (const [k, n] of Object.entries(__magpieSkillKeys)) {
		skills[k] = async (a) => {
			if (!(n in made)) made[n] = (0, eval)(src(n));
			if (typeof made[n] !== "function") throw new Error("the skill " + n + "'s code.js doesn't export a function by default");
			return await made[n](a === undefined || a === null ? {} : a);
		};
	}
	globalThis.skills = new Proxy(skills, {get(o, k) {
		if (typeof k !== "string" || k in o || k === "then" || k === "toJSON") return o[k];
		return async () => { throw new Error("no skill with code is called " + k) };
	}});
	delete globalThis.__magpieSkillKeys; delete globalThis.__magpieSkillSrc;
})();`)
	return err
}

// setArgs puts args, a JSON object, on vm's `args`: an empty object when
// there is none.
func setArgs(vm *goja.Runtime, args json.RawMessage) error {
	if len(args) == 0 || string(args) == "null" {
		vm.Set("args", vm.NewObject())
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return err
	}
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	v, err := parse(goja.Undefined(), vm.ToValue(string(args)))
	if err != nil {
		return err
	}
	vm.Set("args", v)
	return nil
}
