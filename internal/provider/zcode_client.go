package provider

// PLUGIN-SERVED (see AGENTS.md): ZCode ("zcode") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zcode-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zcode) and raise the
// mover's min in internal/provider/migrate_zcode.go.

// ZCode's Start Plan serves only what looks like ZCode's own request: one
// without ZCode's system prompt is turned away with 405 "request has been
// blocked due to unusual activity", code 3012 (#425), and one dressed as
// the desktop app, or as ZCode 3.14.3's CLI source has it, still was for
// some, while the OpenCode plugin ARNO sent ("Freeflow", provider
// zcode-start) got through on the same account. So a Start Plan request
// goes as that plugin sends it: three cached system blocks (its opening
// line; its identity and harness, then the desktop context; "\n\n" and its
// dynamic sections around the environment), the agent's own system text
// after them uncached; the day in a <system-reminder> as a user turn of
// its own before the agent's; one cache mark in the turns, on the last;
// metadata.user_id naming the device; and its headers
// (zcodeSourceHeaders). The text is zcode_prompt.json. The GLM Coding
// Plan, and every other provider, get the agent's request as it is.

import (
	_ "embed"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

//go:embed zcode_prompt.json
var zcodePromptJSON []byte

var zcodePrompt = func() (p struct {
	Prefix            string `json:"prefix"`
	Stable            string `json:"stable"`
	Desktop           string `json:"desktop"`
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

// zcodePlatform is the platform as ZCode (Node's process.platform) names it.
func zcodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// zcodeArch is the architecture as ZCode (Node's os.arch()) names it.
func zcodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	}
	return runtime.GOARCH
}

// zcodeOSVersion is the prompt's OS Version as ZCode gives it: "darwin
// 25.2.0 arm64".
func zcodeOSVersion() string {
	parts := []string{zcodePlatform()}
	if r := zcodeOSRelease(); r != "" {
		parts = append(parts, r)
	}
	return strings.Join(append(parts, zcodeArch()), " ")
}

// zcodeStartProvider is the provider the powered-by line names for the
// Start Plan of an account on base, as the plugin the Start Plan serves
// names it: zai-api, or bigmodel-api on BigModel.
func zcodeStartProvider(base string) string {
	if base == ZCodeBigModelBase {
		return "bigmodel-api"
	}
	return "zai-api"
}

// zcodeCwdRe finds the working directory an agent names in its own prompt
// (Claude Code's "Primary working directory: …", Codex's <cwd>…</cwd>).
var zcodeCwdRe = regexp.MustCompile(`(?m)(?:^\s*-?\s*(?:Primary working directory|Working directory):[ \t]*([^\r\n]+?)[ \t]*$|<cwd>([^<\r\n]+)</cwd>)`)

// zcodeCwd is the agent's working directory, as the environment section's
// process.cwd(): the one its request names, else the user's home.
func zcodeCwd(texts []string) string {
	for _, t := range texts {
		if m := zcodeCwdRe.FindStringSubmatch(t); m != nil {
			return strings.TrimSpace(m[1] + m[2])
		}
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// zcodeShell is the shell as the environment section names it: the
// basename of $SHELL, or of %ComSpec% on Windows (cmd.exe).
func zcodeShell() string {
	for _, k := range []string{"SHELL", "ComSpec"} {
		if s := os.Getenv(k); s != "" {
			return filepath.Base(s)
		}
	}
	return "unknown"
}

// zcodeEnvironment is the prompt's environment section for model on
// provider, cwd being where the agent runs.
func zcodeEnvironment(provider, model, cwd string) string {
	e := zcodePrompt.Environment
	lines := []string{
		e.Heading,
		e.InvokedLine,
		"- " + e.CwdLabel + ": " + cwd,
		"- " + e.GitLabel + ": " + e.GitNo,
		"- " + e.PlatformLabel + ": " + zcodePlatform(),
		"- " + e.ShellLabel + ": " + zcodeShell(),
		"- " + e.OSVersionLabel + ": " + zcodeOSVersion(),
	}
	if model != "" {
		lines = append(lines, strings.NewReplacer("{provider}", provider, "{model}", model).Replace(e.PoweredByLine))
	}
	return strings.Join(lines, "\n")
}

// zcodeStable is the stable system block: the identity and harness
// section, then the desktop context, as the plugin joins them.
func zcodeStable() string { return zcodePrompt.Stable + "\n\n" + zcodePrompt.Desktop }

// zcodeSystem is ZCode's three system blocks for model on provider.
func zcodeSystem(provider, model, cwd string) []any {
	cached := func(text string) map[string]any {
		return map[string]any{"type": "text", "text": text, "cache_control": map[string]any{"type": "ephemeral"}}
	}
	dynamic := strings.Join([]string{zcodePrompt.BeforeEnvironment, zcodeEnvironment(provider, model, cwd), zcodePrompt.AfterEnvironment}, "\n\n")
	return []any{cached(zcodePrompt.Prefix), cached(zcodeStable()), cached("\n\n" + dynamic)}
}

// zcodeDateReminder is the context prefix, a user turn of its own before
// the agent's: the day in a <system-reminder>, the tags hugging the text.
func zcodeDateReminder(now time.Time) map[string]any {
	c := zcodePrompt.Context
	text := strings.Join([]string{c.Intro, c.CurrentDateHeading + "\n" + strings.ReplaceAll(c.CurrentDateLine, "{date}", now.Format("2006-01-02")), "", c.Outro}, "\n")
	return map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<system-reminder>" + text + "</system-reminder>"}}}
}

// zcodeUserID is metadata.user_id as the plugin sends it: this machine's
// device id, no account or session, as JSON.
func zcodeUserID() string {
	b, _ := json.Marshal(struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}{zcodeDeviceMid(), "", ""})
	return string(b)
}

// zcodeStartBody is an Anthropic messages request as the plugin the Start
// Plan serves sends it, provider (zcodeStartProvider) serving it: ZCode's
// three cached system blocks, then the agent's own system text uncached;
// the context prefix as the first turn; no cache mark in the turns but one
// on the last block of the last; none on the tools; and metadata.user_id
// naming the device. A body that isn't one, or already starts with
// ZCode's prompt, is left as it is.
func zcodeStartBody(body []byte, provider string, now time.Time) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m["messages"] == nil {
		return body
	}
	var model string
	json.Unmarshal(m["model"], &model)

	var own []any
	var texts []string
	if raw := m["system"]; raw != nil {
		var s string
		var blocks []any
		if json.Unmarshal(raw, &s) == nil {
			if strings.TrimSpace(s) != "" {
				own = []any{map[string]any{"type": "text", "text": s}}
				texts = append(texts, s)
			}
		} else if zcodeDecode(raw, &blocks) == nil {
			for _, b := range blocks {
				b, _ := b.(map[string]any)
				t, _ := b["text"].(string)
				if b["type"] == "text" && t != "" {
					if t == zcodePrompt.Prefix && len(own) == 0 {
						return body
					}
					own = append(own, map[string]any{"type": "text", "text": t})
					texts = append(texts, t)
				}
			}
		} else if string(raw) != "null" {
			return body
		}
	}

	var msgs []map[string]any
	if zcodeDecode(m["messages"], &msgs) != nil {
		return body
	}
	if len(msgs) > 0 {
		switch c := msgs[0]["content"].(type) {
		case string:
			texts = append(texts, c)
		case []any:
			for _, b := range c {
				if b, ok := b.(map[string]any); ok {
					if t, ok := b["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
		}
	}
	system, _ := zcodeEncode(append(zcodeSystem(provider, model, zcodeCwd(texts)), own...))

	msgs = append([]map[string]any{zcodeDateReminder(now)}, msgs...)
	lastAt := -1
	for i, msg := range msgs {
		if msg["role"] == "system" {
			continue
		}
		lastAt = i
		if blocks, ok := msg["content"].([]any); ok {
			for _, b := range blocks {
				if b, ok := b.(map[string]any); ok {
					delete(b, "cache_control")
				}
			}
		}
	}
	if lastAt >= 0 {
		switch c := msgs[lastAt]["content"].(type) {
		case string:
			msgs[lastAt]["content"] = []any{map[string]any{"type": "text", "text": c, "cache_control": map[string]any{"type": "ephemeral"}}}
		case []any:
			if len(c) > 0 {
				if b, ok := c[len(c)-1].(map[string]any); ok {
					b["cache_control"] = map[string]any{"type": "ephemeral"}
				}
			}
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
	meta := map[string]any{}
	if raw := m["metadata"]; raw != nil {
		zcodeDecode(raw, &meta)
		if meta == nil {
			meta = map[string]any{}
		}
	}
	meta["user_id"] = zcodeUserID()
	m["metadata"], _ = zcodeEncode(meta)
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
