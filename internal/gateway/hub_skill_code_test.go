package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// skillHub is a fakeHub that also keeps skills with code, and records the
// skills saved to it.
type skillHub struct {
	*fakeHub
	smu    sync.Mutex
	skills map[string][]HubSkill // by agent
	saved  []string
	refuse error
}

func (s *skillHub) Skills(agent string) []HubSkill {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.skills[agent]
}

func (s *skillHub) SaveSkill(agent, name, about, code string) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	if s.refuse != nil {
		return s.refuse
	}
	s.saved = append(s.saved, agent+" "+name+" "+about)
	s.skills[agent] = append(s.skills[agent], HubSkill{Name: name, About: about, Code: code})
	return nil
}

// skillCodeHub is codeHub's hub with skills, and a run that takes
// run_code's whole arguments.
func skillCodeHub(t *testing.T, skills []HubSkill) (*skillHub, func(args map[string]any) mcpReply) {
	t.Helper()
	f, _ := codeHub(t, func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		if tool == "search_issues" {
			return json.RawMessage(`{"content":[{"type":"text","text":"[{\"number\":1,\"merged\":true,\"title\":\"a\"},{\"number\":2,\"merged\":false,\"title\":\"b\"},{\"number\":3,\"merged\":true,\"title\":\"c\"}]"}]}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	})
	sh := &skillHub{fakeHub: f, skills: map[string][]HubSkill{"opencode": skills}}
	withHub(t, sh)
	h := lanGuard(New().Handler())
	return sh, func(args map[string]any) mcpReply {
		b, _ := json.Marshal(args)
		return readMCP(t, hubPost(h, "opencode", here, toolsCall(hubCodeTool, string(b))))
	}
}

// A skill's code.js is skills.<name>(args) in run_code: its default
// export, given the object of args, with what its file declares before
// and after it in scope, calling the agent's tools as the script does.
// run_code's description names it with its description. One with no
// default export, or a name no skill has, throws a reason the script can
// catch; `args` is the arguments' own args.
func TestHubCodeRunsSkills(t *testing.T) {
	report := "const keep = (p) => p.merged\n" +
		"export default async ({ repo }) => {\n" +
		"  const prs = await tools.github__search_issues({ repo })\n" +
		"  return titles(prs.filter(keep))\n" +
		"}\n" +
		"function titles(ps) { return ps.map(p => p.title) }\n"
	f, run := skillCodeHub(t, []HubSkill{
		{Name: "weekly-report", About: "This week's merged PRs. args: {repo}", Code: report},
		{Name: "broken", Code: "return 1"},
	})
	h := lanGuard(New().Handler())
	var desc string
	var listed struct {
		Result struct {
			Tools []struct{ Name, Description string }
		}
	}
	if err := json.Unmarshal(hubPost(h, "opencode", here, toolsList).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, x := range listed.Result.Tools {
		if x.Name == hubCodeTool {
			desc = x.Description
		}
	}
	if !strings.Contains(desc, "skills.weekly_report (This week's merged PRs. args: {repo})") || !strings.Contains(desc, "skills.broken") || !strings.Contains(desc, "save_as") {
		t.Fatalf("run_code's description: %s", desc)
	}

	got := run(map[string]any{"code": `return await skills.weekly_report({repo: args.repo})`, "args": map[string]any{"repo": "x/y"}})
	if got.Result.IsError || unspaced(codeText(got)) != `["a","c"]` {
		t.Fatalf("weekly_report: %+v", got)
	}
	if ran := f.ran(); len(ran) != 1 || ran[0] != `opencode github search_issues {"repo":"x/y"}` {
		t.Fatalf("ran %q", ran)
	}
	if got := run(map[string]any{"code": `return await skills["weekly-report"]({repo: "z"})`}); got.Result.IsError || unspaced(codeText(got)) != `["a","c"]` {
		t.Fatalf("by its own name: %+v", got)
	}
	for code, want := range map[string]string{
		`try { await skills.broken() } catch (e) { return e.message }`:    "the skill broken can't be run: its code.js has no `export default",
		`try { await skills.nothing({}) } catch (e) { return e.message }`: "no skill with code is called nothing",
		`return typeof args === "object" && Object.keys(args).length`:     "0",
	} {
		if got := run(map[string]any{"code": code}); got.Result.IsError || !strings.Contains(codeText(got), want) {
			t.Fatalf("%s gave %+v, want %q", code, got, want)
		}
	}
	if got := run(map[string]any{"code": `return 1`, "args": []int{1}}); !got.Result.IsError {
		t.Fatalf("args not an object: %+v", got)
	}
}

// save_as saves code that ran without an error as a skill of the agent's,
// its code.js an async function of args whose body is the code, and the
// result says so; the next run_code calls it. Code that threw isn't
// saved, and the hub's refusal is said.
func TestHubCodeSavesSkill(t *testing.T) {
	sh, run := skillCodeHub(t, nil)
	save := map[string]any{"name": "merged-titles", "description": "Merged PR titles. args: {repo}"}
	got := run(map[string]any{
		"code":    "const prs = await tools.github__search_issues({repo: args.repo})\nreturn prs.filter(p => p.merged).length",
		"args":    map[string]any{"repo": "x/y"},
		"save_as": save,
	})
	if got.Result.IsError || !strings.HasPrefix(codeText(got), "2") || !strings.Contains(codeText(got), "Saved as the skill merged-titles: skills.merged_titles(args)") {
		t.Fatalf("save: %+v", got)
	}
	if len(sh.saved) != 1 || sh.saved[0] != "opencode merged-titles Merged PR titles. args: {repo}" {
		t.Fatalf("saved %q", sh.saved)
	}
	if got := run(map[string]any{"code": `return await skills.merged_titles({repo: "x/y"})`}); got.Result.IsError || codeText(got) != "2" {
		t.Fatalf("the saved skill: %+v", got)
	}

	if got := run(map[string]any{"code": `throw new Error("no")`, "save_as": map[string]any{"name": "bad", "description": "d"}}); !got.Result.IsError || !strings.Contains(codeText(got), "Not saved as a skill") {
		t.Fatalf("a script that threw: %+v", got)
	}
	sh.smu.Lock()
	sh.refuse = errors.New("there is a skill called merged-titles already")
	sh.smu.Unlock()
	if got := run(map[string]any{"code": `return 1`, "save_as": save}); !strings.Contains(codeText(got), "Not saved as a skill: there is a skill called") {
		t.Fatalf("refused: %+v", got)
	}
	if len(sh.saved) != 1 {
		t.Fatalf("saved %q", sh.saved)
	}
}

// unspaced is text without its whitespace: a returned value is shown
// indented.
func unspaced(text string) string { return strings.Join(strings.Fields(text), "") }
