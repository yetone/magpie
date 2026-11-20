package gateway

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A tool parameter that is an object of any fields — {"type":"object"},
// no properties — has no form in the Schema object Google's function
// declarations take (`parameters`), and a Gemini model asked through
// one fills it with what it can't say: Hermes Agent's tool_call bridge,
// whose calls[].arguments is such an object, reached Hermes with the
// nested objects in it cut to strings ("boundary":"{strategy:") or made
// booleans ("workspace":true), through a relay's chat completions to
// gemini-4-hydrogen (#7363, @JNDliuliuM on X); Copilot's Gemini, which
// is given properties {} for it (copilot_gemini_shapes.go), answered
// every such call with "arguments":{} (probed live, gemini-4-hydrogen
// and gemini-4-argon). Whoever relays a Chat, Responses or Messages body
// to Gemini turns its schemas into that Schema object, so it is keyed on
// the model, not the provider, as geminiToolTypes is. Google's own
// Gemini API, Vertex AI and Code Assist are sent parametersJsonSchema,
// which takes JSON Schema whole, and their bodies aren't walked.
//
// Such a parameter, under the root, goes to a Gemini model as a string
// holding the object as JSON text, which the model writes whole (the same
// probe: every nested object kept), and the call's arguments reach the
// client with that string read back into the object at the same place.
// Only a string there that is a JSON object is read back: anything else
// the model wrote goes as it came. The root, an array's items and
// properties are walked; a union, a $ref and what is under them are left
// as they came. Every other model's request and reply go as they came.

// freeObjectHint is what a free object's description is told beside its
// own, so the model writes JSON text in the string.
const freeObjectHint = "A JSON object, written as JSON text."

// freeObject is whether schema s is an object of any fields: typed
// object, with no properties to fill, and more fields not ruled out.
func freeObject(s gjson.Result) bool {
	if !s.IsObject() || s.Get("type").String() != "object" || s.Get("type").Type != gjson.String {
		return false
	}
	if p := s.Get("properties"); p.Exists() && (!p.IsObject() || len(p.Map()) > 0) {
		return false
	}
	if s.Get("additionalProperties").Type == gjson.False {
		return false // no field at all: {} is all it can be
	}
	for _, k := range []string{"patternProperties", "anyOf", "oneOf", "allOf", `\$ref`, "enum", "const", "not", "if"} {
		if s.Get(k).Exists() {
			return false
		}
	}
	return true
}

// freeObjectSpot is a free object in a tool schema: where it is in the
// arguments (property names, "[]" for each of an array's items) and the
// node itself.
type freeObjectSpot struct {
	path []string
	node gjson.Result
}

// freeObjectSpots lists the free objects under a tool's root schema.
func freeObjectSpots(root gjson.Result) []freeObjectSpot {
	var out []freeObjectSpot
	var walk func(s gjson.Result, path []string, depth int)
	visit := func(s gjson.Result, path []string, depth int) {
		if freeObject(s) {
			out = append(out, freeObjectSpot{append([]string(nil), path...), s})
			return
		}
		walk(s, path, depth+1)
	}
	walk = func(s gjson.Result, path []string, depth int) {
		if !s.IsObject() || depth > 32 {
			return
		}
		s.Get("properties").ForEach(func(name, sub gjson.Result) bool {
			visit(sub, append(path, name.String()), depth)
			return true
		})
		if items := s.Get("items"); items.IsObject() {
			visit(items, append(path, "[]"), depth)
		}
	}
	walk(root, nil, 0)
	return out
}

