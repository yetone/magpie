package usage

import (
	"encoding/csv"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// What a request said it cost in its plan's credits (#2512) is summed by
// model, agent and session, through the packed blocks a long history is
// kept in; a call that said nothing is not counted as credited.
func TestPlanCreditsSummed(t *testing.T) {
	resetUsageTest(t)
	at := Clock().Add(-time.Hour)
	credit := func(v float64) *float64 { return &v }
	// past a block's 64 rows, so the rows are read back from a packed one
	for i := 0; i < 70; i++ {
		Append(Record{Time: at.Add(time.Duration(i) * time.Second), Agent: "claude-code", Session: "s1", Provider: "workbuddy-ai", Model: "gemini-3.5-flash", Input: 9, Output: 12, Status: 200, Credits: credit(0.01)})
	}
	Append(Record{Time: at.Add(2 * time.Minute), Agent: "codex", Session: "s2", Provider: "workbuddy-ai", Model: "deepseek-v4.1-flash", Input: 9, Output: 1, Status: 200, Credits: credit(0)})
	Append(Record{Time: at.Add(3 * time.Minute), Agent: "codex", Session: "s2", Provider: "p", Model: "m", Input: 9, Output: 1, Status: 200})

	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	page := QueryPage(All, Filter{}, 0, 50)
	if s := page.Sum; !near(s.Credits, 0.7) || s.Credited != 71 {
		t.Fatalf("sum: %v credits of %d calls", s.Credits, s.Credited)
	}
	byModel := map[string]Share{}
	for _, s := range page.By["model"] {
		byModel[s.ID] = s
	}
	if s := byModel["gemini-3.5-flash"]; !near(s.Credits, 0.7) || s.Credited != 70 {
		t.Fatalf("gemini-3.5-flash: %v credits of %d calls", s.Credits, s.Credited)
	}
	if s := byModel["deepseek-v4.1-flash"]; s.Credits != 0 || s.Credited != 1 {
		t.Fatalf("a free model: %v credits of %d calls", s.Credits, s.Credited)
	}
	if s := byModel["m"]; s.Credited != 0 {
		t.Fatalf("a call that said none is credited: %+v", s)
	}
	for _, s := range page.By["agent"] {
		if want := map[string]int{"claude-code": 70, "codex": 1}[s.ID]; s.Credited != want {
			t.Fatalf("agent %s: %d credited, want %d", s.ID, s.Credited, want)
		}
	}
	vias := Vias(time.Time{})
	if v := vias.Of("claude-code|s1"); len(v) != 1 || !near(v[0].Credits, 0.7) || v[0].Credited != 70 {
		t.Fatalf("session s1: %+v", v)
	}
	if v := vias.Of("codex|s2"); len(v) != 2 || v[0].Credited+v[1].Credited != 1 {
		t.Fatalf("session s2: %+v", v)
	}
	if got := FormatCredits(page.Sum); got != "0.7 credits" {
		t.Fatalf("FormatCredits %q", got)
	}
	// the CSV's credits column: the reply's figure, empty where it said none
	rows, _, _ := Ledger(All, Filter{})
	var b strings.Builder
	if err := WriteCSV(&b, rows); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := slices.Index(CSVHeader, "credits")
	said := map[string]int{}
	for _, c := range cells[1:] {
		said[c[col]]++
	}
	if said["0.01"] != 70 || said["0"] != 1 || said[""] != 1 {
		t.Fatalf("credits column: %v", said)
	}
}

func TestFormatCredits(t *testing.T) {
	for _, c := range []struct {
		t    Totals
		want string
	}{
		{Totals{}, ""},
		{Totals{Credited: 3}, "0 credits"},
		{Totals{Credits: 0.004, Credited: 1}, "<0.01 credits"},
		{Totals{Credits: 5.71, Credited: 1}, "5.71 credits"},
		{Totals{Credits: 0.1 + 0.2, Credited: 2}, "0.3 credits"},
		{Totals{Credits: 1234.567, Credited: 9}, "1234.57 credits"},
	} {
		if got := FormatCredits(c.t); got != c.want {
			t.Errorf("FormatCredits(%v of %d) = %q, want %q", c.t.Credits, c.t.Credited, got, c.want)
		}
	}
}

// A packed block, and one the block pricer rewrote, give each row's
// credits back, and only to the rows that had them.
func TestPlanCreditsPacked(t *testing.T) {
	credit := 0.25
	var chunk rowChunk
	for i := 0; i < 80; i++ {
		r := Record{Time: time.Now(), Provider: "workbuddy-ai", Model: "m", Input: 1, Output: 1, Status: 200}
		if i%2 == 0 {
			r.Credits = &credit
		}
		chunk.add(Row{Record: r}, "", int64(i), false)
	}
	b := newBlockPricer(provider.Renames{}, nil, func(Record) *catalog.Price { return nil })
	defer b.release()
	packed := chunk.pack()
	if len(packed.Archive) == 0 {
		t.Fatal("the block wasn't packed")
	}
	for name, c := range map[string]*rowChunk{"raw": &chunk, "unpacked": packed.unpack(), "priced": b.block(packed)} {
		for i := 0; i < 80; i++ {
			got := c.row(i).Credits
			if i%2 == 0 && (got == nil || *got != credit) || i%2 == 1 && got != nil {
				t.Fatalf("%s row %d: credits %v", name, i, got)
			}
		}
	}
}

// Each account's credits are its own on the Usage page's Accounts list
// (lcyksp on Discord: WorkBuddy's 积分 spent, per account, not only
// tokens), through the summary and the Requests page's account groups.
func TestPlanCreditsByAccount(t *testing.T) {
	resetUsageTest(t)
	at := Clock().Add(-time.Hour)
	credit := func(v float64) *float64 { return &v }
	for i := 0; i < 70; i++ {
		Append(Record{Time: at.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "workbuddy", ProviderAccount: "a@x", Model: "glm-5.3", Input: 9, Output: 12, Status: 200, Credits: credit(0.5)})
	}
	Append(Record{Time: at.Add(2 * time.Minute), Agent: "codex", Provider: "workbuddy", ProviderAccount: "b@x", Model: "glm-5.3", Input: 9, Output: 1, Status: 200, Credits: credit(2.25)})
	Append(Record{Time: at.Add(3 * time.Minute), Agent: "codex", Provider: "workbuddy", ProviderAccount: "b@x", Model: "glm-5.3", Input: 9, Output: 1, Status: 200})

	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	want := map[string]Totals{"a@x": {Credits: 35, Credited: 70}, "b@x": {Credits: 2.25, Credited: 1}}
	check := func(what string, groups []Group) {
		t.Helper()
		if len(groups) != len(want) {
			t.Fatalf("%s: %d accounts, want %d: %+v", what, len(groups), len(want), groups)
		}
		for _, g := range groups {
			w := want[g.Account]
			if !near(g.Credits, w.Credits) || g.Credited != w.Credited {
				t.Fatalf("%s %s: %v credits of %d calls, want %v of %d", what, g.Account, g.Credits, g.Credited, w.Credits, w.Credited)
			}
		}
	}
	check("summary", Summarize(All).Accounts)
	check("requests", QueryPage(All, Filter{}, 0, 50).Accounts)
}
