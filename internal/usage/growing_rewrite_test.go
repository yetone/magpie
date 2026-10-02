package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLogGrowingRewriteThenAppend(t *testing.T) {
	for _, writer := range []string{"this process", "another writer"} {
		t.Run(writer, func(t *testing.T) {
			pageHome(t)
			historyLog(t, logBlockRows*2+10)
			for _, period := range []Period{Today, Week, Month, All} {
				Summarize(period)
				QueryPage(period, Filter{}, 0, 100)
			}
			Vias(time.Time{})
			old := readLogSnapshot()
			data, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			_, rest, ok := bytes.Cut(data, []byte{'\n'})
			if !ok {
				t.Fatal("fixture has no complete first line")
			}
			// Change an early record and grow the same inode. Treating this as an
			// append would reuse stale blocks and seek into the rewritten prefix.
			edited := Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Session: "edited-prefix", Input: 73, Status: 200, RequestID: strings.Repeat("r", 2048)}
			first, err := json.Marshal(edited)
			if err != nil {
				t.Fatal(err)
			}
			rewritten := append(append(first, '\n'), rest...)
			if len(rewritten) <= len(data) {
				t.Fatal("rewrite must grow the log")
			}
			if err := os.WriteFile(Path(), rewritten, 0600); err != nil {
				t.Fatal(err)
			}
			added := Record{Time: time.Now(), Agent: "codex", Provider: "relay", Model: "m", Session: "appended-tail", Input: 19, Status: 200}
			if writer == "this process" {
				Append(added)
			} else {
				f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = json.NewEncoder(f).Encode(added)
				closeErr := f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			cold := Load(time.Time{})
			if len(cold) != logBlockRows*2+11 {
				t.Fatalf("fixture rows: %d", len(cold))
			}
			for _, period := range []Period{Today, Week, Month, All} {
				got, want := Summarize(period), summarize(period, time.Now(), cold)
				if got.Totals != want.Totals {
					t.Fatalf("%s summary stale: got %+v want %+v", period, got.Totals, want.Totals)
				}
				equalPage(t, QueryPage(period, Filter{}, 0, 100), pageFromLedger(period, Filter{}, 0, 100, LedgerOf(period, Filter{})))
			}
			if next := readLogSnapshot(); next.blocks[0] == old.blocks[0] {
				t.Fatal("growing rewrite reused the old prefix")
			}
			vias := Vias(time.Time{})
			for session, input := range map[string]int{"edited-prefix": 73, "appended-tail": 19} {
				v := vias["codex|"+session]
				if len(v) != 1 || v[0].Calls != 1 || v[0].Tokens != input {
					t.Fatalf("%s vias stale: %+v", session, v)
				}
			}
		})
	}
}
