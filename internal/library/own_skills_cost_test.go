package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// zysicyj on X (Windows): ten agents, each with some forty skills of its
// own by the library's names, and each click of the warning's "Use the
// library's" or "Keep X's" took tens of seconds. The click looked every
// agent up again for each skill (Takes, targetByID, sharing: agent
// detection and every agent's folder resolved, once a name), and the sync
// after it resolved every agent's folder again for each skill of each
// agent. Both are once an agent now: what a click and its sync look at
// grows with the agents and skills, not with their square.
func TestOwnSkillsClickLooksOnce(t *testing.T) {
	h := sandbox(t)
	for _, d := range []string{".claude", ".codex", ".gemini", ".config/opencode", ".pi/agent", ".cursor", ".copilot", ".config/goose", ".config/crush"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, tg := range Targets() {
		if tg.Skills != "" && tg.Desktop == nil {
			ids = append(ids, tg.Agent.ID)
		}
	}
	if len(ids) < 6 {
		t.Fatalf("agents with skills: %v, want several", ids)
	}
	var names []string
	for i := range 20 {
		names = append(names, fmt.Sprintf("skill-%02d", i))
		if _, err := createSkill(names[i], "the library's", nil, "body\n", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, tg := range Targets() {
		if tg.Skills == "" || tg.Desktop != nil {
			continue
		}
		for _, n := range names {
			skill(t, filepath.Join(tg.Skills, n), n, "own of "+tg.Agent.ID)
		}
	}
	r, err := EverySkillAgents(ids, true)
	if err != nil {
		t.Fatal(err)
	}
	own := 0
	for _, p := range r.Problems {
		if p.Own {
			own++
		}
	}
	if own < len(names)*4 {
		t.Fatalf("%d own skills in the way, want the agents' forty-odd each", own)
	}

	folders := map[string]bool{}
	for _, tg := range Targets() {
		if tg.Skills != "" {
			folders[realDir(tg.Skills)] = true // resolve looks at the real path
		}
	}
	look, detect := lookAt, detectAgents
	t.Cleanup(func() { lookAt, detectAgents = look, detect })
	var looks, atFolders, finds atomic.Int64
	lookAt = func(p string) (part, bool) {
		looks.Add(1)
		if folders[p] {
			atFolders.Add(1)
		}
		return look(p)
	}
	detectAgents = func() []*agent.Agent {
		finds.Add(1)
		return detect()
	}
	count := func(f func() (*Result, error)) int64 {
		looks.Store(0)
		atFolders.Store(0)
		finds.Store(0)
		if _, err := f(); err != nil {
			t.Fatal(err)
		}
		return looks.Load()
	}
	pairs := int64(len(ids) * len(names))
	sync := count(Sync)
	syncFinds, syncFolders := finds.Load(), atFolders.Load()
	keep := count(func() (*Result, error) { return KeepAgentSkills(names, ids[1]) })
	keepFinds, keepFolders := finds.Load(), atFolders.Load()
	use := count(func() (*Result, error) { return UseLibrarySkills(names, ids[1]) })
	useFinds, useFolders := finds.Load(), atFolders.Load()
	after := count(Sync)
	afterFolders := atFolders.Load()
	t.Logf("%d agents × %d skills: %d looks in a sync, %d in keep, %d in use, %d in the sync after; at the agents' folders %d, %d, %d, %d; agents found %d times in a sync, %d in keep, %d in use",
		len(ids), len(names), sync, keep, use, after, syncFolders, keepFolders, useFolders, afterFolders, syncFinds, keepFinds, useFinds)
	// the agents are found as often as a sync finds them, and a few times
	// more: not once for each skill settled
	for _, c := range []struct {
		what string
		n    int64
	}{{"keep", keepFinds}, {"use", useFinds}} {
		if c.n > syncFinds+clickFinds {
			t.Errorf("%s found the agents %d times, a sync %d: once a skill", c.what, c.n, syncFinds)
		}
	}
	// a sync resolves each agent's folder once for each of its skills
	// (the skill's own path goes through it), not once more for each
	// skill of each other agent
	if syncFolders > pairs*folderLooksAPair {
		t.Errorf("a sync looked at the agents' folders %d times, %d for each agent's skill (want at most %d)", syncFolders, syncFolders/pairs, folderLooksAPair)
	}
	// a click is a sync and what it looks at for each skill it settles:
	// the agents' folders are resolved once, not once a skill
	for _, c := range []struct {
		what string
		n    int64
	}{{"keep", keepFolders}, {"use", useFolders}} {
		if over := c.n - max(syncFolders, afterFolders); over > int64(len(ids))*clickFolderLooks {
			t.Errorf("%s looked at the agents' folders %d times beyond a sync's (want at most %d)", c.what, over, int64(len(ids))*clickFolderLooks)
		}
	}
}

// Measured with 12 agents and 20 skills on macOS: a sync looks at the
// agents' folders 7 times for each agent's skill (13 before), a click
// none beyond its sync (85 more for each agent before), and a click finds
// the agents 3 times (41 before, once a skill).
const (
	folderLooksAPair = 10
	clickFolderLooks = 20
	clickFinds       = 5
)
