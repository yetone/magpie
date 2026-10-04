package sessions

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func snapshotMeta() string {
	return fmt.Sprintf(`{"timestamp":"%s","type":"session_meta","payload":{"id":"child","session_id":"parent","history_mode":"paginated","subagent_history_start_ordinal":219}}`, stamp(0))
}

func snapshotCompaction() string {
	return strings.Replace(cxCompactionLine(0, "snapshot", cxUse(1000, 500, 0, 100, 0), cxUse(30, 10, 0, 2, 0)), `"response_id":"snapshot"`, `"thread_id":"child","response_id":"snapshot"`, 1)
}

func TestCodexUsagePaginatedSnapshotBoundary(t *testing.T) {
	u := cxUse(100, 50, 0, 10, 0)
	for _, before := range []bool{false, true} {
		for split := 1; split <= 8; split++ {
			t.Run(fmt.Sprintf("recordBefore=%v/split=%d", before, split), func(t *testing.T) {
				lines := []string{snapshotMeta()}
				if before {
					lines = append(lines, cxRecordLine(0, "snapshot", cxUse(1000, 500, 0, 100, 0), cxUse(30, 10, 0, 2, 0)))
				}
				lines = append(lines, snapshotCompaction(), tokenCountLine(0, cxUse(900, 450, 0, 90, 0), cxUse(0, 0, 0, 0, 0)))
				lines = append(lines, cxTurnLines(1, "t1", "gpt-6-astra", "high")...)
				lines = append(lines,
					cxRecordLine(2, "a", cxUse(1100, 550, 0, 110, 0), u), tokenCountLine(2, cxUse(1000, 500, 0, 100, 0), u),
					cxCompactionLine(3, "new-compaction", cxUse(1300, 580, 0, 130, 0), cxUse(200, 30, 0, 20, 0)),
					cxRecordLine(4, "b", cxUse(1400, 630, 0, 140, 0), u), tokenCountLine(4, cxUse(1100, 550, 0, 110, 0), u),
					// A real legacy-only request with equal usage remains a new call.
					tokenCountLine(5, cxUse(1200, 600, 0, 120, 0), u))
				path := codexUsageLog(t, lines[:split])
				Calls(time.Time{})
				List(0)
				Reset()
				appendText(t, path, strings.Join(lines[split:], "\n")+"\n")
				cs := Calls(time.Time{})
				if len(cs) != 5 {
					t.Fatalf("calls=%+v", cs)
				}
				var total Tokens
				for _, c := range cs {
					total.add(c.Tokens)
				}
				if total != (Tokens{340, 52, 190, 0}) {
					t.Fatalf("tokens=%+v", total)
				}
				if ss := List(0); len(ss) != 1 || ss[0].Tokens != total {
					t.Fatalf("summary=%+v", ss)
				}
				Reset()
				if got := Calls(time.Time{}); !reflect.DeepEqual(cs, got) {
					t.Fatal("snapshot state changed after reload")
				}
			})
		}
	}
}

func TestCodexUsageSnapshotNeedsSameBoundary(t *testing.T) {
	u := cxUse(100, 50, 0, 10, 0)
	for _, kind := range []string{"different_time", "different_thread", "missing_field", "not_paginated", "new_response"} {
		t.Run(kind, func(t *testing.T) {
			meta, compacted := snapshotMeta(), snapshotCompaction()
			zero := tokenCountLine(0, cxUse(900, 450, 0, 90, 0), cxUse(0, 0, 0, 0, 0))
			switch kind {
			case "different_time":
				zero = strings.Replace(zero, stamp(0), stamp(1), 1)
			case "different_thread":
				compacted = strings.Replace(compacted, `"thread_id":"child"`, `"thread_id":"another"`, 1)
			case "missing_field":
				zero = strings.ReplaceAll(zero, `"reasoning_output_tokens":0,`, "")
			case "not_paginated":
				meta = strings.Replace(meta, `"paginated"`, `"full"`, 1)
			}
			lines := []string{meta, compacted}
			if kind == "new_response" {
				lines = append(lines, cxRecordLine(0, "intervening", cxUse(1010, 505, 0, 101, 0), cxUse(10, 5, 0, 1, 0)))
			}
			lines = append(lines, zero, cxRecordLine(2, "a", cxUse(1100, 550, 0, 110, 0), u), tokenCountLine(2, cxUse(1000, 500, 0, 100, 0), u))
			codexUsageLog(t, lines)
			want := 3
			if kind == "new_response" {
				want++
			}
			if cs := Calls(time.Time{}); len(cs) != want {
				t.Fatalf("unproven offset accepted: %+v", cs)
			}
		})
	}
}
