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

// An agent that searches its tools itself (a WSL one's id too: Droid,
// which defers every MCP tool to its ToolSearch, and omp, which names them
// in a catalog, are such agents; tao5010 on Discord), the
// address for any other app, an agent given a few tools, and every agent
// once the user turns on every tool whole, are given every tool.
func TestHubMCPToolsWhole(t *testing.T) {
	fresh(t)
	many := manyHubTools(hubOnDemandOver + 2)
	f := &fakeHub{on: true, tools: map[string][]HubTool{
		"claude": many, "claude@wsl:Ubuntu": many, "claude-desktop": many, "codex": many, "pi": many, "": many,
		"droid": many, "droid@wsl:Ubuntu": many, "omp": many,
		"gemini": manyHubTools(hubOnDemandOver), "opencode": many,
	}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	for _, a := range []string{"claude", "claude@wsl:Ubuntu", "claude-desktop", "codex", "pi", "droid", "droid@wsl:Ubuntu", "omp", "gemini"} {
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

// listedSpecs are tools/list's tools, by name, with their descriptions.
func listedSpecs(t *testing.T, body string) map[string]string {
	t.Helper()
	var m struct {
		Result struct {
			Tools []struct{ Name, Description string }
		}
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	out := map[string]string{}
	for _, x := range m.Result.Tools {
		out[x.Name] = x.Description
	}
	return out
}

// imageAndNotes are an image server and one of n tools, ticked for agent,
// as the hub lists and describes them.
func imageAndNotes(agent string, n int) *fakeHub {
	img := []HubTool{
		{Server: "magpie-image", Tool: "generate_image", Spec: map[string]json.RawMessage{
			"name":        json.RawMessage(`"generate_image"`),
			"description": json.RawMessage(`"Generate an image from a text prompt, saved in the project."`),
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}`),
		}},
	}
	notes := HubServer{Name: "notes", Known: true}
	ts := slices.Clone(img)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("note_op%d", i)
		ts = append(ts, HubTool{Server: "notes", Tool: name, Spec: hubSpec(name)})
		notes.Tools = append(notes.Tools, HubToolNote{Name: name, About: "Does " + name})
	}
	return &fakeHub{on: true, tools: map[string][]HubTool{agent: ts}, servers: map[string][]HubServer{agent: {
		{Name: "magpie-image", About: "Generate and edit images", Known: true, Tools: []HubToolNote{{Name: "generate_image", About: "Generate an image from a text prompt"}}},
		notes,
	}}}
}

// An agent given its tools on demand learns from tools/list alone what is
// behind them: find_tools' description starts with the line naming each
// server and what it is for, the part an agent showing a tool by its first
// line shows (omp's catalog), and names each server's tools after what it
// does. Told only how to search, a model asked for a picture of a cat
// searched three times and stopped (tao5010 on Discord, 11-04). Many
// servers keep it bounded.
func TestHubFindToolsSaysWhatIsBehind(t *testing.T) {
	fresh(t)
	withHub(t, imageAndNotes("opencode", hubOnDemandOver+2))
	h := lanGuard(New().Handler())
	specs := listedSpecs(t, hubPost(h, "opencode", here, toolsList).Body.String())
	find, ok := specs[hubFindTool]
	if !ok || len(specs) != 2 {
		t.Fatalf("listed %v", specs)
	}
	first, _, _ := strings.Cut(find, "\n")
	if !strings.Contains(first, "magpie-image (Generate and edit images)") || !strings.Contains(first, "notes") {
		t.Fatalf("find_tools' first line doesn't say what is behind it: %q", first)
	}
	for _, x := range []string{"generate_image (Generate an image from a text prompt)", "note_op12", "query"} {
		if !strings.Contains(find, x) {
			t.Fatalf("find_tools' description has no %q: %s", x, find)
		}
	}

	// forty servers of twenty tools each: the description stays bounded
	many := &fakeHub{on: true, tools: map[string][]HubTool{}, servers: map[string][]HubServer{}}
	for i := range 40 {
		s := HubServer{Name: fmt.Sprintf("server%02d", i), About: "Does a long list of things for the user, each of them described at length", Known: true}
		for j := range 20 {
			name := fmt.Sprintf("tool_with_a_long_name_%d", j)
			many.tools["opencode"] = append(many.tools["opencode"], HubTool{Server: s.Name, Tool: name, Spec: hubSpec(name)})
			s.Tools = append(s.Tools, HubToolNote{Name: name, About: "Does something rather long to describe in a clause"})
		}
		many.servers["opencode"] = append(many.servers["opencode"], s)
	}
	withHub(t, many)
	find = listedSpecs(t, hubPost(h, "opencode", here, toolsList).Body.String())[hubFindTool]
	if max := hubLineMax + len(hubFindHow) + hubFindSayMax + 2; runeLen(find) > max || !strings.Contains(find, "server00") {
		t.Fatalf("with 40 servers find_tools' description is %d characters (at most %d): %s", runeLen(find), max, find)
	}
}
