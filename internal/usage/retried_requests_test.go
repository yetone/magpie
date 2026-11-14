package usage

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Requests card counts requests, not tries (小一 on Discord): a try
// that failed and handed its request on to another account is neither a
// request nor a failure of its own, and a request answered after such a
// try is counted as answered on a retry. Failed is what the agent got an
// error for. A log written before the gateway marked those tries is told
// by its route: a failed row whose route a later row ends.
func TestRequestsCountedOnceWithTheirRetries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)) // midnight never falls between a call and Today (#6486)
	at := func(m int) string { return now.Add(time.Duration(m-10) * time.Second).Format(time.RFC3339Nano) }
	lines := []string{
		// written before handed_on: a safety refusal that a group's next
		// member answered, the two sharing the route
		`{"t":"` + at(1) + `","agent":"claude","provider":"yylx","model":"m","in":10,"ms":5,"status":400,"error":"content blocked by the safety filter","route_id":101}`,
		`{"t":"` + at(2) + `","agent":"claude","provider":"b","model":"m","in":10,"out":5,"ms":9,"status":200,"route_id":101}`,
		// today's: a refused try handed on, a 429 that wrote no row, then
		// the answer on the third try
		`{"t":"` + at(3) + `","agent":"codex","provider":"a","model":"m","in":20,"ms":5,"status":400,"error":"refused","route_id":102,"handed_on":true}`,
		`{"t":"` + at(4) + `","agent":"codex","provider":"c","model":"m","in":20,"out":7,"ms":9,"status":200,"route_id":102,"tries":3}`,
		// a request that failed outright, after two tries
		`{"t":"` + at(5) + `","agent":"codex","provider":"a","model":"m","ms":5,"status":502,"error":"bad gateway","route_id":103,"tries":2}`,
		// one answered at once, and one that failed with no route at all
		`{"t":"` + at(6) + `","agent":"codex","provider":"a","model":"m","in":1,"out":1,"ms":5,"status":200,"route_id":104}`,
		`{"t":"` + at(7) + `","agent":"codex","provider":"a","model":"m","ms":5,"status":429}`,
	}
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	if err := os.WriteFile(Path(), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	rows, sum, _ := Ledger(Today, Filter{})
	// five requests: 101 and 102 answered on a retry, 103 and the routeless
	// 429 failed, 104 answered; all seven rows are still listed, and the
	// handed-on tries' tokens are still in the sums
	if len(rows) != 7 || sum.Calls != 5 || sum.Errors != 2 || sum.Retried != 2 || sum.Input != 61 {
		t.Fatalf("rows %d, calls %d, errors %d, retried %d, in %d", len(rows), sum.Calls, sum.Errors, sum.Retried, sum.Input)
	}
	handed := 0
	for _, r := range rows {
		if r.HandedOn {
			handed++
			if r.RouteID != 101 && r.RouteID != 102 {
				t.Fatalf("handed on: %+v", r)
			}
		}
		if r.RouteID == 101 && r.Status == 200 && r.Tries != 2 {
			t.Fatalf("legacy answer's tries %d", r.Tries)
		}
	}
	if handed != 2 {
		t.Fatalf("%d rows handed on, want 2", handed)
	}

	// Failed keeps every request a try failed on, as Routing shows them
	// (maohy915 on X): the two that failed, the two tries handed on and
	// the two answers after them; still counted by how each ended
	failed, fsum, _ := Ledger(Today, Filter{Failed: true})
	if len(failed) != 6 || fsum.Errors != 2 || fsum.Retried != 2 || fsum.Calls != 4 {
		t.Fatalf("failed filter: %d rows, %+v", len(failed), fsum)
	}
	for _, r := range failed {
		if r.RouteID == 104 || !r.Failed() && r.Tries < 2 {
			t.Fatalf("failed filter kept %+v", r)
		}
	}
	retried, rsum, _ := Ledger(Today, Filter{Retried: true})
	if len(retried) != 2 || rsum.Retried != 2 || rsum.Errors != 0 {
		t.Fatalf("retried filter: %d rows, %+v", len(retried), rsum)
	}
	for _, r := range retried {
		if r.Status != 200 || r.Tries < 2 {
			t.Fatalf("retried filter kept %+v", r)
		}
	}

	page := QueryPage(Today, Filter{}, 0, 100)
	if page.Sum.Calls != 5 || page.Sum.Errors != 2 || page.Sum.Retried != 2 {
		t.Fatalf("page sum %+v", page.Sum)
	}
	if p := QueryPage(Today, Filter{Failed: true}, 0, 100); p.Total != 6 || p.Sum.Errors != 2 || p.Sum.Retried != 2 {
		t.Fatalf("failed page has %d rows", p.Total)
	}
	if p := QueryPage(Today, Filter{Retried: true}, 0, 100); p.Total != 2 {
		t.Fatalf("retried page has %d rows", p.Total)
	}
	if s := Summarize(Today); s.Calls != 5 || s.Errors != 2 || s.Retried != 2 {
		t.Fatalf("summary %d calls, %d errors, %d retried", s.Calls, s.Errors, s.Retried)
	}
	calls := 0
	for _, d := range HeatmapOf(Filter{}).Days {
		calls += d.Calls
	}
	if calls != 5 {
		t.Fatalf("heatmap counts %d calls", calls)
	}

	var out bytes.Buffer
	if err := WriteCSV(&out, rows); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	head := recs[0]
	ti, hi := -1, -1
	for i, h := range head {
		switch h {
		case "tries":
			ti = i
		case "handed_on":
			hi = i
		}
	}
	if ti < 0 || hi < 0 {
		t.Fatalf("CSV header %v", head)
	}
	seen := map[string]int{}
	for _, r := range recs[1:] {
		seen[r[ti]+"/"+r[hi]]++
	}
	if seen["/true"] != 2 || seen["3/false"] != 1 || seen["2/false"] != 2 {
		t.Fatalf("CSV tries/handed_on %v", seen)
	}
}
