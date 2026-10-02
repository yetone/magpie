package provider

// ZCode's Start Plan serves only what looks like the ZCode app's own
// request: one without ZCode's system prompt is turned away with 405
// "request has been blocked due to unusual activity", code 3012 (#425).
// So a Start Plan request goes as ZCode sends it: the app's system prompt
// first, three blocks (its opening line; its identity and desktop
// context; "\n\n" and its dynamic sections around the environment), each
// cached, the agent's own system prompt after them; the day in a
// <system-reminder> before the first user turn; and the app's headers.
// The text is ZCode 3.11's (zcode_prompt.json), as zcode2api
// (github.com/a137460387/zcode2api, src/upstream/system-prompt.js,
// zcode-system.json, headers.js) takes it from the app. The GLM Coding
// Plan, and every other provider, get the agent's request as it is.

import (
	_ "embed"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed zcode_prompt.json
var zcodePromptJSON []byte

var zcodePrompt = func() (p struct {
	Prefix            string `json:"prefix"`
	Stable            string `json:"stable"`
	BeforeEnvironment string `json:"beforeEnvironment"`
	AfterEnvironment  string `json:"afterEnvironment"`
	Environment       struct {
		Heading        string `json:"heading"`
		InvokedLine    string `json:"invokedLine"`
		CwdLabel       string `json:"cwdLabel"`
		GitLabel       string `json:"gitLabel"`
		GitNo          string `json:"gitNo"`
		PlatformLabel  string `json:"platformLabel"`
		ShellLabel     string `json:"shellLabel"`
		OSVersionLabel string `json:"osVersionLabel"`
		PoweredByLine  string `json:"poweredByLine"`
	} `json:"environment"`
	Context struct {
		Intro              string `json:"intro"`
		Outro              string `json:"outro"`
		CurrentDateHeading string `json:"currentDateHeading"`
		CurrentDateLine    string `json:"currentDateLine"`
	} `json:"context"`
}) {
	if err := json.Unmarshal(zcodePromptJSON, &p); err != nil {
		panic("zcode_prompt.json: " + err.Error())
	}
	return p
}()

// zcodePlatform is the platform as ZCode (Electron) names it.
func zcodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// zcodeEnvironment is the prompt's environment section. Where the agent
// runs isn't magpie's to say, so that is left unknown.
func zcodeEnvironment(model string) string {
	e := zcodePrompt.Environment
	shell := "unknown"
	if s := os.Getenv("SHELL"); s != "" {
		shell = filepath.Base(s)
	} else if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	lines := []string{
		e.Heading,
		e.InvokedLine,
		"- " + e.CwdLabel + ": unknown",
		"- " + e.GitLabel + ": " + e.GitNo,
		"- " + e.PlatformLabel + ": " + zcodePlatform(),
		"- " + e.ShellLabel + ": " + shell,
		"- " + e.OSVersionLabel + ": unknown",
	}
	if model != "" {
		lines = append(lines, strings.NewReplacer("{provider}", "bigmodel-api", "{model}", strings.ToLower(model)).Replace(e.PoweredByLine))
	}
	return strings.Join(lines, "\n")
}

// zcodeSystem is ZCode's three system blocks for model.
func zcodeSystem(model string) []any {
	cached := func(text string) map[string]any {
		return map[string]any{"type": "text", "text": text, "cache_control": map[string]any{"type": "ephemeral"}}
	}
	dynamic := strings.Join([]string{zcodePrompt.BeforeEnvironment, zcodeEnvironment(model), zcodePrompt.AfterEnvironment}, "\n\n")
	return []any{cached(zcodePrompt.Prefix), cached(zcodePrompt.Stable), cached("\n\n" + dynamic)}
}

// zcodeDateReminder is what ZCode puts before the first user turn.
func zcodeDateReminder(now time.Time) map[string]any {
	c := zcodePrompt.Context
	text := strings.Join([]string{c.Intro, c.CurrentDateHeading + "\n" + strings.ReplaceAll(c.CurrentDateLine, "{date}", now.Format("2006-01-02")), "", c.Outro}, "\n")
	return map[string]any{"type": "text", "text": "<system-reminder>" + text + "</system-reminder>"}
}

// zcodeStartBody is an Anthropic messages request as ZCode would send it
// to the Start Plan: ZCode's system blocks before the agent's, the date
// before the first user turn when nothing is reminded there already. As
// ZCode's blocks take three of the four cache breakpoints Anthropic's API
// allows, the agent's are dropped from its system and tools, and only its
// last in the messages is kept. A body that isn't one, or already starts
// with ZCode's prompt, is left as it is.
func zcodeStartBody(body []byte, now time.Time) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m["messages"] == nil {
		return body
	}
	var model string
	json.Unmarshal(m["model"], &model)

	var own []any
	if raw := m["system"]; raw != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if strings.TrimSpace(s) != "" {
				own = []any{map[string]any{"type": "text", "text": s}}
			}
		} else if zcodeDecode(raw, &own) != nil {
			return body
		}
	}
	if len(own) > 0 {
		if b, ok := own[0].(map[string]any); ok && b["text"] == zcodePrompt.Prefix {
			return body
		}
	}
	for _, b := range own {
		if b, ok := b.(map[string]any); ok {
			delete(b, "cache_control")
		}
	}
	system, _ := zcodeEncode(append(zcodeSystem(model), own...))

	var msgs []map[string]any
	if zcodeDecode(m["messages"], &msgs) != nil {
		return body
	}
	last := true
	for i := len(msgs) - 1; i >= 0; i-- {
		blocks, _ := msgs[i]["content"].([]any)
		for j := len(blocks) - 1; j >= 0; j-- {
			if b, ok := blocks[j].(map[string]any); ok && b["cache_control"] != nil {
				if !last {
					delete(b, "cache_control")
				}
				last = false
			}
		}
	}
	if len(msgs) > 0 && msgs[0]["role"] == "user" {
		var blocks []any
		switch c := msgs[0]["content"].(type) {
		case string:
			blocks = []any{map[string]any{"type": "text", "text": c}}
		case []any:
			blocks = c
		}
		reminded := false
		for _, b := range blocks {
			if b, ok := b.(map[string]any); ok {
				if t, _ := b["text"].(string); b["type"] == "text" && strings.HasPrefix(t, "<system-reminder>") {
					reminded = true
				}
			}
		}
		if !reminded {
			msgs[0]["content"] = append([]any{zcodeDateReminder(now)}, blocks...)
		}
	}
	messages, err := zcodeEncode(msgs)
	if err != nil {
		return body
	}

	if raw := m["tools"]; raw != nil {
		var tools []map[string]any
		if zcodeDecode(raw, &tools) == nil {
			for _, t := range tools {
				delete(t, "cache_control")
			}
			m["tools"], _ = zcodeEncode(tools)
		}
	}
	m["system"], m["messages"] = system, messages
	out, err := zcodeEncode(m)
	if err != nil {
		return body
	}
	return out
}

// zcodeDecode reads JSON keeping its numbers as they are written.
func zcodeDecode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(v)
}

// zcodeEncode writes JSON leaving <, > and & as they are.
func zcodeEncode(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
