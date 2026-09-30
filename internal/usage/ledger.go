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
	"github.com/yetone/magpie/internal/settings"
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
//
// A row's swapped is judged by the upstream names in force now, not by the
// one its call went out under: the names are read from the settings here,
// once, so a name given or changed since a call was recorded re-judges that
// call — one recorded under the name magpie knows the model by, and read
// after a name was set for it, reads as a swap although the vendor answered
// with the very model that was asked for. A record keeps the canonical id
// and the name the vendor's own reply gave (served_model) and not the name
// the request was sent under, so this is the only name there is to judge by
// — read as the name that went out, which the effort a record keeps of
// makes exact: an Antigravity account is asked for the variant of the model
// that level picks, so a reply naming that one is the model that was asked
// for and not another, whatever the family it is a level of.
// It is how the loop below already treats the rest of a row: a provider
// renamed since is put back to the id it has now, and the names in force now
// are what the catalog lists the models under as well.
//
// Storing the name that was sent would judge each call by what it was
// really sent under, and it is the better answer, but it is not this
// function's to make: it is a field on every record the gateway writes, and
// so a change to the file's format — a column a row written by an older
// magpie has no value for, and a decision about whether an empty one means
// "no upstream name" or "unknown" — rather than a row of a report. The rows
// here are what a reader sets beside a bill, and a name in force today is
// the one whose reply a bill's model column is read against.
//
// The names are read here, once for the lot, rather than asked for a row
// at a time, so a caller already holding them — a report over the
// provider/model it was given — reads them the same way and reaches the
// same judgement: what a row says is the same however the rows were asked
// for.
func Ledger(p Period, f Filter) (rows []Row, sum Totals, agents []string) {
	return ledger(p.Since(time.Now()), f, Load(time.Time{}))
}

func ledger(since time.Time, f Filter, recs []Record) (rows []Row, sum Totals, agents []string) {
	renamed := provider.Renamed()
	// the upstream names in force now, read once for the lot: a row is
	// judged by the names standing today, which is what Ledger says, and
	// the rows here are what a reader compares a bill against
	wires := settings.Load().ModelWires
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
		row := Row{Record: r, Swapped: r.Served != "" && Swapped(provider.SentNameIn(wires, r.Provider, r.Model, r.Effort), r.Served)}
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
