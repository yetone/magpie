package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexUsageCompactionCounterDomains(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-compaction-counter-domains.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if reverse {
				for _, i := range []int{2, 8, 10} {
					lines[i], lines[i+1] = lines[i+1], lines[i]
				}
			}
			codexUsageLog(t, lines)
			cs := Calls(time.Time{})
			var total Tokens
			for _, c := range cs {
				if c.ResponseID == "" {
					t.Fatalf("extra legacy call: %+v", c)
				}
				total.add(c.Tokens)
			}
			if len(cs) != 4 || total != (Tokens{11157, 6856, 496640, 0}) {
				t.Fatalf("calls=%d tokens=%+v", len(cs), total)
			}
			if ss := List(0); len(ss) != 1 || ss[0].Tokens != total {
				t.Fatalf("summary mismatch: %+v", ss)
			}
			resetCalls()
			if got := Calls(time.Time{}); !reflect.DeepEqual(got, cs) {
				t.Fatal("reloaded calls differ")
			}
		})
	}
}

func TestCodexUsageCounterDomainOffsetChangesAndReload(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	legacy := func(n int) string { return cxUse(n*100, n*40, 0, n*10, n*2) }
	for _, reverse := range []bool{false, true} {
		for split := 0; split <= 7; split++ {
			t.Run(fmt.Sprintf("reverse=%v/split=%d", reverse, split), func(t *testing.T) {
				events := []string{
					cxCompactionLine(0, "compact-1", legacy(9), legacy(9)),
					cxRecordLine(1, "a", legacy(10), u), tokenCountLine(2, legacy(1), u),
					cxRecordLine(3, "compact-2", legacy(14), legacy(4)),
					cxCompactionLine(4, "compact-2", legacy(14), legacy(4)),
					cxRecordLine(5, "b", legacy(15), u), tokenCountLine(6, legacy(2), u),
				}
				if reverse {
					events[1], events[2], events[5], events[6] = events[2], events[1], events[6], events[5]
				}
				path := codexUsageLog(t, append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), events[:split]...))
				Calls(time.Time{})
				List(0)
				Reset()
				events = append(events[split:], tokenCountLine(7, legacy(3), u),
					cxRecordLine(8, "a", legacy(10), u), tokenCountLine(9, legacy(4), u),
					// Replaying compaction metadata must not change the offset.
					cxCompactionLine(4, "compact-2", legacy(14), legacy(4)),
					cxRecordLine(10, "c", legacy(18), u), tokenCountLine(11, legacy(5), u),
					tokenCountLine(2, legacy(1), u), tokenCountLine(6, legacy(2), u))
				appendText(t, path, strings.Join(events, "\n")+"\n")
				cs := Calls(time.Time{})
				if len(cs) != 7 {
					t.Fatalf("lost or duplicated requests: %+v", cs)
				}
				var named int
				for _, c := range cs {
					if c.ResponseID != "" {
						named++
					}
				}
				if named != 5 {
					t.Fatalf("named calls = %d", named)
				}
				if epoch := callCache[path].CX.Usage.Epoch; epoch != 0 {
					t.Fatalf("cross-domain reset: %d", epoch)
				}
				if ss := List(0); len(ss) != 1 || ss[0].Tokens != (Tokens{1080, 180, 720, 0}) {
					t.Fatalf("summary: %+v", ss)
				}
				resetCalls()
				if got := Calls(time.Time{}); !reflect.DeepEqual(got, cs) {
					t.Fatal("reloaded calls differ")
				}
			})
		}
	}
}

func TestCodexUsageIdenticalTokensAcrossInputBoundaryRemainDistinct(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			events := []string{cxRecordLine(1, "a", u, u), tokenCountLine(2, cxUse(200, 80, 0, 20, 4), u)}
			if reverse {
				events[0], events[1] = events[1], events[0]
			}
			// A new input proves another call even when it uses equal tokens.
			boundary := `{"timestamp":"` + stamp(2) + `","type":"event_msg","payload":{"type":"user_message","message":"next request"}}`
			events = []string{events[0], boundary, events[1]}
			codexUsageLog(t, append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), events...))
			if cs := Calls(time.Time{}); len(cs) != 2 {
				t.Fatalf("distinct calls merged: %+v", cs)
			}
		})
	}
}