// geminiFreeObjects is body with each free object under its tools' roots
// declared as a string of JSON text, for a Gemini model.
func geminiFreeObjects(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || tools.Index <= 0 {
		return body
	}
	type cut struct {
		from, to int
		raw      string
	}
	var cuts []cut
	tools.ForEach(func(_, tool gjson.Result) bool {
		for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
			root := tool.Get(at)
			if !root.IsObject() {
				continue
			}
			for _, sp := range freeObjectSpots(root) {
				n := sp.node
				if n.Index <= 0 || n.Index+len(n.Raw) > len(body) || string(body[n.Index:n.Index+len(n.Raw)]) != n.Raw {
					continue
				}
				desc := freeObjectHint
				if d := strings.TrimSpace(n.Get("description").String()); d != "" {
					desc = d + " (" + freeObjectHint + ")"
				}
				d, _ := json.Marshal(desc)
				raw := `{"type":"string","description":` + string(d)
				if t := n.Get("title"); t.Type == gjson.String {
					raw += `,"title":` + t.Raw
				}
				cuts = append(cuts, cut{n.Index, n.Index + len(n.Raw), raw + "}"})
			}
		}
		return true
	})
	if len(cuts) == 0 {
		return body
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].from < cuts[j].from })
	out := make([]byte, 0, len(body)+len(cuts)*48)
	last := 0
	for _, c := range cuts {
		if c.from < last {
			continue
		}
		out = append(out, body[last:c.from]...)
		out = append(out, c.raw...)
		last = c.to
	}
	return append(out, body[last:]...)
}

// freeObjectPaths are the free objects of each tool the client declared,
// by its name, for reading a Gemini model's calls back; nil when there
// are none.
func freeObjectPaths(tools []Tool) map[string][][]string {
	var out map[string][][]string
	for _, t := range tools {
		if len(t.Schema) == 0 {
			continue
		}
		for _, sp := range freeObjectSpots(gjson.ParseBytes(t.Schema)) {
			if out == nil {
				out = map[string][][]string{}
			}
			out[t.Name] = append(out[t.Name], sp.path)
		}
	}
	return out
}

// freeObjectPathsOf is freeObjectPaths for a request body as a client sent
// it, on any of the three APIs.
func freeObjectPathsOf(body []byte) map[string][][]string {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}
	var list []Tool
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("function.name")
		if !name.Exists() {
			name = tool.Get("name")
		}
		for _, at := range []string{"function.parameters", "parameters", "input_schema"} {
			if root := tool.Get(at); root.IsObject() && name.String() != "" {
				list = append(list, Tool{Name: name.String(), Schema: json.RawMessage(root.Raw)})
			}
		}
		return true
	})
	return freeObjectPaths(list)
}

// geminiServed is whether a request for model on p goes to a Gemini
// model, by magpie's id for it or the name it is sent upstream under.
func geminiServed(wires map[string]string, p provider.Provider, model string) bool {
	return geminiNamed(model) || geminiNamed(provider.UpstreamNameIn(wires, p.ID, model))
}

// freeArgs is a call's whole arguments with each string at one of paths
// that is a JSON object read back into that object. Everything else stays
// byte for byte.
func freeArgs(args string, paths [][]string) string {
	if len(paths) == 0 || !gjson.Valid(args) {
		return args
	}
	type rep struct {
		at, n int
		raw   string
	}
	var reps []rep
	var walk func(v gjson.Result, path []string)
	walk = func(v gjson.Result, path []string) {
		if len(path) == 0 {
			if v.Type != gjson.String {
				return
			}
			s := strings.TrimSpace(v.String())
			if strings.HasPrefix(s, "{") && gjson.Valid(s) {
				reps = append(reps, rep{v.Index, len(v.Raw), s})
			}
			return
		}
		seg := path[0]
		if seg == "[]" {
			if v.IsArray() {
				v.ForEach(func(_, e gjson.Result) bool {
					walk(e, path[1:])
					return true
				})
			}
			return
		}
		if v.IsObject() {
			v.ForEach(func(k, e gjson.Result) bool {
				if k.String() == seg {
					walk(e, path[1:])
				}
				return true
			})
		}
	}
	root := gjson.Parse(args)
	for _, p := range paths {
		walk(root, p)
	}
	if len(reps) == 0 {
		return args
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].at < reps[j].at })
	var b strings.Builder
	last := 0
	for _, r := range reps {
		if r.at < last || r.at+r.n > len(args) {
			continue
		}
		b.WriteString(args[last:r.at])
		b.WriteString(r.raw)
		last = r.at + r.n
	}
	b.WriteString(args[last:])
	return b.String()
}

