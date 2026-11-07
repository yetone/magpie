package gateway

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #5646 (louisgreen0726): the Routing page told a Smart order from the
// trace's renews, each window's own reset, while Smart sorted by
// Allowance.Renewal, which takes a credit pack's end for the window's
// renewal when it comes first, and broke a tie by Allowance.Lapse. A
// WorkBuddy account whose credits ran out on 11-11 ranked ahead of one
// renewing on 11-30 and was told as renewing in the same hour, kept in
// order. The trace carries the keys the sort used, soon and lapse, so the
// page compares what the gateway compared.
func TestTraceCarriesSmartSortKeys(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: "a"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: user}}, model: "hy3", rest: "workbuddy#" + user}
	}
	month := now.Truncate(time.Hour).Add(22*24*time.Hour + 59*time.Minute)
	credits := func(used float64, lapses time.Time) provider.Allowance {
		return provider.Allowance{{Used: used, Amount: used * 34, Of: 3400, Unit: "credits", Resets: month, Span: 31*24*time.Hour - time.Second, RunsOut: lapses}}
	}
	// the trace's rows in the order Smart put them
	rows := func(cs ...candidate) ([]string, []Weighed) {
		got, wg := weigh(p, cs, "hy3", provider.Chat)
		var ids []string
		var ws []Weighed
		for _, c := range got {
			ids = append(ids, c.p.Account.User)
			ws = append(ws, weighed(c, c.p, wg, false, provider.Chat))
		}
		return ids, ws
	}
	// byTrace orders the rows by the trace's soon then lapse, to the hour,
	// as routes.Smart does, the order given kept where they tie
	byTrace := func(ws []Weighed) string {
		s := slices.Clone(ws)
		hour := func(t time.Time) int64 { return t.Truncate(time.Hour).Unix() }
		slices.SortStableFunc(s, func(a, b Weighed) int {
			for k := 0; k < len(a.Soon) || k < len(b.Soon); k++ {
				var x, y time.Time
				if k < len(a.Soon) {
					x = a.Soon[k]
				}
				if k < len(b.Soon) {
					y = b.Soon[k]
				}
				if hour(x) != hour(y) {
					return int(hour(x) - hour(y))
				}
			}
			var x, y time.Time
			if a.Lapse != nil {
				x = *a.Lapse
			}
			if b.Lapse != nil {
				y = *b.Lapse
			}
			switch {
			case hour(x) == hour(y):
				return 0
			case x.IsZero():
				return 1
			case y.IsZero():
				return -1
			}
			return int(hour(x) - hour(y))
		})
		var out []string
		for _, w := range s {
			out = append(out, strings.TrimPrefix(w.ID, "workbuddy#"))
		}
		return strings.Join(out, "")
	}

	// the reporter's: a's credits end eight days before the month b
	// renews with, which a renews with too
	pack := now.Truncate(time.Hour).Add(3*24*time.Hour + 32*time.Second)
	share["a"] = credits(10, pack)
	share["b"] = credits(30, time.Time{})
	ids, ws := rows(acct("b"), acct("a"))
	if strings.Join(ids, "") != "ab" {
		t.Fatalf("credits ending first go first: %v", ids)
	}
	a := ws[0]
	if len(a.Soon) != 1 || !a.Soon[0].Equal(pack) {
		t.Fatalf("a's soon is its credits' end %v: %+v", pack, a.Soon)
	}
	if len(a.Renews) != 1 || !a.Renews[0].Equal(month) {
		t.Fatalf("a's renews stays its window's reset: %+v", a.Renews)
	}
	if a.Lapse == nil || !a.Lapse.Equal(pack) || ws[1].Lapse != nil {
		t.Fatalf("lapse: a %v, b %v", a.Lapse, ws[1].Lapse)
	}
	if got := byTrace([]Weighed{ws[1], ws[0]}); got != "ab" {
		t.Fatalf("the trace's keys order them %s, Smart ab", got)
	}

	// renewing together, a's given credits lapsing after the month's end
	// and before b's: the lapse breaks the tie
	share["a"] = credits(10, month.Add(5*24*time.Hour))
	share["b"] = credits(30, month.Add(50*24*time.Hour))
	ids, ws = rows(acct("b"), acct("a"))
	if strings.Join(ids, "") != "ab" {
		t.Fatalf("lapsing first goes first: %v", ids)
	}
	if !ws[0].Soon[0].Equal(month) || !ws[1].Soon[0].Equal(month) {
		t.Fatalf("both renew with the month: %v %v", ws[0].Soon, ws[1].Soon)
	}
	if got := byTrace([]Weighed{ws[1], ws[0]}); got != "ab" {
		t.Fatalf("the trace's keys order them %s, Smart ab", got)
	}

	// alike to the hour: their order stands, and the trace's keys tie
	share["a"] = credits(10, month.Add(5*24*time.Hour))
	share["b"] = credits(30, month.Add(5*24*time.Hour-time.Minute))
	ids, ws = rows(acct("b"), acct("a"))
	if strings.Join(ids, "") != "ba" || byTrace(ws) != "ba" {
		t.Fatalf("tied keep their order: %v, by the trace %s", ids, byTrace(ws))
	}
}