func TestCodexUsageResumeCounterDomainsAndReload(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	total := func(n int) string { return cxUse(n*100, n*40, 0, n*10, n*2) }
	for _, reverse := range []bool{false, true} {
		for _, split := range []int{5, 6, 7, 8, 12, 13} {
			t.Run(fmt.Sprintf("reverse=%v/split=%d", reverse, split), func(t *testing.T) {
				events := []string{
					tokenCountLine(0, total(9), u), // its exact named response arrives after both resets
					cxRecordLine(1, "a", total(10), u), tokenCountLine(2, total(10), u),
					cxCompactionLine(3, "compact1", total(12), total(2)),
					tokenCountLine(4, total(10), total(0)),
					cxRecordLine(5, "b", total(13), u), tokenCountLine(6, total(1), u),
					cxRecordLine(7, "d", total(14), u), tokenCountLine(8, total(2), u),
					cxCompactionLine(9, "compact2", total(15), u),
					cxRecordLine(10, "f", total(16), u), tokenCountLine(11, total(3), u),
					cxRecordLine(12, "g", total(17), u), tokenCountLine(13, total(1), u),
					tokenCountLine(14, total(2), u), cxRecordLine(0, "late", total(9), u),
				}
				if reverse {
					events[5], events[6] = events[6], events[5]
					events[12], events[13] = events[13], events[12]
				}
				path := codexUsageLog(t, append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), events[:split]...))
				Calls(time.Time{})
				List(0)
				Reset()
				appendText(t, path, strings.Join(events[split:], "\n")+"\n")
				cs := Calls(time.Time{})
				if len(cs) != 9 {
					t.Fatalf("reset doubled/lost calls: %+v", cs)
				}
				if ss := List(0); len(ss) != 1 || ss[0].Tokens != (Tokens{600, 100, 400, 0}) {
					t.Fatalf("summary: %+v", ss)
				}
				Reset()
				if got := Calls(time.Time{}); !reflect.DeepEqual(got, cs) {
					t.Fatal("restart changed reset reconciliation")
				}
			})
		}
	}
}

func TestCodexUsageBothCountersResetDoesNotInventOffset(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	total := func(n int) string { return cxUse(n*100, n*40, 0, n*10, n*2) }
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "old", total(2), u), tokenCountLine(2, total(2), u), tokenCountLine(4, u, u), cxRecordLine(3, "reset", u, u))
	path := codexUsageLog(t, lines)
	Calls(time.Time{})
	List(0)
	Reset()
	appendText(t, path, cxRecordLine(5, "b", total(2), u)+"\n"+tokenCountLine(6, total(2), u)+"\n"+cxRecordLine(7, "c", total(3), u)+"\n"+tokenCountLine(8, total(3), u)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 4 {
		t.Fatalf("unproven reset offset applied: %+v", cs)
	}
}

