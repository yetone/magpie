package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"regexp"
	"strings"

	"golang.org/x/image/draw"
)

// The browser MCP server lists one tool, browser, whose action names what
// to do: one schema to read instead of eighteen, each with its own copy of
// the tab argument. The gateway turns a call into the extension's own tool
// (browser_click and the rest), so any extension version carries it out,
// and the eighteen names are still taken from a client that listed them
// before.

type browserAction struct {
	tool string
	set  map[string]any // arguments the action stands for
}

var browserActions = map[string]browserAction{
	"tabs":        {"browser_tabs", nil},
	"tab_new":     {"browser_tabs", map[string]any{"action": "new"}},
	"tab_select":  {"browser_tabs", map[string]any{"action": "select"}},
	"tab_close":   {"browser_tabs", map[string]any{"action": "close"}},
	"browsers":    {"browser_tabs", map[string]any{"action": "browsers"}},
	"use_browser": {"browser_tabs", map[string]any{"action": "use"}},
	"navigate":    {"browser_navigate", nil},
	"back":        {"browser_navigate", map[string]any{"go": "back"}},
	"forward":     {"browser_navigate", map[string]any{"go": "forward"}},
	"reload":      {"browser_navigate", map[string]any{"go": "reload"}},
	"snapshot":    {"browser_snapshot", nil},
	"click":       {"browser_click", nil},
	"type":        {"browser_type", nil},
	"press":       {"browser_press", nil},
	"hover":       {"browser_hover", nil},
	"select":      {"browser_select", nil},
	"scroll":      {"browser_scroll", nil},
	"drag":        {"browser_drag", nil},
	"upload":      {"browser_upload", nil},
	"wait":        {"browser_wait", nil},
	"screenshot":  {"browser_screenshot", nil},
	"evaluate":    {"browser_evaluate", nil},
	"text":        {"browser_text", nil},
	"console":     {"browser_console", nil},
	"dialog":      {"browser_dialog", nil},
	// the gateway's own: browserRecord
	"record_start": {browserRecordTool, map[string]any{"action": "start"}},
	"record_stop":  {browserRecordTool, map[string]any{"action": "stop"}},
}

// browserRecordTool is the record actions' tool, which the gateway answers
// itself.
const browserRecordTool = "record"

// browserActionOrder is the order the schema lists them in.
var browserActionOrder = []string{"tabs", "tab_new", "tab_select", "tab_close", "browsers", "use_browser", "navigate", "back", "forward", "reload", "snapshot", "click", "type", "press", "hover", "select", "scroll", "drag", "upload", "wait", "screenshot", "evaluate", "text", "console", "dialog", "record_start", "record_stop"}

