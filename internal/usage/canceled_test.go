package usage

import (
	"bytes"
	"encoding/csv"
	"testing"
	"time"
)

// TestAgentCanceledIsNoFailure: a request its agent canceled before it was
// answered — a person stopped the reply, or sent the next message while it
// came and Claude Desktop asked again with it — is neither a failure nor an
// answer. The Requests card's failed count, its Failed filter and the CSV's
// error column leave it out, it is counted apart, and no first-token time
// or speed is read from it, as before. magpie's own limit running out on a
// request it asked of itself is logged 499 too, and stays a failure (#2318).
func TestAgentCanceledIsNoFailure(t *testing.T) {
	now := time.Now()
	canceled := Record{Time: now, Agent: "claude-desktop", Model: "m", Status: StatusCanceled, Error: AgentCanceled, Millis: 25000, TTFT: 1300, RouteID: 1}
	afterTry := Record{Time: now, Agent: "claude-desktop", Model: "m", Status: StatusCanceled, Error: AgentCanceled, Tries: 2, Millis: 9000, RouteID: 2}
	sideLimit := Record{Time: now, Agent: "claude-desktop", Model: "m", Status: StatusCanceled, Error: "magpie stopped waiting: its search had no answer in 2m0s", RouteID: 3}
	refused := Record{Time: now, Agent: "claude-desktop", Model: "m", Status: 400, Error: "Copilot: unsupported beta header(s): tool-search-tool-2025-10-19", RouteID: 4}
	answered := Record{Time: now, Agent: "claude-desktop", Model: "m", Status: 200, TTFT: 900, FirstText: 950, Millis: 5000, Output: 400, RouteID: 5}
	// a record logged with its error type keeps being the agent's cancel
	// whatever the message says; one logged before that is told by its message
	typed := Record{Time: now, Agent: "codex", Model: "m", Status: StatusCanceled, Error: "the client went away", ErrType: CanceledErrType, RouteID: 6}
	for _, c := range []struct {
		name                                      string
		r                                         Record
		failed, canceled, answered, lost, retried bool
	}{
		{"canceled", canceled, false, true, false, false, false},
		{"canceled after a failed try", afterTry, false, true, false, false, false},
		{"magpie's own limit", sideLimit, true, false, false, true, false},
		{"refused", refused, true, false, false, true, false},
		{"answered", answered, false, false, true, false, false},
		{"canceled, by its error type", typed, false, true, false, false, false},
	} {
		r := c.r
		if r.Failed() != c.failed || r.Canceled() != c.canceled || r.Answered() != c.answered || r.LostRequest() != c.lost || r.RetriedRequest() != c.retried {
			t.Errorf("%s: failed %v canceled %v answered %v lost %v retried %v, want %v %v %v %v %v", c.name,
				r.Failed(), r.Canceled(), r.Answered(), r.LostRequest(), r.RetriedRequest(),
				c.failed, c.canceled, c.answered, c.lost, c.retried)
		}
	}

	var tot Totals
	rows := []Row{}
	for _, r := range []Record{canceled, afterTry, sideLimit, refused, answered} {
		tot.add(r, nil)
		rows = append(rows, Row{Record: r})
	}
	if tot.Calls != 5 || tot.Errors != 2 || tot.Canceled != 2 || tot.Retried != 0 {
		t.Errorf("calls %d errors %d canceled %d retried %d, want 5, 2 (the refusal and magpie's own limit), 2, 0", tot.Calls, tot.Errors, tot.Canceled, tot.Retried)
	}
	if tot.Timed != 1 {
		t.Errorf("%d first-token times counted, want the answered one's only", tot.Timed)
	}
	var sum Totals
	for _, r := range rows {
		sum.addRow(r)
	}
	if sum.Errors != 2 || sum.Canceled != 2 || sum.Timed != 1 {
		t.Errorf("ledger rows: errors %d canceled %d timed %d, want 2, 2, 1", sum.Errors, sum.Canceled, sum.Timed)
	}

	var b bytes.Buffer
	if err := WriteCSV(&b, rows); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range got[0] {
		col[h] = i
	}
	for i, want := range []string{"false", "false", "true", "true", "false"} {
		row := got[i+1]
		if row[col["error"]] != want {
			t.Errorf("CSV row %d (status %s): error %q, want %q", i, row[col["status"]], row[col["error"]], want)
		}
	}
	if s := got[1][col["status"]]; s != "499" {
		t.Errorf("a canceled call's status in the CSV is %q, want 499 as before", s)
	}
}

// A Claude Code or Codex row read from its session file tells a speed
// floor from its output over its time (Row.Floor). One its agent canceled
// is timed nowhere, as Totals and the page leave its first token out: its
// output stopped where the agent left, not where the reply ended.
func TestAgentCanceledTellsNoFloor(t *testing.T) {
	canceled := Row{Source: "log", Record: Record{Agent: "claude", Model: "m", Status: StatusCanceled, Error: AgentCanceled, Output: 3000, Millis: 4000}}
	answered := canceled
	answered.Status, answered.Error = 200, ""
	if n, w := answered.Floor(); n == 0 || w == 0 {
		t.Fatalf("the answered row tells no floor (%d in %d ms); the case doesn't test the cancel", n, w)
	}
	if n, w := canceled.Floor(); n != 0 || w != 0 {
		t.Errorf("a canceled row tells a speed floor: %d tokens in %d ms", n, w)
	}
}
