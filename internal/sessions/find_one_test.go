package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hdhfhf_hdh219 on X: over ~3000 sessions each `magpie sessions show
// agent:id --from N` took 4-5s, every agent's files listed and the newest
// 200 sessions put together for the one asked for. FindOne reads a
// Claude Code or Codex session from its agent's files alone, with no pass
// over every agent's (allFiles), and gives what Find gives: copies,
// branches, subagents, workflow agents, a Codex thread's identity, one
// nothing was said in.
func TestFindOneIsFindWithoutAFullPass(t *testing.T) {
	claude, codex := setup(t)
	// a workflow's agent beside the base fixture's subagent
	app := filepath.Join(claude, "projects", "-work-app", "11111111-2222-3333-4444-555555555555", "subagents")
	b, err := os.ReadFile(filepath.Join(app, "agent-a1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(app, "workflows", "run-1", "agent-w1.jsonl"), strings.TrimSpace(string(b)))
	writeLines(t, filepath.Join(app, "workflows", "run-1", "journal.jsonl"), `{"type":"journal"}`)
	// a session nothing was said in
	writeLines(t, filepath.Join(claude, "projects", "-work-app", "33333333-2222-3333-4444-555555555555.jsonl"), `{"type":"summary","summary":"x"}`)
	// a Codex fork and its parent: identities read across rollouts
	for _, r := range []struct{ fixture, day, stamp, id string }{
		{"codex-fork-parent.jsonl", "23", "10-00-00", "019d186b-a100-7aaa-8bbb-cccdddeeefff"},
		{"codex-fork-child.jsonl", "24", "17-30-00", "019d1f2d-f9c0-7111-8222-333444555666"},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", r.fixture))
		if err != nil {
			t.Fatal(err)
		}
		writeLines(t, filepath.Join(codex, "sessions", "2026", "03", r.day, "rollout-2026-03-"+r.day+"T"+r.stamp+"-"+r.id+".jsonl"), strings.TrimSpace(string(b)))
	}
	// copies (compacted-branch twice, tasks' forks) and branches (the
	// forks of original that went on)
	ccForkFixture(t, claude,
		[2]string{"original.jsonl", ccOriginal},
		[2]string{"fork-on.jsonl", ccForkOn},
		[2]string{"fork-other.jsonl", ccForkOther},
		[2]string{"compacted-branch.jsonl", ccBranch},
		[2]string{"compacted-branch.jsonl", ccCopyIDs[0]},
		[2]string{"compacted.jsonl", ccCompacted},
		[2]string{"tasks.jsonl", ccTaskSession},
		[2]string{"tasks-fork.jsonl", ccTasksFork},
		[2]string{"tasks-fork-2.jsonl", ccTasksFork2},
		[2]string{"tasks-branch.jsonl", ccTasksBranch})

	// the copies, which no list shows, as Get finds them
	ids := [][2]string{{"claude", ccOriginal}, {"claude", ccBranch}, {"claude", ccCopyIDs[0]}}
	for _, agent := range []string{"claude", "codex"} {
		for _, m := range ListAgent(agent) {
			ids = append(ids, [2]string{agent, m.ID})
		}
	}
	if len(ids) < 16 {
		t.Fatalf("only %d sessions listed: %v", len(ids), ids)
	}
	var copies, branches, subs, roles int
	for _, k := range ids {
		agent, id := k[0], k[1]
		// as a new process finds it: nothing in memory, the cache on disk
		Reset()
		passes := FullPasses()
		one, ok := FindOne(agent, id)
		if !ok {
			t.Errorf("FindOne(%s): not found", agent+":"+id)
			continue
		}
		if n := FullPasses() - passes; n != 0 {
			t.Errorf("FindOne(%s) listed every agent's files %d times", agent+":"+id, n)
		}
		Reset()
		want, _ := Find(agent, id)
		g, _ := json.Marshal(one)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("FindOne(%s) =\n%s\nFind gives\n%s", agent+":"+id, g, w)
		}
		copies += len(one.Copies)
		subs += len(one.Subagents)
		if one.Role == "branch" {
			branches++
		} else if one.Role != "" {
			roles++
		}
	}
	// what the fixtures are for is in what was compared
	if copies == 0 || branches == 0 || subs < 2 || roles == 0 {
		t.Errorf("compared %d copies, %d branches, %d subagents, %d Codex roles: a fixture no longer makes one", copies, branches, subs, roles)
	}
	// one that isn't there is Find's: nothing
	if _, ok := FindOne("claude", "nope"); ok {
		t.Error("FindOne found claude:nope")
	}
}
