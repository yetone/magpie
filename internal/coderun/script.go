// Package coderun runs the MCP router's code mode scripts (run_code) in a
// process of their own: magpie started again as `magpie run-code-helper`,
// which the gateway gives the script and serves its tool calls to.
//
// The script runs in moejs, the JavaScript interpreter magpie's middleware
// and agent plugins run in, with nothing of the computer's. It is
// interrupted at its time and stopped past its memory limit inside a
// builtin too, but a builtin is Go: one that misses a check runs to its
// end however long it takes or however much it allocates, and in magpie's
// own process such a script took magpie's memory or a core with it, and
// the request waited on it past its time. A process of its own is ended
// whatever it is doing: at its time, by the gateway, and past its memory,
// by itself.
package coderun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moejs"

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
	resolve, reject func(moejs.Value) error
}

// wrapped is code as the script runs it: the body of an async function
// whose end settles the run.
func wrapped(code string) string {
	return "(async () => {\n" + code + "\n})().then(__magpieDone, __magpieFail);"
}

// script runs sc's code, the body of an async function, with sc's tools
// as the functions of `tools`, each run by call, and its skills as those
// of `skills`, and is the tool result of what it returned and logged. It
// runs in this process: only the helper calls it.
func script(ctx context.Context, sc Script, call Call, lim Limits) any {
	// its own builtins, which a script may change as it likes, and no cap
	// on a skill's code.js beyond the script's memory
	vm := moejs.NewRuntime(moejs.Options{MutableIntrinsics: true, MaxDynamicSource: -1, MemoryLimit: int64(min(lim.Heap, 1<<62))})
	var logs strings.Builder
	logged := 0
	var shown func(moejs.Value) string
	log := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = shown(a)
		}
		line := strings.Join(parts, " ") + "\n"
		if logged < lim.Max {
			logs.WriteString(line)
		}
		logged += len(line)
		return moejs.Undefined(), nil
	})
	console := map[string]any{}
	for _, k := range []string{"log", "info", "warn", "error", "debug"} {
		console[k] = log
	}
	start := func(err error) any { return Result("code mode couldn't start: "+err.Error(), true) }
	if err := vm.SetGlobal("console", console); err != nil {
		return start(err)
	}
	str, err := global(vm, "String")
	if err != nil {
		return start(err)
	}
	shown = func(v moejs.Value) string { return show(vm, str, v) }

	finished := make(chan done)
	slots := make(chan struct{}, max(lim.AtOnce, 1))
	pending, calls := 0, 0
	var wg sync.WaitGroup
	defer wg.Wait()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	tool := func(name string) moejs.NativeFunc {
		return func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			args := json.RawMessage(`{}`)
			if v := moejs.Arg(a, 0); !v.IsNullish() {
				b, err := vm.AppendJSON(nil, v)
				if err != nil {
					return moejs.Undefined(), err
				}
				if string(b) != "null" {
					args = b
				}
			}
			p, resolve, reject := vm.NewPromise()
			if calls++; calls > lim.Calls {
				if v, ok := vm.ThrownValue(fmt.Errorf("more than %d tool calls in one script", lim.Calls)); ok {
					reject(v)
				}
				return p, nil
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
			return p, nil
		}
	}
	tools := map[string]any{}
	for _, name := range sc.Tools {
		f := tool(name)
		tools[name] = f
		if alt := strings.ReplaceAll(name, "-", "_"); alt != name && tools[alt] == nil {
			tools[alt] = f
		}
	}
	// a name no tool listed is still asked for, as call_tool asks: a
	// server's list can change after it was listed
	asked := moejs.NativeFunc(func(r *moejs.Realm, this moejs.Value, a []moejs.Value) (moejs.Value, error) {
		return tool(shown(moejs.Arg(a, 0)))(r, this, a[min(1, len(a)):])
	})
	if err := vm.SetGlobal("__magpieTool", asked); err != nil {
		return start(err)
	}
	if err := vm.SetGlobal("__magpieTools", tools); err != nil {
		return start(err)
	}
	if err := run(vm, "tools.js", `globalThis.tools = new Proxy(__magpieTools, {get(o, k) {
		if (typeof k !== "string" || k in o || k === "then" || k === "toJSON") return o[k];
		return (args) => __magpieTool(k, args);
	}}); delete globalThis.__magpieTools;`); err != nil {
		return start(err)
	}
	if err := setSkills(vm, sc.Skills); err != nil {
		return start(err)
	}
	if err := setArgs(vm, sc.Args); err != nil {
		return Result("args isn't a JSON object: "+err.Error(), true)
	}

	// stopped: the time is up, or the memory. A builtin that misses the
	// interrupt is past its reach; the gateway ends this process when it
	// doesn't stop.
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

	var result moejs.Value
	var thrown error
	settled := false
	if err := vm.SetGlobal("__magpieDone", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
		result, settled = moejs.Arg(a, 0), true
		return moejs.Undefined(), nil
	})); err != nil {
		return start(err)
	}
	if err := vm.SetGlobal("__magpieFail", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
		thrown, settled = scriptError(vm, str, moejs.Arg(a, 0)), true
		return moejs.Undefined(), nil
	})); err != nil {
		return start(err)
	}
	stopped := func(err error) any {
		stopMu.Lock()
		why := whyStopped
		stopMu.Unlock()
		if why == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			why = fmt.Errorf("it didn't finish in %s", lim.Wait)
		}
		if why == nil && errors.Is(err, moejs.ErrMemoryLimit) {
			why = fmt.Errorf("it used more than %d MB of memory", lim.Heap>>20)
		}
		if why == nil {
			why = err
		}
		return out(logs.String(), logged, "The code was stopped: "+why.Error()+".", true, lim.Max)
	}
	// failed is the result of err, what running the script or settling
	// one of its promises returned.
	failed := func(err error) any {
		var ie *moejs.InterruptedError
		if errors.As(err, &ie) {
			return stopped(err)
		}
		var ex *moejs.Exception
		if errors.As(err, &ex) {
			err = scriptError(vm, str, ex.Value)
		}
		return out(logs.String(), logged, "The code threw: "+err.Error(), true, lim.Max)
	}
	prog, err := moejs.CompileScript("code.js", wrapped(sc.Code))
	if err != nil {
		return out("", 0, "The code doesn't parse: "+err.Error(), true, lim.Max)
	}
	if _, err := vm.RunScript(prog); err != nil {
		return failed(err)
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
			err = rejectWith(vm, d.reject, d.err)
		} else if v, bad := value(vm, d.res); bad != nil {
			err = rejectWith(vm, d.reject, bad)
		} else {
			err = d.resolve(v)
		}
		if err != nil {
			return failed(err)
		}
	}
	if thrown != nil {
		return out(logs.String(), logged, "The code threw: "+thrown.Error(), true, lim.Max)
	}
	return returned(vm, str, logs.String(), logged, result, lim.Max)
}

