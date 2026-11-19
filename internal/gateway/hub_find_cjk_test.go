package gateway

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// cjkHubTools are realHubTools with more: a screenshot tool described in
// English, two described in Chinese, and one of a server named in Chinese.
func cjkHubTools() []hubTool {
	var ts []HubTool
	for _, t := range realHubTools() {
		ts = append(ts, t.HubTool)
	}
	add := func(server, tool, desc string) {
		d, _ := json.Marshal(desc)
		n, _ := json.Marshal(tool)
		ts = append(ts, HubTool{Server: server, Tool: tool, Spec: map[string]json.RawMessage{
			"name": n, "description": d, "inputSchema": json.RawMessage(`{"type":"object","properties":{}}`),
		}})
	}
	add("puppeteer", "puppeteer_screenshot", "Take a screenshot of the current page or a specific element.")
	add("desktop", "snap", "截取当前屏幕或窗口的截图，保存为 PNG 文件。")
	add("feishu", "send_message", "发送飞书消息到群聊或个人。")
	add("截屏工具", "capture", "Capture the whole screen as a PNG.")
	return hubNamed(ts)
}

// An agent that works in Chinese finds the tools there are (Passings_z on
// X, 11-19). Chinese has no spaces, so 帮我截个图 was one word, looked for
// whole in English descriptions, and a query of 截图 and screenshot needed
// both, so nothing ever matched. A word in Chinese now counts only for a
// tool with such letters, and is matched as pieces of it.
func TestHubFindInChinese(t *testing.T) {
	ts := cjkHubTools()
	for q, want := range map[string]string{
		"截图 screenshot":        "puppeteer__puppeteer_screenshot",
		"截图screenshot":         "puppeteer__puppeteer_screenshot",
		"take a screenshot 截图": "puppeteer__puppeteer_screenshot",
		"帮我截个图":                "desktop__snap",
		"截取屏幕":                 "desktop__snap",
		"发送飞书消息":               "feishu__send_message",
		"发消息到群聊":               "feishu__send_message",
		"截屏":                   "截屏工具/capture",
	} {
		ms := hubMatches(ts, q)
		if len(ms) == 0 || ms[0].name != want && ms[0].Server+"/"+ms[0].Tool != want {
			t.Errorf("%q matched %v, want %s first", q, hubToolNames(ms), want)
		}
	}
	for q, not := range map[string]string{
		"发送飞书消息": "desktop__snap",
		"帮我截个图":  "feishu__send_message",
		"截图":     "github__create_issue",
	} {
		if ms := hubMatches(ts, q); slices.ContainsFunc(ms, func(t hubTool) bool { return t.name == not }) {
			t.Errorf("%q matched %v", q, hubToolNames(ms))
		}
	}
	// the match comes with its parameters
	if text := findText(t, ts, `{"query":"截图 screenshot"}`); !strings.Contains(text, `"puppeteer__puppeteer_screenshot"`) || strings.HasPrefix(text, "No tool") {
		t.Errorf("截图 screenshot: %s", text)
	}
}