// freeEncoder hands its encoder a Gemini model's reply with each call's
// free objects read back: the arguments of a call to a tool that has any
// are held until the call is whole, and said at once.
type freeEncoder struct {
	streamEncoder
	paths [][]string // of the call being held; nil when none is
	held  strings.Builder
	open  bool
	tools map[string][][]string
}

func (e *freeEncoder) event(ev Event) {
	if ev.Kind == KToolArgs && e.open {
		e.held.WriteString(ev.Text)
		return
	}
	e.release()
	if ev.Kind == KToolStart {
		if ps := e.tools[ev.Name]; len(ps) > 0 {
			e.paths, e.open = ps, true
		}
	}
	e.streamEncoder.event(ev)
}

func (e *freeEncoder) release() {
	if !e.open {
		return
	}
	e.open = false
	text := e.held.String()
	e.held.Reset()
	if text != "" {
		e.streamEncoder.event(Event{Kind: KToolArgs, Text: freeArgs(text, e.paths)})
	}
}

func (e *freeEncoder) finish() {
	e.release()
	e.streamEncoder.finish()
}

// freeResult is a Gemini model's whole reply with its calls' free objects
// read back.
func freeResult(res Result, tools map[string][][]string) Result {
	if len(tools) == 0 {
		return res
	}
	var parts []Part
	for i, p := range res.Parts {
		ps := tools[p.Name]
		if p.Kind != ToolCall || len(p.Args) == 0 || len(ps) == 0 {
			continue
		}
		if a := freeArgs(string(p.Args), ps); a != string(p.Args) {
			if parts == nil {
				parts = append([]Part(nil), res.Parts...)
			}
			parts[i].Args = json.RawMessage(a)
		}
	}
	if parts != nil {
		res.Parts = parts
	}
	return res
}

// freeTidy reads back, in a Gemini model's reply relayed as it came, the
// free objects of its calls: Chat's tool_calls, Responses' function_call
// items and argument deltas, and Messages' tool_use input. A streamed
// call to a tool with free objects is told with its argument pieces
// emptied, and its whole arguments, read back, in its last piece: Chat's
// at the chunk that ends its choice, Responses' and Messages' in the
// last delta, held until the event that ends the call. Every other event
// passes as it came; a reply that isn't a stream is held whole.
type freeTidy struct {
	proto provider.Protocol
	sse   bool
	tools map[string][][]string
	buf   []byte
	block []byte // a stream's event so far, to its blank line

	names map[string]string           // a streamed call's tool, by its key
	text  map[string]*strings.Builder // its arguments so far
	order []string                    // Chat's held calls, in the order they opened
	pend  map[string]*freeHeld        // Responses' and Messages' last delta, held
}

// freeHeld is a delta event held back, its data decoded.
type freeHeld struct {
	ev   map[string]any
	pre  []byte // the lines before its data
	post []byte // its data line's end, and the lines after it
}

func (t *freeTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	var out []byte
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := t.buf[:i+1]
		t.block = append(t.block, line...)
		t.buf = t.buf[i+1:]
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			out = append(out, t.event(t.block)...)
			t.block = t.block[:0]
		}
	}
	t.buf = append([]byte(nil), t.buf...)
	return out
}

func (t *freeTidy) flush() []byte {
	if !t.sse {
		b := t.buf
		t.buf = nil
		if out, ok := t.whole(b); ok {
			return out
		}
		return b
	}
	b := append(t.block, t.buf...)
	t.block, t.buf = nil, nil
	var out []byte
	if len(b) > 0 {
		out = t.event(b)
	}
	return append(out, t.drain()...)
}