func TestCodexUsageLateResponseBeforeCompaction(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	total := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"),
		tokenCountLine(2, u, u), cxCompactionLine(4, "compact", total, u), cxRecordLine(1, "late", u, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 2 || cs[1].ResponseID != "late" {
		t.Fatalf("late response not reconciled: %+v", cs)
	}
}

func TestCodexUsageEmbeddedCompactionBeforeTopLevelRecord(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"),
		cxCompactionLine(2, "compact", u, u), cxRecordLine(1, "compact", u, u),
		cxCompactionLine(2, "compact", u, u),
		cxRecordLine(3, "a", cxUse(200, 80, 0, 20, 4), u), tokenCountLine(4, u, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 2 {
		t.Fatalf("compaction replay duplicated usage: %+v", cs)
	}
	if ss := List(0); len(ss) != 1 || ss[0].Tokens != (Tokens{120, 20, 80, 0}) {
		t.Fatalf("summary: %+v", ss)
	}
}

func TestCodexUsageLocalPairDoesNotRequireCompactionMetadata(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"),
		`{"timestamp":"`+stamp(1)+`","type":"compacted","payload":{}}`,
		cxRecordLine(2, "a", cxUse(1000, 400, 0, 100, 20), u), tokenCountLine(3, u, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 1 {
		t.Fatalf("local pair counted twice: %+v", cs)
	}
}

func cxCompactionLine(sec int, id, total, usage string) string {
	return fmt.Sprintf(`{"timestamp":"%s","type":"compacted","payload":{"compaction_response_id":%q,"latest_token_usage_record":{"response_id":%q,"turn_id":"t1","usage":%s,"thread_token_usage":%s},"replacement_history":[{"role":"user","content":"synthetic"}]}}`, stamp(sec), id, id, usage, total)
}

func cxRecordLine(sec int, id, total, usage string) string {
	return fmt.Sprintf(`{"timestamp":"%s","type":"token_usage_record","payload":{"response_id":%q,"turn_id":"t1","usage":%s,"thread_token_usage":%s}}`, stamp(sec), id, usage, total)
}

func codexUsageLog(t *testing.T, lines []string) string {
	t.Helper()
	d := setupCalls(t)
	path := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, path, lines...)
	return path
}

func TestCodexUsageRealCheckpointPairs(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-usage-checkpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	if err = json.Unmarshal(b, &events); err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			lines := []string{`{"timestamp":"2026-10-03T08:49:00Z","type":"session_meta","payload":{"id":"thread","session_id":"synthetic-2"}}`, `{"timestamp":"2026-10-03T08:49:00Z","type":"turn_context","payload":{"turn_id":"synthetic-1","model":"gpt-6-astra"}}`}
			order := []int{0, 1, 2, 3}
			if reverse {
				order = []int{1, 0, 3, 2}
			}
			for _, i := range order {
				var v any
				json.Unmarshal(events[i], &v)
				compact, _ := json.Marshal(v)
				lines = append(lines, string(compact))
			}
			codexUsageLog(t, lines)
			cs := Calls(time.Time{})
			if len(cs) != 2 {
				t.Fatalf("calls: %+v", cs)
			}
			var total Tokens
			for _, c := range cs {
				total.add(c.Tokens)
				if c.ResponseID == "" || c.RequestID != "" {
					t.Fatalf("identity: %+v", c)
				}
			}
			if total != (Tokens{4515, 1084, 299776, 0}) {
				t.Fatal(total)
			}
			if cs[0].Time.Format(time.RFC3339Nano) != "2026-10-03T08:49:56.408Z" || cs[1].Time.Format(time.RFC3339Nano) != "2026-10-03T08:49:31.433Z" {
				t.Fatalf("completion times: %+v", cs)
			}
			ss := List(0)
			if len(ss) != 1 || ss[0].Tokens != total {
				t.Fatalf("summary: %+v; calls %+v", ss, total)
			}
		})
	}
}

func TestCodexUsageMixedReplayAndReload(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	third := cxUse(300, 120, 0, 30, 6)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), tokenCountLine(3, u, u))
	path := codexUsageLog(t, lines)
	before := Calls(time.Time{})
	if len(before) != 1 {
		t.Fatal(before)
	}
	resetCalls()
	appendText(t, path, cxRecordLine(2, "a", u, u)+"\n"+cxRecordLine(4, "b", twice, u)+"\n"+tokenCountLine(5, twice, u)+"\n"+tokenCountLine(6, third, u)+"\n"+cxRecordLine(2, "a", u, u)+"\n")
	got := Calls(time.Time{})
	if len(got) != 3 {
		t.Fatalf("mixed lost/duplicated calls: %+v", got)
	}
	if got[2].ResponseID != "a" || got[2].Time != callT0.Add(2*time.Second) {
		t.Fatal(got[2])
	}
	if got[1].ResponseID != "b" || got[0].ResponseID != "" {
		t.Fatal(got)
	}
	if before[0].ResponseID != "" {
		t.Fatal("published snapshot was mutated")
	}
	resetCalls()
	if again := Calls(time.Time{}); !reflect.DeepEqual(again, got) {
		t.Fatalf("reload mismatch: %+v", again)
	}
}

