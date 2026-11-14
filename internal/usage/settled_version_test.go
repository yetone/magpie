package usage

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// settledCounted is the hash of what a cold start keeps of the fixture's
// settled days, under settledCountVersion. A change that counts them
// otherwise changes it: raise settledCountVersion and set this to the new
// hash in the same commit.
const settledCounted = "e27e12fd0fb0b8589936f2a562ae602f0cc2f1368d2e7a8f4668cdff6afe5952"

// The settled days, a page's cells and Codex's effort are kept on disk
// under settledCountVersion, not under this package's source: an upgrade
// that changes the source and counts the same keeps them, and every
// user's next start pairs only the last days (#2278). One that counts a
// row otherwise has to raise it, or a start reads the old counts: this
// fails then.
func TestSettledCountedAsVersioned(t *testing.T) {
	zone := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = zone })
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	for i, days := range []int{40, 20, 10, 5, 0} {
		id := string(rune('a' + i))
		at := now.AddDate(0, 0, -days).Add(-time.Hour)
		path := filepath.Join(sessions.CodexDir(), "sessions", at.Format("2006/01/02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		effort := []string{"low", "high", "medium", "low", "xhigh"}[i]
		writeCodexCall(t, path, id, "s"+id, `"vscode"`, "resp-"+id, "m", effort, at, 100+i, 10+i)
		Append(Record{Time: at.Add(time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 100 + i, Output: 10 + i, CacheRead: 7 * i, Status: 200, ResponseID: "resp-" + id, Session: "s" + id})
	}
	Append(Record{Time: now.AddDate(0, 0, -30), Agent: "claude", Provider: "openai", Model: "m", Input: 40, Output: 20, CacheRead: 300, CacheWrite: 50, Status: 200, UpBytes: 2400, DownBytes: 800, ImageFilesSaved: 300})
	Append(Record{Time: now.AddDate(0, 0, -25), Agent: "claude", Provider: "openai", Model: "m", Input: 9, Status: 500})
	codexEffort.Lock()
	codexEffort.periods = map[Period]*effortEntry{}
	codexEffort.Unlock()
	codexEffortBuilding.Lock()
	effortOlds = map[Period]*effortOld{}
	codexEffortBuilding.Unlock()
	restart := func() {
		requestCache.Lock()
		requestCache.parts, requestCache.pageOld, requestCache.pairs, requestCache.pages = nil, nil, nil, map[pageKey]RequestPage{}
		requestCache.heatParts, requestCache.heatOld, requestCache.heats = nil, nil, nil
		requestCache.Unlock()
	}
	restart()
	t.Cleanup(restart)
	QueryPage(All, Filter{}, 0, 1)
	HeatmapOf(Filter{})
	CodexModelEffort(All)

	read := func(path string, v any) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	var pages []pageDiskEntry
	var heat heatDiskEntry
	var efforts []effortDiskEntry
	read(pageDiskPath(), &pages)
	read(heatDiskPath(), &heat)
	read(effortDiskPath(), &efforts)
	var effort effortDiskEntry
	if len(efforts) > 0 {
		effort = efforts[0]
	}
	// when and under what they were counted, not how; and the cells in
	// one order, as a first build a few weeks at a time adds them up in
	// another
	for i := range pages {
		pages[i].Key, pages[i].At, pages[i].Cut, pages[i].Since = "", time.Time{}, time.Time{}, time.Time{}
		slices.SortFunc(pages[i].Cells, func(a, b pageDiskCell) int {
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			return bytes.Compare(x, y)
		})
	}
	if len(pages) == 0 || len(heat.Days) == 0 || len(effort.Sums) == 0 {
		t.Fatalf("kept %d pages, %d days, %d threads' effort", len(pages), len(heat.Days), len(effort.Sums))
	}
	b, err := json.Marshal([]any{pages, heat.Days, effort.Sums})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != settledCounted {
		t.Fatalf("the settled days are counted otherwise (%s): raise settledCountVersion (%q) and set settledCounted to %q\n%s", settledCounted, settledCountVersion, got, b)
	}
}
