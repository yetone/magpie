package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func codeAssistRequest(t *testing.T) *Request {
	t.Helper()
	r, err := parseAnthropic([]byte(`{"model":"x","max_tokens":2000,"system":"be brief","stream":true,
		"thinking":{"type":"enabled","budget_tokens":4000},
		"tools":[{"name":"read","description":"read a file","input_schema":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object",
			"properties":{"path":{"type":["string","null"],"format":"uri"},"mode":{"const":"r"},"opts":{"$ref":"#/$defs/Opts"},
			"n":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["path","gone"],"additionalProperties":false,
			"$defs":{"Opts":{"type":"object","properties":{"deep":{"type":"boolean","default":false}}}}}}],
		"messages":[
			{"role":"user","content":"read a"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"sig-from-claude"},
				{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_01:x","name":"read","input":{"path":"a"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01:x","content":"A!"}]},
			{"role":"user","content":"and?"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildCodeAssistForGemini(t *testing.T) {
	var env struct {
		Model   string         `json:"model"`
		Request map[string]any `json:"request"`
	}
	json.Unmarshal(buildCodeAssist(codeAssistRequest(t), "gemini-2.5-pro", "gemini"), &env)
	req := env.Request
	if env.Model != "gemini-2.5-pro" {
		t.Errorf("model %q", env.Model)
	}
	b, _ := json.Marshal(req)
	s := string(b)
	for _, want := range []string{
		`"systemInstruction":{"parts":[{"text":"be brief"}],"role":"user"}`,
		`"functionCall":{"args":{"path":"a"},"id":"toolu_01:x","name":"read"},"thoughtSignature":"skip_thought_signature_validator"`,
		`"functionResponse":{"id":"toolu_01:x","name":"read","response":{"output":"A!"}}`,
		`"parametersJsonSchema":{`,
		`"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}`,
		`"maxOutputTokens":2000`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Contains(s, "sig-from-claude") || strings.Contains(s, `"hm"`) {
		t.Error("thinking was sent back")
	}
	// the tool result and the next question are one user turn
	contents := req["contents"].([]any)
	if len(contents) != 3 || len(contents[2].(map[string]any)["parts"].([]any)) != 2 {
		t.Errorf("contents = %v", contents)
	}
}

func TestBuildCodeAssistForAntigravity(t *testing.T) {
	var env struct {
		Request map[string]any `json:"request"`
	}
	json.Unmarshal(buildCodeAssist(codeAssistRequest(t), "claude-sonnet-4-6", "antigravity"), &env)
	b, _ := json.Marshal(env.Request)
	s := string(b)
	for _, want := range []string{
		`"id":"toolu_01_x"`,
		`"response":{"result":"A!"}`,
		`"mode":"VALIDATED"`,
		`"maxOutputTokens":2000`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	// Sonnet here doesn't think
	if strings.Contains(s, "thinkingConfig") {
		t.Error("thinking asked of a model that doesn't")
	}
	decl := env.Request["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	params, _ := json.Marshal(decl["parameters"])
	want := `{"properties":{"mode":{"enum":["r"]},"n":{"nullable":true,"type":"integer"},"opts":{"properties":{"deep":{"type":"boolean"}},"type":"object"},"path":{"nullable":true,"type":"string"}},"required":["path"],"type":"object"}`
	if string(params) != want {
		t.Errorf("parameters =\n%s\nwant\n%s", params, want)
	}

	// a Gemini model on Antigravity has no output cap sent, and a thinking
	// Claude has room past its budget
	json.Unmarshal(buildCodeAssist(codeAssistRequest(t), "gemini-3-flash", "antigravity"), &env)
	if gen, _ := env.Request["generationConfig"].(map[string]any); gen["maxOutputTokens"] != nil {
		t.Errorf("gen = %v", gen)
	}
	r := codeAssistRequest(t)
	r.MaxTokens = 0
	env.Request = nil
	json.Unmarshal(buildCodeAssist(r, "claude-opus-4-6-thinking", "antigravity"), &env)
	gen := env.Request["generationConfig"].(map[string]any)
	tc := gen["thinkingConfig"].(map[string]any)
	if tc["thinkingBudget"].(float64) >= gen["maxOutputTokens"].(float64) {
		t.Errorf("gen = %v", gen)
	}
}

func TestCodeAssistDecoder(t *testing.T) {
	var got []Event
	d := &codeAssistDecoder{}
	for _, line := range []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"let me see","thought":true}]}}],"modelVersion":"gemini-2.5-pro","responseId":"r1"},"traceId":"t"}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Reading."}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":3}}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a"}},"thoughtSignature":"abc"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":20,"cachedContentTokenCount":40}}}`,
	} {
		d.decode(line, func(ev Event) { got = append(got, ev) })
	}
	kinds := []EventKind{KStart, KThink, KText, KToolStart, KToolArgs, KStop, KUsage}
	if len(got) != len(kinds) {
		t.Fatalf("events = %+v", got)
	}
	for i, k := range kinds {
		if got[i].Kind != k {
			t.Fatalf("event %d = %+v, want kind %d", i, got[i], k)
		}
	}
	if got[0].Model != "gemini-2.5-pro" || got[3].Name != "read" || got[3].ID == "" || got[4].Text != `{"path":"a"}` {
		t.Errorf("events = %+v", got)
	}
	if got[5].Stop != "tool" {
		t.Errorf("stop = %q", got[5].Stop)
	}
	if u := got[6].Usage; u != (Usage{Input: 60, CacheRead: 40, Output: 30, Reasoning: 20}) {
		t.Errorf("usage = %+v", u)
	}

	var errs []Event
	(&codeAssistDecoder{}).decode(`{"error":{"code":429,"message":"quota"}}`, func(ev Event) { errs = append(errs, ev) })
	if len(errs) != 1 || errs[0].Kind != KError || errs[0].Text != "quota" {
		t.Errorf("error events = %+v", errs)
	}
}

// On Antigravity a model that is one of its families of levels is asked
// for as the variant the effort picks, told the level that variant is at
// (with none, a variant gave no thinking back once tools came in, #636);
// an old variant id moves with an effort asked; Gemini 3 Flash takes
// medium as medium, Pro as high.
func TestCodeAssistAntigravityLevels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var raw []catalog.Model
	for _, l := range []string{"gemini-3-flash - Gemini 3 Flash", "gemini-3.1-pro-high - Gemini 3.1 Pro (High)", "gemini-3.1-pro-low - Gemini 3.1 Pro (Low)",
		"gemini-3.7-flash-high - Gemini 3.7 Flash (High)", "gemini-3.7-flash-low - Gemini 3.7 Flash (Low)",
		"gemini-3.7-flash-medium - Gemini 3.7 Flash (Medium)", "gemini-pro-agent - Gemini 3.1 Pro (High)"} {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	// level: "" no thinkingConfig, "-" thinking with no level
	for _, c := range []struct{ model, effort, agent, want, level string }{
		{"gemini-3.7-flash", "low", "antigravity", "gemini-3.7-flash-low", "low"},
		{"gemini-3.7-flash", "medium", "antigravity", "gemini-3.7-flash-medium", "medium"},
		{"gemini-3.7-flash", "xhigh", "antigravity", "gemini-3.7-flash-high", "high"},
		{"gemini-3.7-flash", "", "antigravity", "gemini-3.7-flash-high", ""},
		{"gemini-3.7-flash-high", "", "antigravity", "gemini-3.7-flash-high", ""},
		{"gemini-3.7-flash-high", "low", "antigravity", "gemini-3.7-flash-low", "low"},
		{"gemini-3.1-pro", "medium", "antigravity", "gemini-3.1-pro-high", "high"},
		{"gemini-3-flash", "medium", "antigravity", "gemini-3-flash", "medium"},
		{"gemini-pro-agent", "medium", "antigravity", "gemini-pro-agent", "high"},
		{"gemini-pro-agent", "low", "antigravity", "gemini-pro-agent", "low"},
		{"gemini-3.7-flash", "low", "gemini", "gemini-3.7-flash", "low"},
	} {
		r := codeAssistRequest(t)
		r.Thinking, r.Effort = false, c.effort
		var env struct {
			Model   string         `json:"model"`
			Request map[string]any `json:"request"`
		}
		json.Unmarshal(buildCodeAssist(r, c.model, c.agent), &env)
		gen, _ := env.Request["generationConfig"].(map[string]any)
		tc, _ := gen["thinkingConfig"].(map[string]any)
		level, _ := tc["thinkingLevel"].(string)
		if tc != nil && level == "" {
			level = "-"
		}
		if env.Model != c.want || level != c.level {
			t.Errorf("%s at %q on %s: %s at %q, want %s at %q", c.model, c.effort, c.agent, env.Model, level, c.want, c.level)
		}
	}
}

// A call Antigravity's model writes out in its reply's text,
// call:default_api:<tool>{...}, is the call it meant, however the stream
// cuts it up, and a call named with Gemini's namespace is the client's
// tool (#636). Text that only looks like one stays text.
func TestCodeAssistTextCalls(t *testing.T) {
	chunk := func(parts string, finish string) string {
		f := ""
		if finish != "" {
			f = `,"finishReason":"` + finish + `"`
		}
		return `{"response":{"candidates":[{"content":{"role":"model","parts":[` + parts + `]}` + f + `}]}}`
	}
	text := func(s string) string { b, _ := json.Marshal(map[string]string{"text": s}); return string(b) }
	type call struct{ name, args string }
	for _, c := range []struct {
		name   string
		chunks []string
		text   string
		calls  []call
		stop   string
	}{
		{"whole", []string{chunk(text("Let's see what `shims/three.ts` imports!call:default_api:bash{command:cat /tmp/w/shims/three.ts}"), "STOP")},
			"Let's see what `shims/three.ts` imports!", []call{{"bash", `{"command":"cat /tmp/w/shims/three.ts"}`}}, "tool"},
		{"cut up", []string{chunk(text("And lines 240-270:cal"), ""), chunk(text("l:default_api:ba"), ""),
			chunk(text(`sh{command:sed -n '241,275p' a.ts}`), ""), chunk(text(""), "STOP")},
			"And lines 240-270:", []call{{"bash", `{"command":"sed -n '241,275p' a.ts"}`}}, "tool"},
		{"quoted", []string{chunk(text(`call:default_api:edit{path:<ctrl46>a.go<ctrl46>,new:<ctrl46>func f() {<ctrl46>,n:2}`), "STOP")},
			"", []call{{"edit", `{"path":"a.go","new":"func f() {","n":2}`}}, "tool"},
		{"json", []string{chunk(text(`ok.call:default_api:read{"path":"a","limit":3}`), "STOP")},
			"ok.", []call{{"read", `{"path":"a","limit":3}`}}, "tool"},
		{"named", []string{chunk(`{"functionCall":{"name":"default_api:bash","args":{"command":"ls"}}}`, "STOP")},
			"", []call{{"bash", `{"command":"ls"}`}}, "tool"},
		{"unfinished", []string{chunk(text("look call:default_api:bash{command:ls"), "MAX_TOKENS")},
			"look call:default_api:bash{command:ls", nil, "length"},
		{"not a call", []string{chunk(text("say call:default_api: or recall"), ""), chunk(text(" ca"), "STOP")},
			"say call:default_api: or recall ca", nil, "stop"},
	} {
		var col collector
		dec := (&codeAssistDecoder{}).decode
		for _, ch := range c.chunks {
			dec(ch, col.add)
		}
		res := col.finish()
		var got string
		var calls []call
		for _, p := range res.Parts {
			switch p.Kind {
			case Text:
				got += p.Text
			case ToolCall:
				calls = append(calls, call{p.Name, string(p.Args)})
			}
		}
		if got != c.text || res.Stop != c.stop || len(calls) != len(c.calls) {
			t.Errorf("%s: text %q, calls %v, stop %q; want %q, %v, %q", c.name, got, calls, res.Stop, c.text, c.calls, c.stop)
			continue
		}
		for i, cl := range calls {
			var a, b any
			json.Unmarshal([]byte(cl.args), &a)
			json.Unmarshal([]byte(c.calls[i].args), &b)
			if cl.name != c.calls[i].name || !reflect.DeepEqual(a, b) {
				t.Errorf("%s: call %v, want %v", c.name, cl, c.calls[i])
			}
		}
	}
}

// Two reads that each return a screenshot, as in @sinswing's report: on
// Antigravity's Claude the responses come first and the images after them,
// each told whose it is, or Google's Anthropic turn has an image between two
// tool_results ("tool_use ids were found without tool_result blocks
// immediately after"). Gemini keeps each image beside its response.
func TestCodeAssistToolImagesAfterEveryResponse(t *testing.T) {
	r, err := parseAnthropic([]byte(`{"model":"x","max_tokens":2000,
		"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],
		"messages":[
			{"role":"user","content":"look at both"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_bdrk_01AvFfWkjBwUMB97S9vUMAR7","name":"Read","input":{"file_path":"a.png"}},
				{"type":"tool_use","id":"toolu_bdrk_01TJixAcopSp2JamBfnbfF4V","name":"Read","input":{"file_path":"b.png"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_bdrk_01AvFfWkjBwUMB97S9vUMAR7","content":[
					{"type":"text","text":"a.png"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUFB"}}]},
				{"type":"tool_result","tool_use_id":"toolu_bdrk_01TJixAcopSp2JamBfnbfF4V","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QkJC"}}]},
				{"type":"text","text":"which is bigger?"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	shape := func(model, agent string) []string {
		var env struct {
			Request struct {
				Contents []struct {
					Parts []map[string]json.RawMessage `json:"parts"`
				} `json:"contents"`
			} `json:"request"`
		}
		if err := json.Unmarshal(buildCodeAssist(r, model, agent), &env); err != nil {
			t.Fatal(err)
		}
		cs := env.Request.Contents
		var out []string
		for _, p := range cs[len(cs)-1].Parts {
			switch {
			case p["functionResponse"] != nil:
				var fr struct{ ID string }
				json.Unmarshal(p["functionResponse"], &fr)
				out = append(out, "response "+fr.ID)
			case p["inlineData"] != nil:
				var d struct{ Data string }
				json.Unmarshal(p["inlineData"], &d)
				out = append(out, "image "+d.Data)
			case p["text"] != nil:
				var s string
				json.Unmarshal(p["text"], &s)
				out = append(out, s)
			}
		}
		return out
	}
	want := []string{
		"response toolu_bdrk_01AvFfWkjBwUMB97S9vUMAR7",
		"response toolu_bdrk_01TJixAcopSp2JamBfnbfF4V",
		"Image returned by tool Read (call toolu_bdrk_01AvFfWkjBwUMB97S9vUMAR7):",
		"image QUFB",
		"Image returned by tool Read (call toolu_bdrk_01TJixAcopSp2JamBfnbfF4V):",
		"image QkJC",
		"which is bigger?",
	}
	if got := shape("claude-opus-4-6-thinking", "antigravity"); !reflect.DeepEqual(got, want) {
		t.Errorf("Antigravity Claude:\n got %q\nwant %q", got, want)
	}
	gem := []string{
		"response toolu_bdrk_01AvFfWkjBwUMB97S9vUMAR7", "image QUFB",
		"response toolu_bdrk_01TJixAcopSp2JamBfnbfF4V", "image QkJC",
		"which is bigger?",
	}
	if got := shape("gemini-3-flash", "antigravity"); !reflect.DeepEqual(got, gem) {
		t.Errorf("Antigravity Gemini:\n got %q\nwant %q", got, gem)
	}
}