// rejectWith rejects a promise with err as the Error a script catches.
func rejectWith(vm *moejs.Runtime, reject func(moejs.Value) error, err error) error {
	v, ok := vm.ThrownValue(err)
	if !ok {
		return err
	}
	return reject(v)
}

// run runs src, a script of the runtime's own, in vm.
func run(vm *moejs.Runtime, name, src string) error {
	prog, err := moejs.CompileScript(name, src)
	if err != nil {
		return err
	}
	_, err = vm.RunScript(prog)
	return err
}

// global is the value of the global name in vm.
func global(vm *moejs.Runtime, name string) (moejs.Value, error) {
	prog, err := moejs.CompileScript(name+".js", name)
	if err != nil {
		return moejs.Undefined(), err
	}
	return vm.RunScript(prog)
}

// text is v as String(v) is, str being String: as it shows itself when
// that throws.
func text(vm *moejs.Runtime, str, v moejs.Value) string {
	if v.IsString() {
		return v.String()
	}
	if s, err := vm.CallFunction(str, moejs.Undefined(), v); err == nil && s.IsString() {
		return s.String()
	}
	return v.String()
}

// isError is whether v is an Error: an object with an Error's stack.
func isError(vm *moejs.Runtime, v moejs.Value) bool {
	return v.IsObject() && vm.StackTrace(&moejs.Exception{Value: v}) != ""
}