// A query in Chinese that no tool can match, all of them described in
// English, says so and names every tool, so the model can look again in
// English or pick one; naming eight of a server's, it had no way to the
// rest.
func TestHubFindNotFoundNamesEveryTool(t *testing.T) {
	var raw []HubTool
	for i := range 12 {
		d, _ := json.Marshal("Does thing " + fmt.Sprint(i) + ".")
		raw = append(raw, HubTool{Server: "chrome-devtools", Tool: fmt.Sprintf("tool_%d", i), Spec: map[string]json.RawMessage{"description": d}})
	}
	ts := hubNamed(raw)
	text := findText(t, ts, `{"query":"帮我截个图"}`)
	if !strings.HasPrefix(text, "No tool was found for") || !strings.Contains(text, "chrome-devtools (12 tools: tool_0,") || !strings.Contains(text, "tool_11)") || strings.Contains(text, "…") {
		t.Errorf("not every tool: %s", text)
	}
	if !strings.Contains(text, "described in English") {
		t.Errorf("no word of English: %s", text)
	}
	if text := findText(t, ts, `{"query":"send an email"}`); strings.Contains(text, "described in English") || !strings.Contains(text, "tool_11)") {
		t.Errorf("send an email: %s", text)
	}
	// too many to name: eight a server, and the way to all of them
	raw = nil
	for i := range 300 {
		d, _ := json.Marshal("Does thing.")
		raw = append(raw, HubTool{Server: "big", Tool: fmt.Sprintf("a_rather_long_tool_name_%d", i), Spec: map[string]json.RawMessage{"description": d}})
	}
	text = findText(t, hubNamed(raw), `{"query":"send an email"}`)
	if !strings.Contains(text, "big (300 tools: a_rather_long_tool_name_0,") || !strings.Contains(text, "…") || !strings.Contains(text, hubFindTool+" with an empty query names every tool") || runeLen(text) > hubSayMax+500 {
		t.Errorf("300 tools: %s", text)
	}
}

// What an English query matches is what it matched before words in
// Chinese were (results of 14b6b9555).
func TestHubFindEnglishAsBefore(t *testing.T) {
	ts := realHubTools()
	for q, want := range map[string][]string{
		"generate an image":           {"magpie-image__generate_image"},
		"open a page in the browser":  {"magpie-browser__browser"},
		"read a file":                 {"filesystem__read_file"},
		"create a GitHub issue":       {"github__create_issue", "github__list_issues", "github__create_pull_request", "linear__save_issue"},
		"open a pull request":         {"github__create_pull_request"},
		"list my Linear issues":       {"linear__list_issues", "github__list_issues", "linear__save_issue"},
		"search code":                 {"github__search_code"},
		"browser":                     {"magpie-browser__browser"},
		"write_file":                  {"filesystem__write_file"},
		"issue":                       {"github__create_issue", "github__list_issues", "linear__save_issue", "linear__list_issues"},
		"issues":                      {"github__create_issue", "github__list_issues", "linear__save_issue", "linear__list_issues"},
		"search":                      {"github__search_code", "filesystem__search_files", "linear__search_documentation"},
		"file":                        {"github__get_file_contents", "filesystem__read_file", "filesystem__write_file", "filesystem__list_directory", "filesystem__search_files", "magpie-image__generate_image"},
		"list":                        {"github__list_issues", "filesystem__list_directory", "linear__list_issues", "magpie-browser__browser"},
		"create":                      {"github__create_issue", "github__create_pull_request", "filesystem__write_file", "linear__save_issue"},
		"repository":                  {"github__create_issue", "github__list_issues", "github__create_pull_request", "github__get_file_contents"},
		"directory listing":           {"filesystem__list_directory"},
		"search files in a directory": {"filesystem__list_directory", "filesystem__search_files", "github__get_file_contents"},
		"Linear documentation":        {"linear__search_documentation"},
		"screenshot":                  {"magpie-browser__browser"},
		"take a screenshot":           {"magpie-browser__browser"},
		"github":                      {"github__create_issue", "github__list_issues", "github__search_code", "github__create_pull_request", "github__get_file_contents"},
		"linear":                      {"linear__save_issue", "linear__list_issues", "linear__search_documentation"},
		"send an email":               nil,
		"create or update":            {"linear__save_issue"},
		"contents of a file":          {"github__get_file_contents", "filesystem__read_file", "filesystem__write_file"},
		"image":                       {"magpie-image__generate_image"},
		"pull request":                {"github__create_pull_request"},
		"find files":                  {"filesystem__list_directory", "filesystem__search_files"},
		"edit images":                 {"magpie-image__generate_image"},
	} {
		if got := hubToolNames(hubMatches(ts, q)); !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%q matched %q, want %q", q, got, want)
		}
	}
}