func TestCodexUsageNamedZeroAndEqualUsageRemainDistinct(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	zero := cxUse(0, 0, 0, 0, 0)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u), cxRecordLine(2, "b", twice, u), cxRecordLine(3, "z", twice, zero), cxRecordLine(1, "a", u, u))
	codexUsageLog(t, lines)
	got := Calls(time.Time{})
	if len(got) != 3 || got[0].ResponseID != "z" || !got[0].Tokens.zero() {
		t.Fatal(got)
	}
}

func TestCodexUsageRevisionsKeepCompletionAndRejectOldReplay(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	v := cxUse(100, 40, 0, 20, 3)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(2, "a", u, u), cxRecordLine(4, "a", v, v), cxRecordLine(2, "a", u, u))
	codexUsageLog(t, lines)
	cs := Calls(time.Time{})
	if len(cs) != 1 || cs[0].Output != 20 || cs[0].Time != callT0.Add(2*time.Second) {
		t.Fatal(cs)
	}
	ss := List(0)
	if len(ss) != 1 || ss[0].Output != 20 {
		t.Fatal(ss)
	}
}

func TestCodexUsageUnpairedCountKeepsLegacyDelta(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	tripled := cxUse(300, 120, 0, 30, 6)
	// The named response has its checkpoint. Later cumulative usage lacks
	// last usage; retain the old delta rather than silently losing 200 tokens.
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u), tokenCountLine(2, u, u), tokenCountLine(3, tripled, "null"))
	codexUsageLog(t, lines)
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[0].Tokens != spent(mustCXUsage(t, twice).raw()) {
		t.Fatal(cs)
	}
}

func mustCXUsage(t *testing.T, text string) cxUsage {
	t.Helper()
	var usage cxUsage
	if err := json.Unmarshal([]byte(text), &usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestCodexUsageLateCallRevokesTurnTiming(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u), cxDoneLine(3, "t1", 3000, 300))
	path := codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].TTFT != 300 {
		t.Fatal(cs)
	}
	resetCalls()
	appendText(t, path, cxRecordLine(2, "b", twice, u)+"\n")
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[0].TTFT != 0 || cs[1].TTFT != 0 || cs[1].Millis != 1000 {
		t.Fatal(cs)
	}
}

func TestCodexUsageNoLastBaselineAndLegacyReset(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), tokenCountLine(1, u, "null"), tokenCountLine(2, twice, u), tokenCountLine(3, u, u), tokenCountLine(4, u, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 3 {
		t.Fatal(cs)
	}
}

func TestCodexUsageSummaryCloneAndJSON(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	s := &state{}
	for _, l := range append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u)) {
		codexLine(s, []byte(l), true)
	}
	c := s.clone()
	codexLine(c, []byte(cxRecordLine(2, "b", cxUse(200, 80, 0, 20, 4), u)), true)
	if len(s.Codex.Entries) != 1 || len(c.Codex.Entries) != 2 {
		t.Fatal("clone shared contributions")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var restored state
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Models, c.Models) || restored.Codex != nil || strings.Contains(string(b), "Entries") {
		t.Fatal("summary cache must retain totals without per-response observations")
	}
}