func browserOneTool() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	strs := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return map[string]any{
		"name":  "browser",
		"title": "Browser",
		"description": "Act in the user's own browser, in tabs of your own opened behind the one they are looking at, in your Magpie tab group; a tab of the user's you name has its page opened in one of yours. Actions, with the arguments each takes:\n" +
			"tabs: list them. tab_new [url, front]. tab_select [tab]. tab_close [tab]. browsers: the connected ones. use_browser [browser].\n" +
			"navigate [url]. back. forward. reload.\n" +
			"snapshot [interactive, ref, maxChars]: the page as an accessibility tree, a ref on each element you can act on.\n" +
			"click [ref, double, button, modifiers]. type [ref, text, clear, submit, slowly]. press [key, ref]. hover [ref]. select [ref, values]. scroll [direction, amount, ref]. drag [from, to]. upload [ref, paths].\n" +
			"wait [text, textGone, url, seconds, timeout]. screenshot [ref, fullPage, png]. evaluate [script, ref]. text [maxChars]: the visible text. console [level, limit]. dialog [accept, promptText].\n" +
			"record_start: keep a picture of the window after each action that changes the page. record_stop [format]: those pictures as a video, at a link to download, for the user to see what was done.\n" +
			"Every action takes tab, and screenshot: true to add a small picture of the window to its answer.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string", "enum": browserActionOrder},
				"tab":         str("t123, from tabs. Default: this session's own tab, else a new one behind the user's."),
				"ref":         str("An element's ref from the latest snapshot (e12)."),
				"url":         str("navigate, tab_new: the address; wait: until the address contains this."),
				"text":        str("type: what to type; wait: until the page shows this."),
				"front":       boolean("tab_new: show it to the user, only when they asked to see it."),
				"browser":     str("use_browser: its id from browsers (b2) or name (Edge)."),
				"interactive": boolean("snapshot: only what you can act on, and headings."),
				"maxChars":    num("snapshot, text: the most characters back."),
				"double":      boolean("click: double-click."),
				"button":      map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
				"modifiers":   map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}}},
				"clear":       boolean("type: replace what the field holds (default true)."),
				"submit":      boolean("type: press Enter after."),
				"slowly":      boolean("type: a key at a time, for an autocomplete."),
				"key":         str("press: a key or chord (Enter, Control+a)."),
				"values":      strs("select: the options, by value or label."),
				"direction":   map[string]any{"type": "string", "enum": []string{"down", "up", "left", "right"}, "description": "scroll; with a ref and no direction, into view."},
				"amount":      num("scroll: pixels."),
				"from":        str("drag: the ref to drag."),
				"to":          str("drag: the ref to drop on."),
				"paths":       strs("upload: full paths of files on this computer."),
				"textGone":    str("wait: until the page no longer shows this."),
				"seconds":     num("wait: just this long (at most 60)."),
				"timeout":     num("wait: give up after (default 10, at most 60)."),
				"fullPage":    boolean("screenshot: the whole page."),
				"png":         boolean("screenshot: PNG, not JPEG."),
				"script":      str("evaluate: an expression, or a function (called with the ref's element)."),
				"level":       map[string]any{"type": "string", "enum": []string{"all", "errors", "network"}},
				"limit":       num("console: the most recent messages."),
				"accept":      boolean("dialog: accept (default true) or dismiss."),
				"promptText":  str("dialog: a prompt's answer."),
				"format":      map[string]any{"type": "string", "enum": []string{"mp4", "gif"}, "description": "record_stop: mp4 (default) or gif."},
				"screenshot":  boolean("Add a small picture of the window to the answer."),
			},
			"required": []string{"action"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "openWorldHint": true},
	}
}

// browserUnfold turns a browser call into the extension's tool and its
// arguments, and says whether a picture is wanted after it.
func browserUnfold(raw json.RawMessage) (tool string, args json.RawMessage, shot bool, err error) {
	m := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", nil, false, errors.New("BAD_ARGS: the arguments aren't a JSON object: " + err.Error())
		}
	}
	name, _ := m["action"].(string)
	a, ok := browserActions[name]
	if !ok {
		return "", nil, false, errors.New("BAD_ARGS: action is one of " + strings.Join(browserActionOrder, ", "))
	}
	shot, _ = m["screenshot"].(bool)
	delete(m, "action")
	delete(m, "screenshot")
	for k, v := range a.set {
		m[k] = v
	}
	args, _ = json.Marshal(m)
	return a.tool, args, shot, nil
}

// the extension's answers name its own tools; through browser they are
// actions
var browserNameRe = regexp.MustCompile(`browser_(tabs|navigate|snapshot|click|type|press|hover|select|scroll|drag|upload|wait|screenshot|evaluate|text|console|dialog)\b`)

var browserNameSay = strings.NewReplacer(
	"browser_tabs with action new", "the tab_new action",
	"browser_tabs with action browsers", "the browsers action",
	"browser_tabs with action use and browser", "the use_browser action with browser",
	"browser_tabs with action use", "the use_browser action",
	"browser_tabs select", "the tab_select action",
	"browser_navigate", "the navigate action",
)

func browserSay(s string) string {
	return browserNameRe.ReplaceAllString(browserNameSay.Replace(s), "the $1 action")
}

// browserSays rewrites the text of a result's content for browser.
func browserSays(result map[string]any) {
	content, _ := result["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok && m["type"] == "text" {
			if s, ok := m["text"].(string); ok {
				m["text"] = browserSay(s)
			}
		}
	}
}

// browserShotWidth is the widest picture added to an answer: about a
// thousand tokens for a window, where a Retina one is several times that.
const browserShotWidth = 1024

// browserSmall is a screenshot made small enough to come with every
// answer: no wider than browserShotWidth, a JPEG.
func browserSmall(data string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", false
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return "", false
	}
	r := img.Bounds()
	if r.Dx() > browserShotWidth {
		h := r.Dy() * browserShotWidth / r.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, browserShotWidth, max(1, h)))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, r, draw.Src, nil)
		img = dst
	}
	var out bytes.Buffer
	if jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}) != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), true
}
