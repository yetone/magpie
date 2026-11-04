package gateway

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// manyHubTools are n tools of two servers, github's create_issue among
// them, more than an agent is given whole.
func manyHubTools(n int) []HubTool {
	ts := []HubTool{{Server: "github", Tool: "create_issue", Spec: map[string]json.RawMessage{
		"name":        json.RawMessage(`"create_issue"`),
		"description": json.RawMessage(`"Opens a new issue in a GitHub repository. Needs a title."`),
		"inputSchema": json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`),
	}}}
	for i := 1; len(ts) < n; i++ {
		ts = append(ts, HubTool{Server: "fs", Tool: fmt.Sprintf("op%d", i), Spec: hubSpec(fmt.Sprintf("op%d", i))})
	}
	return ts
}

func listedNames(t *testing.T, m mcpReply) []string {
	t.Helper()
	var out []string
	for _, x := range m.Result.Tools {
		out = append(out, x.Name)
	}
	return out
}

func initInstructions(t *testing.T, body string) string {
	t.Helper()
	var m struct {
		Result struct{ Instructions string }
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m.Result.Instructions
}

const hubInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

// An agent that doesn't search its MCP tools itself, given more than
// hubOnDemandOver, is given find_tools and call_tool in their place
// (ArvinCharl on X: the tools filled the context); the instructions say
// so and still name each server's tools. find_tools gives a match's
// parameters, call_tool runs it by its name, its bare name or with its
// arguments as a string, and a name no server gives is told the close
// ones. A tool called by its own name still runs.
func TestHubMCPToolsOnDemand(t *testing.T) {
	fresh(t)
	tools := manyHubTools(hubOnDemandOver + 2)
	f := &fakeHub{on: true, tools: map[string][]HubTool{"opencode": tools},
		servers: map[string][]HubServer{"opencode": {
			{Name: "github", Known: true, Tools: []HubToolNote{{Name: "create_issue", About: "Opens a new issue"}}},
			{Name: "fs", Known: true, Tools: func() (ns []HubToolNote) {
				for _, x := range tools[1:] {
					ns = append(ns, HubToolNote{Name: x.Tool})
				}
				return
			}()},
		}}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	say := initInstructions(t, hubPost(h, "opencode", here, hubInit).Body.String())
	if !strings.Contains(say, "on demand") || !strings.Contains(say, "create_issue") {
		t.Fatalf("instructions: %s", say)
	}
	if got := listedNames(t, readMCP(t, hubPost(h, "opencode", here, toolsList))); !slices.Equal(got, []string{hubFindTool, hubCallTool}) {
		t.Fatalf("listed %v", got)
	}

	found := readMCP(t, hubPost(h, "opencode", here, toolsCall(hubFindTool, `{"query":"open an issue"}`)))
	if found.Result.IsError || len(found.Result.Content) == 0 {
		t.Fatalf("find: %+v", found)
	}
	text := found.Result.Content[0].Text
	if !strings.Contains(text, `"github__create_issue"`) || !strings.Contains(text, `"required"`) || strings.Contains(text, "fs__op") {
		t.Fatalf("find gave: %s", text)
	}
	all := readMCP(t, hubPost(h, "opencode", here, toolsCall(hubFindTool, `{}`))).Result.Content[0].Text
	for _, x := range []string{"github__create_issue", "fs__op1", "fs__op11"} {
		if !strings.Contains(all, x) {
			t.Fatalf("an empty query left out %s: %s", x, all)
		}
	}
	if s := readMCP(t, hubPost(h, "opencode", here, toolsCall(hubFindTool, `{"query":"op","server":"github"}`))); strings.Contains(s.Result.Content[0].Text, "fs__") {
		t.Fatalf("server not kept to: %s", s.Result.Content[0].Text)
	}

	for _, args := range []string{
		`{"name":"github__create_issue","arguments":{"title":"a"}}`,
		`{"name":"create_issue","arguments":{"title":"a"}}`,
		`{"name":"github__create_issue","arguments":"{\"title\":\"a\"}"}`,
	} {
		ran := readMCP(t, hubPost(h, "opencode", here, toolsCall(hubCallTool, args)))
		if ran.Result.IsError || ran.Result.Content[0].Text != "ran github create_issue" {
			t.Fatalf("call_tool %s: %+v", args, ran)
		}
	}
	for _, c := range f.ran() {
		if c != `opencode github create_issue {"title":"a"}` {
			t.Fatalf("ran %q", c)
		}
	}
	missed := readMCP(t, hubPost(h, "opencode", here, toolsCall(hubCallTool, `{"name":"create_isue"}`)))
	if !missed.Result.IsError || !strings.Contains(missed.Result.Content[0].Text, "github__create_issue") {
		t.Fatalf("an unknown name: %+v", missed)
	}
	if direct := readMCP(t, hubPost(h, "opencode", here, toolsCall("fs__op3", `{"q":"x"}`))); direct.Result.IsError {
		t.Fatalf("by its own name: %+v", direct)
	}
	// the row names the tool that ran, not call_tool
	rows := mcpRows(t)
	if len(rows) == 0 || !slices.ContainsFunc(rows, func(r usage.Record) bool { return r.Requested == "github__create_issue" && r.Host == "github" }) ||
		slices.ContainsFunc(rows, func(r usage.Record) bool { return r.Requested == hubCallTool && r.Error == "" }) {
		t.Fatalf("rows %+v", rows)
	}
}

// An agent that searches its tools itself (a WSL one's id too), the
// address for any other app, an agent given a few tools, and every agent
// once the user turns on every tool whole, are given every tool.
func TestHubMCPToolsWhole(t *testing.T) {
	fresh(t)
	many := manyHubTools(hubOnDemandOver + 2)
	f := &fakeHub{on: true, tools: map[string][]HubTool{
		"claude": many, "claude@wsl:Ubuntu": many, "claude-desktop": many, "codex": many, "pi": many, "": many,
		"gemini": manyHubTools(hubOnDemandOver), "opencode": many,
	}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	for _, a := range []string{"claude", "claude@wsl:Ubuntu", "claude-desktop", "codex", "pi", "gemini"} {
		if got := listedNames(t, readMCP(t, hubPost(h, a, here, toolsList))); len(got) != len(f.tools[a]) {
			t.Fatalf("%s listed %v", a, got)
		}
	}
	if got := listedNames(t, readMCP(t, hubPostAt(h, HubMCPAll, here, toolsList))); len(got) != len(many) {
		t.Fatalf("any app listed %v", got)
	}
	s := settings.Load()
	s.RouterAllTools = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s := settings.Load()
		s.RouterAllTools = false
		settings.Save(s)
	})
	if got := listedNames(t, readMCP(t, hubPost(h, "opencode", here, toolsList))); len(got) != len(many) {
		t.Fatalf("with every tool whole, listed %v", got)
	}
}