// drain is what is still held at the end of a stream: Chat's calls in a
// chunk of their own, the last deltas with what they held so far.
func (t *freeTidy) drain() []byte {
	var out []byte
	if t.proto == provider.Chat && len(t.order) > 0 {
		chunk := map[string]any{"object": "chat.completion.chunk", "choices": t.chatHeld("", nil)}
		if nb, err := marshalPlain(chunk); err == nil {
			out = append(append(append(out, "data: "...), nb...), "\n\n"...)
		}
	}
	keys := make([]string, 0, len(t.pend))
	for k := range t.pend {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, t.releasePend(k, true)...)
	}
	return out
}

// freeDataOf is a stream event's data line: where it starts and ends in b,
// and what it holds.
func freeDataOf(b []byte) (from, to int, data []byte, ok bool) {
	at := 0
	for at < len(b) {
		end := bytes.IndexByte(b[at:], '\n')
		if end < 0 {
			end = len(b)
		} else {
			end += at + 1
		}
		line := bytes.TrimRight(b[at:end], "\r\n")
		if d, found := bytes.CutPrefix(line, []byte("data:")); found {
			return at, at + len(line), bytes.TrimSpace(d), true
		}
		at = end
	}
	return 0, 0, nil, false
}

// event is one stream event, as it goes on: as it came, rewritten, held
// (nothing), or with what was held before it.
func (t *freeTidy) event(b []byte) []byte {
	from, to, data, ok := freeDataOf(b)
	if !ok || len(data) == 0 || data[0] != '{' {
		if ok && string(data) == "[DONE]" && t.proto == provider.Chat && len(t.order) > 0 {
			return append(t.drain(), b...)
		}
		return append([]byte(nil), b...)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return append([]byte(nil), b...)
	}
	h := &freeHeld{ev: ev, pre: append([]byte(nil), b[:from]...), post: append([]byte(nil), b[to:]...)}
	switch t.proto {
	case provider.Chat:
		if !t.chat(ev) {
			return append([]byte(nil), b...)
		}
		return h.bytes()
	case provider.Responses:
		return t.responses(h, b)
	case provider.Anthropic:
		return t.anthropic(h, b)
	}
	return append([]byte(nil), b...)
}

func (h *freeHeld) bytes() []byte {
	nb, err := marshalPlain(h.ev)
	if err != nil {
		return nil
	}
	out := append([]byte(nil), h.pre...)
	out = append(append(out, "data: "...), nb...)
	return append(out, h.post...)
}

func (t *freeTidy) hold(key, name string) {
	if t.names == nil {
		t.names, t.text = map[string]string{}, map[string]*strings.Builder{}
	}
	if _, has := t.names[key]; !has {
		t.names[key] = name
		t.text[key] = &strings.Builder{}
		t.order = append(t.order, key)
	}
}

