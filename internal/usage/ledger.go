package usage

// The ledger: every call of a period, one row each, newest first — what
// the agent asked for, where it went, what model answered, the tokens and
// what they cost at the effective price — to set beside a vendor's own bill.

import (
	"encoding/csv"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Row is one call as the ledger lists it.
type Row struct {
	Record
	// Cost is the call's price in USD, when its model's is known
	// (Priced); Swapped: the reply named another model than Model
	Cost    float64 `json:"cost"`
	Priced  bool    `json:"priced"`
	Swapped bool    `json:"swapped,omitempty"`
}

// Filter narrows the ledger: to one agent (its id, as AgentOf gives it),
// to the failed calls, and to the rows whose models, provider, host or
// session hold Query (any case).
type Filter struct {
	Agent  string
	Failed bool
	Query  string
}

func (f Filter) keeps(r Record) bool {
	if f.Agent != "" && AgentOf(r.Agent) != f.Agent {
		return false
	}
	if f.Failed && r.Status < 400 {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		return slices.ContainsFunc([]string{r.Requested, r.Model, r.Served, r.Provider, r.Host, r.Session, r.Effort}, func(s string) bool {
			return strings.Contains(strings.ToLower(s), q)
		})
	}
	return true
}

// Since is when the period began, as of now; zero for all.
func (p Period) Since(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch p {
	case Today:
		return day
	case Week:
		return day.AddDate(0, 0, -6)
	case Month:
		return day.AddDate(0, 0, -29)
	}
	return time.Time{}
}

// Ledger is the calls of a period that the filter keeps, newest first,
// with their sum, and the agents that made any call in the period (their
// ids, for a filter to offer).
func Ledger(p Period, f Filter) (rows []Row, sum Totals, agents []string) {
	return ledger(p.Since(time.Now()), f, Load(time.Time{}))
}

func ledger(since time.Time, f Filter, recs []Record) (rows []Row, sum Totals, agents []string) {
	renamed := provider.Renamed()
	priceOf := pricer()
	rows = []Row{}
	agents = []string{}
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if !since.IsZero() && r.Time.Before(since) {
			continue
		}
		if id, ok := renamed[r.Provider]; ok {
			r.Provider = id
		}
		if a := AgentOf(r.Agent); !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
		if !f.keeps(r) {
			continue
		}
		pr := priceOf(r)
		sum.add(r, pr)
		row := Row{Record: r, Swapped: r.Served != "" && Swapped(r.Model, r.Served)}
		if pr != nil && r.Input+r.Output > 0 {
			row.Cost, row.Priced = pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite), true
		}
		row.Agent = AgentOf(r.Agent)
		rows = append(rows, row)
	}
	slices.Sort(agents)
	return rows, sum, agents
}

// pricer looks up what each call cost the user, once per provider and model:
// the price they set for it, else its provider's models.dev entry's, else its
// maker's (a subscription's, #224).
func pricer() func(Record) *catalog.Price {
	prices := map[string]*catalog.Price{}
	return func(r Record) *catalog.Price {
		k := r.Provider + "/" + r.Model
		if pr, ok := prices[k]; ok {
			return pr
		}
		var pr *catalog.Price
		v, ok := provider.EffectivePrice(r.Provider, r.Model)
		if ok {
			pr = &v
		} else {
			catalog.Missing() // models.dev may list it by now
		}
		prices[k] = pr
		return pr
	}
}

// CSVHeader is the ledger's columns, as WriteCSV writes them.
var CSVHeader = []string{"time", "agent", "requested_model", "provider", "host", "model", "served_model", "swapped",
	"effort", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "reasoning_tokens",
	"cost_usd", "duration_ms", "ttft_ms", "status", "error", "session", "kind"}

// WriteCSV writes rows as CSV, a header first: times in RFC 3339 with
// their offset, the cost in USD at the effective price (empty when unknown), error
// "true" for a call answered with a status of 400 or more.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	cw.Write(CSVHeader)
	n := strconv.Itoa
	for _, r := range rows {
		cost := ""
		if r.Priced {
			cost = strconv.FormatFloat(r.Cost, 'f', 6, 64)
		}
		ttft := ""
		if r.TTFT > 0 {
			ttft = strconv.FormatInt(r.TTFT, 10)
		}
		cw.Write([]string{r.Time.Format(time.RFC3339), r.Agent, r.Requested, r.Provider, r.Host, r.Model, r.Served,
			strconv.FormatBool(r.Swapped), r.Effort, n(r.Input), n(r.Output), n(r.CacheWrite), n(r.CacheRead), n(r.Reasoning),
			cost, strconv.FormatInt(r.Millis, 10), ttft, n(r.Status), strconv.FormatBool(r.Status >= 400), r.Session, r.Kind})
	}
	cw.Flush()
	return cw.Error()
}