// scriptError is v, what a script threw, as an error: an Error's message,
// with the line it was thrown at; anything else as it shows.
func scriptError(vm *moejs.Runtime, str, v moejs.Value) error {
	if v.IsUndefined() {
		return errors.New("undefined")
	}
	if v.IsObject() {
		if st, err := vm.Get(v, "stack"); err == nil && st.IsString() {
			if s := strings.TrimSpace(st.String()); s != "" {
				return errors.New(s)
			}
		}
		if m, err := vm.Get(v, "message"); err == nil && !m.IsUndefined() {
			return errors.New(text(vm, str, m))
		}
	}
	return errors.New(show(vm, str, v))
}

// show is v as console.log shows it: a string as it is, an Error by its
// message, anything else as JSON when it has some.
func show(vm *moejs.Runtime, str, v moejs.Value) string {
	switch {
	case v.IsUndefined():
		return "undefined"
	case v.IsString():
		return v.String()
	case isError(vm, v):
		return scriptError(vm, str, v).Error()
	}
	// a function or a symbol has no JSON, and is written as null
	if b, err := vm.AppendJSON(nil, v); err == nil && (string(b) != "null" || v.IsNull()) {
		return string(b)
	}
	return text(vm, str, v)
}

// value is res, a tool's result, as the script is given it: its
// structuredContent, else its text (parsed when it is JSON; texts joined
// by a line), else its content as it is, for an image; failed for a
// result that is an error, then the error to throw.
func value(vm *moejs.Runtime, res any) (v moejs.Value, failed error) {
	b, err := json.Marshal(res)
	if err != nil {
		return moejs.Undefined(), err
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
		return moejs.Undefined(), errors.New(text)
	}
	parse := func(raw []byte) (moejs.Value, bool) {
		if !json.Valid(raw) {
			return moejs.Undefined(), false
		}
		v, err := vm.ParseJSON(raw)
		return v, err == nil
	}
	if len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		if v, ok := parse(r.StructuredContent); ok {
			return v, nil
		}
	}
	if other {
		content, _ := json.Marshal(r.Content)
		v, _ := parse(content)
		return v, nil
	}
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if v, ok := parse([]byte(t)); ok {
			return v, nil
		}
	}
	return moejs.String(text), nil
}

// returned is the tool result of what a script returned, v, after what it
// logged: content of MCP's as it is (an image a tool gave), a string as
// text, anything else as JSON.
func returned(vm *moejs.Runtime, str moejs.Value, logs string, logged int, v moejs.Value, most int) any {
	if v.IsUndefined() {
		if logs == "" {
			return out("", 0, "The code returned nothing and logged nothing: return what you need.", false, most)
		}
		return out(logs, logged, "", false, most)
	}
	if v.IsString() {
		return out(logs, logged, v.String(), false, most)
	}
	shown := show(vm, str, v)
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
func setSkills(vm *moejs.Runtime, skills []Skill) error {
	srcs := map[string]string{}
	keys := map[string]any{}
	for _, k := range skills {
		src, err := SkillSource(k.Code)
		if err != nil {
			q, _ := json.Marshal("the skill " + k.Name + " can't be run: " + err.Error())
			src = "(() => { throw new Error(" + string(q) + ") })"
		}
		srcs[k.Name] = src
		keys[k.Name] = k.Name
		if alt := strings.ReplaceAll(k.Name, "-", "_"); alt != k.Name && keys[alt] == nil {
			keys[alt] = k.Name
		}
	}
	if err := vm.SetGlobal("__magpieSkillKeys", keys); err != nil {
		return err
	}
	if err := vm.SetGlobal("__magpieSkillSrc", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
		n := moejs.Arg(a, 0)
		if !n.IsString() {
			return moejs.Undefined(), nil
		}
		return moejs.String(srcs[n.String()]), nil
	})); err != nil {
		return err
	}
	return run(vm, "skills.js", `(() => {
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
}

// setArgs puts args, a JSON object, on vm's `args`: an empty object when
// there is none.
func setArgs(vm *moejs.Runtime, args json.RawMessage) error {
	if len(args) == 0 || string(args) == "null" {
		return vm.SetGlobal("args", map[string]any{})
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return err
	}
	v, err := vm.ParseJSON(args)
	if err != nil {
		return err
	}
	return vm.SetGlobal("args", v)
}