func (t *freeTidy) forget(key string) {
	delete(t.names, key)
	delete(t.text, key)
	for i, k := range t.order {
		if k == key {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
}

// chatHeld is the tool_calls deltas of choice ci's held calls ("" for
// every choice), their whole arguments read back, and forgets them; one
// choice's list when ci is given, else a list of choices.
func (t *freeTidy) chatHeld(ci string, into []any) []any {
	byChoice := map[string][]any{}
	var choices []string
	for _, key := range append([]string(nil), t.order...) {
		c, idx, _ := strings.Cut(key, "/")
		if ci != "" && c != ci {
			continue
		}
		args := freeArgs(t.text[key].String(), t.tools[t.names[key]])
		n, err := strconv.Atoi(idx)
		var index any = json.Number(idx)
		if err == nil {
			index = n
		}
		if _, seen := byChoice[c]; !seen {
			choices = append(choices, c)
		}
		byChoice[c] = append(byChoice[c], map[string]any{"index": index, "function": map[string]any{"arguments": args}})
		t.forget(key)
	}
	if ci != "" {
		return append(into, byChoice[ci]...)
	}
	var out []any
	for _, c := range choices {
		n, _ := strconv.Atoi(c)
		out = append(out, map[string]any{"index": n, "delta": map[string]any{"tool_calls": byChoice[c]}})
	}
	return out
}

func (t *freeTidy) chat(ev map[string]any) bool {
	changed := false
	choices, _ := ev["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		ci := jsonText(cm["index"])
		d, _ := cm["delta"].(map[string]any)
		if d != nil {
			calls, _ := d["tool_calls"].([]any)
			for j, tc := range calls {
				tm, _ := tc.(map[string]any)
				fn, _ := tm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				idx := jsonText(tm["index"])
				if tm["index"] == nil {
					idx = strconv.Itoa(j)
				}
				key := ci + "/" + idx
				if name, _ := fn["name"].(string); name != "" && len(t.tools[name]) > 0 {
					t.hold(key, name)
				}
				if _, held := t.names[key]; !held {
					continue
				}
				if s, ok := fn["arguments"].(string); ok && s != "" {
					t.text[key].WriteString(s)
					fn["arguments"] = ""
					changed = true
				}
			}
		}
		if fr, _ := cm["finish_reason"].(string); fr != "" && len(t.order) > 0 {
			held := t.chatHeld(ci, nil)
			if len(held) > 0 {
				if d == nil {
					d = map[string]any{}
					cm["delta"] = d
				}
				calls, _ := d["tool_calls"].([]any)
				d["tool_calls"] = append(calls, held...)
				changed = true
			}
		}
		// a whole reply's message
		if m, _ := cm["message"].(map[string]any); m != nil {
			calls, _ := m["tool_calls"].([]any)
			for _, tc := range calls {
				tm, _ := tc.(map[string]any)
				fn, _ := tm["function"].(map[string]any)
				name, _ := fn["name"].(string)
				if ps := t.tools[name]; fn != nil && len(ps) > 0 && set(fn, "arguments", func(s string) string { return freeArgs(s, ps) }) {
					changed = true
				}
			}
		}
	}
	return changed
}

// releasePend is the delta held for key, told the call's whole arguments
// read back when last, else an empty piece.
func (t *freeTidy) releasePend(key string, last bool) []byte {
	h := t.pend[key]
	if h == nil {
		return nil
	}
	delete(t.pend, key)
	text := ""
	if last {
		text = freeArgs(t.text[key].String(), t.tools[t.names[key]])
	}
	field := "delta"
	if t.proto == provider.Anthropic {
		if d, _ := h.ev["delta"].(map[string]any); d != nil {
			d["partial_json"] = text
		}
	} else {
		h.ev[field] = text
	}
	return h.bytes()
}

func (t *freeTidy) holdDelta(key string, h *freeHeld) []byte {
	out := t.releasePend(key, false)
	if t.pend == nil {
		t.pend = map[string]*freeHeld{}
	}
	t.pend[key] = h
	return out
}

func (t *freeTidy) responses(h *freeHeld, b []byte) []byte {
	ev := h.ev
	changed := false
	item := func(it map[string]any) {
		if it == nil || it["type"] != "function_call" {
			return
		}
		name, _ := it["name"].(string)
		if ps := t.tools[name]; len(ps) > 0 && set(it, "arguments", func(s string) string { return freeArgs(s, ps) }) {
			changed = true
		}
	}
	var pre []byte
	switch ev["type"] {
	case "response.output_item.added":
		if it, _ := ev["item"].(map[string]any); it != nil && it["type"] == "function_call" {
			if name, _ := it["name"].(string); len(t.tools[name]) > 0 {
				if id, _ := it["id"].(string); id != "" {
					t.hold(id, name)
				}
				t.hold("#"+jsonText(ev["output_index"]), name)
			}
		}
	case "response.function_call_arguments.delta":
		key := t.respKey(ev)
		if key == "" {
			break
		}
		s, _ := ev["delta"].(string)
		t.text[key].WriteString(s)
		return t.holdDelta(key, h)
	case "response.function_call_arguments.done":
		key := t.respKey(ev)
		if key == "" {
			break
		}
		if s, ok := ev["arguments"].(string); ok {
			// the whole text, as the upstream says it
			t.text[key].Reset()
			t.text[key].WriteString(s)
		}
		pre = t.releasePend(key, true)
		ev["arguments"] = freeArgs(t.text[key].String(), t.tools[t.names[key]])
		t.forgetResp(ev)
		changed = true
	}
	it, _ := ev["item"].(map[string]any)
	item(it)
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, o := range out {
			im, _ := o.(map[string]any)
			item(im)
		}
	}
	if !changed {
		return append(pre, b...)
	}
	return append(pre, h.bytes()...)
}

// respKey is the key a Responses argument event's held call is under, ""
// when it isn't held.
func (t *freeTidy) respKey(ev map[string]any) string {
	if id, _ := ev["item_id"].(string); id != "" {
		if _, held := t.names[id]; held {
			return id
		}
	}
	if k := "#" + jsonText(ev["output_index"]); t.names[k] != "" {
		return k
	}
	return ""
}

func (t *freeTidy) forgetResp(ev map[string]any) {
	id, _ := ev["item_id"].(string)
	k := "#" + jsonText(ev["output_index"])
	// both keys share one call's text: the one written to is the one kept
	for _, key := range []string{id, k} {
		if key != "" {
			t.forget(key)
		}
	}
}

func (t *freeTidy) anthropic(h *freeHeld, b []byte) []byte {
	ev := h.ev
	changed := false
	block := func(bl map[string]any) {
		if bl == nil || bl["type"] != "tool_use" || bl["input"] == nil {
			return
		}
		name, _ := bl["name"].(string)
		ps := t.tools[name]
		if len(ps) == 0 {
			return
		}
		in, err := marshalPlain(bl["input"])
		if err != nil {
			return
		}
		if out := freeArgs(string(in), ps); out != string(in) {
			bl["input"] = json.RawMessage(out)
			changed = true
		}
	}
	content, _ := ev["content"].([]any)
	for _, c := range content {
		cm, _ := c.(map[string]any)
		block(cm)
	}
	key := jsonText(ev["index"])
	var pre []byte
	switch ev["type"] {
	case "content_block_start":
		cb, _ := ev["content_block"].(map[string]any)
		block(cb)
		if cb != nil && cb["type"] == "tool_use" {
			if name, _ := cb["name"].(string); len(t.tools[name]) > 0 {
				t.hold(key, name)
			}
		}
	case "content_block_delta":
		d, _ := ev["delta"].(map[string]any)
		if _, held := t.names[key]; held && d != nil && d["type"] == "input_json_delta" {
			s, _ := d["partial_json"].(string)
			t.text[key].WriteString(s)
			return t.holdDelta(key, h)
		}
	case "content_block_stop":
		if _, held := t.names[key]; held {
			pre = t.releasePend(key, true)
			t.forget(key)
		}
	}
	if !changed {
		return append(pre, b...)
	}
	return append(pre, h.bytes()...)
}

// whole is a reply that came whole with its calls' free objects read
// back, and whether that changed it.
func (t *freeTidy) whole(b []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	h := &freeHeld{ev: ev}
	var out []byte
	switch t.proto {
	case provider.Chat:
		if !t.chat(ev) {
			return nil, false
		}
		out, _ = marshalPlain(ev)
	case provider.Responses:
		out = t.responses(h, nil)
	case provider.Anthropic:
		out = t.anthropic(h, nil)
	}
	if len(out) == 0 {
		return nil, false
	}
	if t.proto != provider.Chat {
		// h.bytes() said it as an event's data line
		out = bytes.TrimPrefix(out, []byte("data: "))
	}
	return out, true
}