func TestCodexUsageAppendFramesStaySmall(t *testing.T) {
	var lines []string
	lines = append(lines, cxTurnLines(0, "t1", "gpt-6-astra", "high")...)
	for i := 1; i <= 2000; i++ {
		lines = append(lines, cxRecordLine(i, fmt.Sprint(i), cxUse(i*100, i*40, 0, i*10, i*2), cxUse(100, 40, 0, 10, 2)))
	}
	path := codexUsageLog(t, lines)
	Calls(time.Time{})
	before, err := os.Stat(callCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	appendText(t, path, cxRecordLine(2001, "new", cxUse(200100, 80040, 0, 20010, 4002), cxUse(100, 40, 0, 10, 2))+"\n")
	want := Calls(time.Time{})
	after, err := os.Stat(callCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size()-before.Size() > 16<<10 {
		t.Fatalf("append saved old identities again: %d bytes", after.Size()-before.Size())
	}
	resetCalls()
	if got := Calls(time.Time{}); !reflect.DeepEqual(got, want) {
		t.Fatal("incremental frame lost identities")
	}
}

func TestCodexUsageLateCheckpointAndResponseCounterReset(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u), cxRecordLine(3, "b", twice, u), tokenCountLine(2, u, u), cxRecordLine(5, "c", u, u), tokenCountLine(6, u, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 3 {
		t.Fatal(cs)
	}
}

func TestCodexUsageTurnContextForLateResponse(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	lines := cxTurnLines(0, "t1", "gpt-6-astra", "high")
	lines = append(lines, cxTurnLines(3, "t2", "gpt-6-luna", "low")...)
	lines = append(lines, cxRecordLine(2, "a", u, u))
	codexUsageLog(t, lines)
	cs := Calls(time.Time{})
	if len(cs) != 1 || cs[0].Model != "gpt-6-astra" || cs[0].Effort != "high" {
		t.Fatal(cs)
	}
}

func TestCodexUsageDelayedOlderCountDoesNotResetEpoch(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u), cxRecordLine(2, "b", twice, u), tokenCountLine(5, u, u), tokenCountLine(6, twice, u))
	codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 2 {
		t.Fatal(cs)
	}
}

func TestCodexUsageMissingCheckpointDoesNotInventRequest(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	twice := cxUse(200, 80, 0, 20, 4)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", "null", u), tokenCountLine(2, u, u))
	path := codexUsageLog(t, lines)
	if cs := Calls(time.Time{}); len(cs) != 1 {
		t.Fatal(cs)
	}
	if callCache[path].CX.Usage.Entries[0].CountTotal == nil {
		t.Fatal("local pair did not retain its legacy checkpoint")
	}
	resetCalls()
	appendText(t, path, tokenCountLine(3, twice, u)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 2 || cs[0].ResponseID != "" {
		t.Fatal("later legacy call suppressed", cs)
	}
}

func TestCodexUsageThreadSettingsReachBothReaders(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	lines := cxTurnLines(0, "t1", "gpt-6-astra", "high")
	lines = append(lines, `{"timestamp":"`+stamp(1)+`","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-6-luna"}}}`, cxRecordLine(2, "a", u, u))
	codexUsageLog(t, lines)
	cs := Calls(time.Time{})
	ss := List(0)
	if len(cs) != 1 || cs[0].Model != "gpt-6-luna" || len(ss) != 1 || model(ss[0], "gpt-6-luna").Output != 10 {
		t.Fatalf("calls=%+v summary=%+v", cs, ss)
	}
}

func TestCodexUsageRepeatedSnapshotAdvancesRevisionAcrossReload(t *testing.T) {
	u := cxUse(100, 40, 0, 10, 2)
	lines := append(cxTurnLines(0, "t1", "gpt-6-astra", "high"), cxRecordLine(1, "a", u, u))
	path := codexUsageLog(t, lines)
	Calls(time.Time{})
	appendText(t, path, cxRecordLine(5, "a", u, u)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].Time != callT0.Add(time.Second) {
		t.Fatal("repeat moved completion", cs)
	}
	resetCalls()
	appendText(t, path, cxRecordLine(4, "a", u, cxUse(0, 0, 0, 0, 0))+"\n"+cxRecordLine(5, "a", u, cxUse(100, 40, 0, 99, 2))+"\n")
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].Output != 10 || cs[0].Time != callT0.Add(time.Second) {
		t.Fatal("older/tied conflict survived restart", cs)
	}
	if ss := List(0); len(ss) != 1 || ss[0].Output != 10 {
		t.Fatal("summary accepted old conflict", ss)
	}
	resetCalls()
	updated := cxUse(100, 40, 0, 11, 2)
	appendText(t, path, cxRecordLine(6, "a", updated, updated)+"\n")
	if cs := Calls(time.Time{}); len(cs) != 1 || cs[0].Output != 11 || cs[0].Time != callT0.Add(time.Second) {
		t.Fatal("newer correction was lost", cs)
	}
}
