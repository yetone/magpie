package usage

// The ledger: every call of a period, one row each, newest first — what
// the agent asked for, where it went, what model answered, the tokens and
// what they cost at the effective price — to set beside a vendor's own bill.

import (
	"encoding/csv"
	"github.com/yetone/magpie/internal/dayzone"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// Row is one call as the ledger lists it.
type Row struct {
	Record
	// SessionRole is the query-time Codex actor classification. Unknown is
	// explicit so a shared session header cannot make an unpaired call look
	// like its root thread.
	SessionRole string `json:"session_role,omitempty"`
	codexActor  string
	// Cost is the call's effective price in USD, when its model's is known
	// (Priced); Swapped: the reply named another model than Model
	Cost    float64 `json:"cost"`
	Priced  bool    `json:"priced"`
	Swapped bool    `json:"swapped,omitempty"`
	// Routed marks a remote magpie group answered by one of its members.
	Routed bool `json:"routed,omitempty"`
	// Source is "log" for a call read from an agent's own session file,
	// which the gateway never saw: no status, no provider, no model asked
	// for, but what the file says of the call. "" for one the gateway logged.
	Source string `json:"source,omitempty"`
	// Client is the client's own name when the user counts it as another
	// agent (settings.ClientAgents, #1958): Agent is the one it is
	// counted as, this what the ledger recorded it as
	Client string `json:"client,omitempty"`
}

// Failed is whether the call failed: the agent was answered an error status,
// or, for a call read from a session file, which has no status, the file
// says an error ended it. A call the agent canceled itself (Canceled) didn't
// fail: the agent left before an answer, and was told no error.
func (r Record) Failed() bool { return (r.Status >= 400 || r.Error != "") && !r.Canceled() }

// AgentCanceled is what the usage log says of a request the agent canceled
// before it was answered (the gateway's canceled), with the status
// StatusCanceled.
const AgentCanceled = "the agent canceled the request"

// StatusCanceled is the status a request the agent canceled is logged
// with: nginx's 499, the client closed the request.
const StatusCanceled = 499

// CanceledBy is whether a call that ended with status and error was
// canceled by its agent: a person stopped the reply, or sent the next
// message while it was coming and the agent asked again. Not magpie's own
// limit running out on a request it asked of itself ("magpie stopped
// waiting", #2318), which is logged 499 too and stays a failure.
func CanceledBy(status int, err string) bool { return status == StatusCanceled && err == AgentCanceled }

// CanceledErrType is the error type a call its agent canceled is logged
// with (CanceledBy), as LoopErrType is a looping reply's: the record keeps
// that it was the agent's cancel, whatever its message says.
const CanceledErrType = "agent_canceled"

// Canceled is whether the agent canceled the call before it was answered:
// neither failed nor answered, counted apart (Totals.Canceled). A record
// logged before CanceledErrType was is told by its status and message.
func (r Record) Canceled() bool {
	return r.Status == StatusCanceled && (r.ErrType == CanceledErrType || CanceledBy(r.Status, r.Error))
}

// Answered is whether the call ended with an answer: it neither failed nor
// was canceled, so how long it took to its first token and its speed tell
// something.
func (r Record) Answered() bool { return !r.Failed() && !r.Canceled() }

// Filter narrows the ledger: to a route, one agent (its id, as AgentOf gives it), to
// one provider, to the failed calls, and to the rows whose models, provider, host or
// session hold Query (any case).
type Filter struct {
	Day string // YYYY-MM-DD in Zone; "" is the whole period
	// Zone is the reader's time zone (Zone gives it), which the period's
	// days, the chart's hours and Day are read in; nil is this computer's
	Zone      *time.Location
	CallerKey string
	RouteID   int64
	Model     string // exact model selected in the ranking
	Agent     string
	Provider  string // a provider's id, as the ledger's rows have it
	Purpose   string // PurposeOf's key; empty keeps every purpose
	// Account narrows to the calls a subscription account answered, by its
	// name as Record.Account gives it (#557)
	Account string
	// Failed keeps the rows of the requests the agent got an error for
	// (Record.LostRequest), Retried those of the requests answered after a
	// try that failed first (Record.RetriedRequest): what the Requests
	// card's two counts are of
	Failed  bool
	Retried bool
	Query   string
	// Computer narrows to the calls of one computer (#542): ThisComputer,
	// OtherComputers, or another's id; "" is every computer's
	Computer string
	// Through narrows to one of the sources a row can be of
	// (SourceGateway, SourceSession, SourceReported); "" keeps all. It is what the requests
	// page's source cells filter by, and, as with the other dimensions, the
	// cells themselves are counted with it cleared so all three stay
	// switchable. Not Record.Via, which is the computer that forwarded the
	// call (#542).
	Through string
}

// SourceGateway and SourceSession are the two sources a request row is of,
// which Row.Source spells "" and "log": the calls magpie's own gateway
// served, and the ones read from the agents' own session files, which it
// never saw. They are the requests page's source cells, and the values its
// via= takes.
const (
	SourceGateway = "through"
	SourceSession = "direct"
	// SourceReported is the third: the calls a client reported through
	// POST /v1/magpie/usage/requests (#1874), which went straight to their
	// vendor. Record.Source and Row.Source spell it the same, as does via=.
	SourceReported = "reported"
)

// SourceOf is the source a row is of, as the page's cells name them: the
// calls magpie's gateway served and the ones read from the agents' session
// files. A call the gateway turned away itself is neither — the page says
// under its totals that local rejections are not counted — so it is "" and
// picking either cell leaves it out, as the cells' own counts already do.
func SourceOf(r Row) string {
	if r.IsRejected() || r.IsProbe() { // magpie's own test is neither too
		return ""
	}
	switch r.Source {
	case "log":
		return SourceSession
	case SourceReported:
		return SourceReported
	}
	return SourceGateway
}

// keepsRow is keeps with the one dimension a Record does not carry: which of
// the two sources the row is of (Row.Source, not the record's).
func (f Filter) keepsRow(r *Row) bool {
	if clearedNow().hides(&r.Record) { // the user's clears (cleared.go)
		return false
	}
	if f.Through != "" && SourceOf(*r) != f.Through {
		return false
	}
	return f.keepsRecord(&r.Record, r.SessionRole != "", r.Subagent, r.ParentAgent)
}

// OtherComputers is the Filter.Computer of every other computer's calls.
const OtherComputers = "others"

// computer is whether r is of the computers Computer picks.
func (f Filter) computer(r *Record) bool {
	switch f.Computer {
	case "":
		return true
	case ThisComputer:
		return r.Computer == ""
	case OtherComputers:
		return r.Computer != ""
	}
	return r.Computer == f.Computer
}

func (f Filter) keeps(r Record) bool {
	return f.keepsRecord(&r, false, "", "")
}

// sameGroupModel is whether a record's model is the found routing group
// a filter names by its kept id (group/auto-…, FilterOf), written by an
// agent's own session file as auto/<model>. Only such pairs read the
// providers, not every record a filter passes over.
func sameGroupModel(recorded, filtered string) bool {
	return strings.HasPrefix(filtered, provider.GroupPrefix) && strings.HasPrefix(recorded, provider.AutoPrefix) &&
		provider.CanonicalRef(recorded) == filtered
}

func (f Filter) keepsRecord(r *Record, codexIdentity bool, subagent, parentAgent string) bool {
	if f.Day != "" {
		var day [len(time.DateOnly)]byte
		if string(r.Time.In(f.loc()).AppendFormat(day[:0], time.DateOnly)) != f.Day {
			return false
		}
	}
	if f.CallerKey != "" && r.CallerKeyID != f.CallerKey && (f.CallerKey != NoCallerKey || r.CallerKeyID != "") {
		return false
	}
	if f.RouteID != 0 && r.RouteID != f.RouteID {
		return false
	}
	if f.Model != "" && r.Model != f.Model && !sameGroupModel(r.Model, f.Model) {
		return false
	}
	if f.Agent != "" && ReportedAs(r.Agent) != f.Agent {
		return false
	}
	if f.Provider != "" && r.Provider != f.Provider {
		return false
	}
	if f.Purpose != "" && PurposeOf(r.Kind) != f.Purpose {
		return false
	}
	if f.Account != "" && r.Account() != f.Account {
		return false
	}
	if !f.computer(r) {
		return false
	}
	if f.Failed && !r.LostRequest() {
		return false
	}
	if f.Retried && !r.RetriedRequest() {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		if codexIdentity && (strings.Contains(strings.ToLower(subagent), q) || strings.Contains(strings.ToLower(parentAgent), q)) {
			return true
		}
		return slices.ContainsFunc([]string{r.Requested, r.Model, r.Served, r.Upstream, r.Provider, r.Host, r.Session, r.Effort, r.SessionProvider, r.SessionAccount, r.ProviderKeyID, r.ProviderKeyName, r.Account(), r.CallerKeyID, r.CallerKeyName}, func(s string) bool {
			return strings.Contains(strings.ToLower(s), q)
		})
	}
	return true
}

// Since is when the period began, as of now; zero for all.
func (p Period) Since(now time.Time) time.Time {
	if since, _, ok := p.bounds(now.Location()); ok {
		return since
	}
	// a rolling window ends now, so it began that long ago (#2075)
	if d := p.Last(); d > 0 {
		return now.Add(-d)
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch p {
	case Today:
		return day
	case Week:
		return day.AddDate(0, 0, -6)
	case Month:
		return day.AddDate(0, 0, -29)
	case heatmapPeriod:
		return heatmapSince(now)
	}
	return time.Time{}
}

// LogCalls reads the calls the agents' own session files record, which the
// ledger adds to those the gateway logged. A variable for the tests.
var LogCalls func(time.Time) []sessions.Call

// Clock is the time the periods are read by: when today began, the hours
// or days of the chart, the day a cached answer is for. A variable for the
// tests, which hold it still so that midnight never falls between a call
// they log and the period they ask for.
var Clock = time.Now

// Ledgered is a period's calls that a filter keeps, newest first, with their
// sum, and the agents and providers that made any call in the period (their
// ids, for a filter to offer).
type Ledgered struct {
	Rows      []Row
	Sum       Totals
	Agents    []string
	Providers []string
}

// LedgerOf is the calls of a period that the filter keeps. The gateway's log
// comes with the calls the agents' session files record that the gateway did
// not see.
func LedgerOf(p Period, f Filter) Ledgered {
	return LedgerOfAt(p, f, Clock().In(f.loc()))
}

// LedgerOfAt is LedgerOf with the period read at now, for an answer that
// names that moment elsewhere too, as an exported CSV's name does.
func LedgerOfAt(p Period, f Filter, now time.Time) Ledgered {
	reader := LogCalls
	if reader == nil {
		reader = sessions.Calls
	}
	return ledgerOfAt(p, f, now, reader)
}

// ledgerOfAt is LedgerOfAt over the session files' calls reader reads.
func ledgerOfAt(p Period, f Filter, now time.Time, reader func(time.Time) []sessions.Call) Ledgered {
	readCleared() // the user's clears, which keepsRow leaves out
	since := p.Since(now)
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	} // calls ending across the period boundary
	rows, sum, agents, providers := ledgerWithShared(since, p.Until(now), f, gatewayRecords(gatewaySince), reader(since), sharedRecords(since))
	return Ledgered{rows, sum, agents, providers}
}

// gatewayRecords is the gateway's calls from since on, as the ledger's index
// holds them: what Load reads, without parsing usage.jsonl again. The TUI,
// magpie usage and the CSV export read a period's ledger this way, and over
// a long history (Vantiboo1ean, ~750k requests) parsing the whole file took
// seconds for each.
func gatewayRecords(since time.Time) []Record {
	var out []Record
	Visit(since, func(r Record) { out = append(out, r) })
	return out
}

// Filtered narrows one snapshot without reloading either log or repricing rows.
func (l Ledgered) Filtered(f Filter) Ledgered {
	g := f
	g.Zone = nil // the zone narrows nothing but through Day
	if g == (Filter{}) {
		return l
	}
	out := Ledgered{Rows: []Row{}, Agents: l.Agents, Providers: l.Providers}
	for _, r := range l.Rows {
		if f.keepsRow(&r) {
			out.Rows = append(out.Rows, r)
			if !r.IsRejected() {
				out.Sum.addRow(r)
			}
		}
	}
	return out
}

// Ledger is LedgerOf's rows, sum and agents.
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
// It is how ledgerWith's loop already treats the rest of a row: a provider
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
	l := LedgerOf(p, f)
	return l.Rows, l.Sum, l.Agents
}

func ledger(since time.Time, f Filter, recs []Record) (rows []Row, sum Totals, agents []string) {
	rows, sum, agents, _ = ledgerWith(since, f, recs, nil)
	return rows, sum, agents
}

// UnknownProvider groups local session calls without claiming a supplier.
// Recorded provider IDs and matched creator identities stay in their own fields.
const UnknownProvider = "session-unknown"

// logRecord is a call read from a session file, as a record: the upstream is
// unknown, and what the file doesn't say is left out. Nothing
// stood between the agent and the vendor, so what was sent is what the agent
// asked for. Claude Code's file names the model it asked for (as it runs) and
// the one the API answered with, which is the served model; Codex's and
// OpenCode's name only the one they asked for (OpenCode's reply keeps the
// model it was sent to, never the one the response named) (#680).
func logRecord(c sessions.Call) Record {
	r := Record{Time: c.Time, Agent: c.Agent, Provider: UnknownProvider, Model: c.Model, Served: c.Model, Requested: c.Requested,
		Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite, CacheWrite1h: c.CacheWrite1h, Reasoning: c.Reasoning, Effort: c.Effort,
		Millis: c.Millis, TTFT: c.TTFT, Session: c.Session, RequestID: c.RequestID, MessageID: c.Msg, Error: c.ErrorText, ErrType: c.Error, Subagent: sessions.SubagentOf(c.File), ParentAgent: sessions.SubagentParent(c.File)}
	if c.Agent == "codex" {
		r.ResponseID = c.ResponseID
		r.Compaction = codexCompactionOf(c)
	}
	if c.Agent == "codex" || c.Agent == "opencode" {
		r.Requested, r.Served = c.Model, ""
	}
	if r.Requested != "" {
		// sent as asked, less the size of its context Claude Code names it by
		r.Model = contextTail.ReplaceAllString(r.Requested, "")
	}
	return r
}

// gatewayMatches correlates IDs first, then native session, equal token counts
// and an end within 2s. Fallback matches must be unique in both logs: an
// ambiguous direct call stays visible. One gateway entry consumes one file call.
func gatewayMatches(recs []Record, logs []sessions.Call) map[int]bool {
	matched, _, _, _ := gatewayMatchesDetails(recs, logs)
	return matched
}

func gatewayMatchesDetails(recs []Record, logs []sessions.Call) (map[int]bool, map[int]int, map[int]string, map[int]*bool) {
	gateway, local := &rowChunk{}, &rowChunk{}
	for i, r := range recs {
		r.Agent = ReportedAs(r.Agent)
		gateway.add(Row{Record: r}, "", int64(i), false)
	}
	for i, c := range logs {
		r := logRecord(c)
		r.Agent = c.Agent // the native log already names its agent
		local.add(Row{Record: r, codexActor: codexActor(c)}, c.Msg, int64(i), c.Error != "")
	}
	duplicates, actors, compactions := duplicateCodexCalls(logs)
	pairs := codexResponseMatches(recs, logs, duplicates)
	deduplicate := refSet{}
	for index := range duplicates {
		deduplicate.add(rowRef{local, index})
	}
	responsePairs := make(map[rowRef]rowRef, len(pairs))
	for record, call := range pairs {
		responsePairs[rowRef{gateway, record}] = rowRef{local, call}
	}
	matched := map[int]bool{}
	for ref := range matchedBlocks([]*rowChunk{gateway}, []*rowChunk{local}, deduplicate, responsePairs, time.Time{}, false).all() {
		matched[ref.Index] = true
	}
	for index := range duplicates {
		matched[index] = true
	}
	return matched, pairs, actors, compactions
}

func ledgerWith(since time.Time, f Filter, recs []Record, logs []sessions.Call) (rows []Row, sum Totals, agents, providers []string) {
	return ledgerWithShared(since, time.Time{}, f, recs, logs, nil)
}

// ledgerWithShared is ledgerWith with the calls other computers made (#542),
// priced and judged here as this computer's are, and none from until on (a
// picked range's end, #1492; zero for none).
func ledgerWithShared(since, until time.Time, f Filter, recs []Record, logs []sessions.Call, others []SharedCall) (rows []Row, sum Totals, agents, providers []string) {
	logs = ownCalls(logs)
	matched, pairs, actors, compactions := gatewayMatchesDetails(recs, logs)
	return ledgerMatched(since, until, f, recs, logs, others, matched, pairs, actors, compactions)
}

// ledgerMatched is ledgerWithShared with the session files' calls the
// gateway saw already told (matched, by their index in logs).
func ledgerMatched(since, until time.Time, f Filter, recs []Record, logs []sessions.Call, others []SharedCall, matched map[int]bool, pairs map[int]int, actors map[int]string, compactions map[int]*bool) (rows []Row, sum Totals, agents, providers []string) {
	renamed := provider.UsageRenames()
	// the upstream names in force now, read once for the lot: a row is
	// judged by the names standing today, which is what Ledger says, and
	// the rows here are what a reader compares a bill against
	wires := settings.Load().Wires()
	priceOf := Pricer()
	codexIdentities := sessions.CodexIdentities()
	rows = make([]Row, 0, len(recs)+len(logs))
	agents, providers = []string{}, []string{}
	identities := newSessionResolver(logs)
	add := func(r Record, pr *catalog.Price, source, actor string) {
		if a := ReportedAs(r.Agent); !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
		if r.Provider != "" && !slices.Contains(providers, r.Provider) {
			providers = append(providers, r.Provider)
		}
		sent := r.Model
		if source == "" {
			sent = provider.SentNameIn(wires, r.Provider, r.Model, r.Effort)
		}
		row := codexIdentityRow(Row{Record: r, Swapped: r.Served != "" && Swapped(sent, r.Served) && !ListedAs(r.Provider, r.Model, r.Served), Routed: GroupRouted(sent, r.Served), Source: source}, actor, codexIdentities)
		if !f.keepsRow(&row) {
			return
		}
		if pr != nil && r.Input+r.Output > 0 {
			row.Cost, row.Priced = r.CostAt(*pr), true
		}
		row.Agent, row.Client = reportedAs(r.Agent)
		if !r.IsRejected() {
			sum.addFrom(row, pr)
		}
		rows = append(rows, row)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if !since.IsZero() && r.Time.Before(since) || after(until, r.Time) {
			continue
		}
		r.renameBy(renamed)
		actor := ""
		if call, ok := pairs[i]; ok && call >= 0 && call < len(logs) {
			actor = logs[call].ThreadID
			if override, ok := actors[call]; ok {
				actor = override
			}
			r.Compaction = compactions[call]
		}
		add(r, priceOf(r), r.Source, actor)
	}
	for i, c := range logs {
		if (!since.IsZero() && c.Time.Before(since)) || after(until, c.Time) || matched[i] {
			continue
		}
		// Session identity does not establish a billing provider; price the
		// record through the same lookup without inferring one from the account.
		r := logRecord(c)
		if metadata, ok := compactions[i]; ok {
			r.Compaction = metadata
		}
		actor := codexActor(c)
		if override, ok := actors[i]; ok {
			actor = override
		}
		r.SessionAccount = identities.resolve(c)
		r.SessionOfficialLogin = identities.officialLogin(c, r.SessionAccount)
		r.SessionProvider = c.Upstream
		add(r, priceOf(r), "log", actor)
	}
	for _, c := range others {
		r := c.Record
		if !since.IsZero() && r.Time.Before(since) || after(until, r.Time) {
			continue
		}
		r.renameBy(renamed)
		add(r, priceOf(r), c.Source, "")
	}
	// the two logs, by when each call began
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.After(rows[j].Time) })
	slices.Sort(agents)
	slices.Sort(providers)
	return rows, sum, agents, providers
}

// ownPrice is what Pricer answers for a reported call that said what it
// cost: a price, so the call counts as priced, which Record.CostAt passes
// over for the call's own cost.
var ownPrice catalog.Price

// Pricer uses the same effective price as the model CLI and session totals:
// explicit provider/model or provider-wide prices (including zero), then the
// provider catalog, then the maker catalog. Settings are read once per query.
//
// A model no price is known for is priced as the model the vendor's reply
// said answered (Served), at the same provider, when that one has a price:
// a relay that sells kimi-k3 as moonshot-kimi-k3 answers as kimi-k3, which
// is listed (#1498). So is a call the vendor genuinely answered with
// another model than the one asked for — the swap the ledger marks, no
// rename in either name (#5340): the vendor billed for the one that ran,
// and the asked-for model's rate describes nothing that happened. A price
// the user set for the model asked for keeps its say either way, a zero
// included, so a ledger that matched the vendor's bill still does, and a
// served model with no price leaves the asked-for one's standing. A record
// inside one of its provider's rate windows (#2463) is priced at that
// window's rate, its own time read against the windows, so calls either
// side of a boundary price apart.
//
// A local session's call has no provider of ours to price at (its billing
// provider is the unknown one) but its file names the route it went
// through; that name prices the call when the catalog lists the very model
// id under it, and never otherwise: a name models.dev doesn't know, or
// one whose other models alone would stand for this call's, prices
// nothing (#4278).
func Pricer() func(Record) *catalog.Price {
	prices := map[[5]string]*catalog.Price{}
	// each provider's rate windows as the same read of the prices saw
	// them: the windows are held once (BasePriceIn), each record's rate
	// read at its own time (provider.RateAt), so a window that opens
	// mid-page prices only the calls inside it (#2463)
	windows := map[string][]provider.RateWindow{}
	s := settings.Load()
	wires := s.Wires()
	return func(r Record) *catalog.Price {
		if r.OwnCost != nil {
			return &ownPrice // CostAt answers its own cost (#1874)
		}
		served := strings.TrimSpace(r.Served)
		if served == r.Model {
			served = ""
		}
		id := r.priceID()
		k := [5]string{id, r.Model, served, r.SessionProvider, r.Effort}
		if pr, ok := prices[k]; ok {
			return atWindow(pr, windows[id], r.Time)
		}
		var pr *catalog.Price
		var v catalog.Price
		var ws []provider.RateWindow
		var ok bool
		// the swap the ledger marks: the reply named another model than
		// asked, no rename of it (#5340)
		if servedInstead(wires, id, r.Model, r.Effort, served) {
			// a price the user set for the model asked for still says what
			// that model costs them, a zero included
			if v, ok, ws = provider.UserBasePriceIn(s, id, r.Model); !ok {
				// the model that ran is priced as itself, what the vendor
				// billed; with none, the asked-for one's price stands
				if v, ok, ws = provider.BasePriceIn(s, id, served); !ok {
					v, ok, ws = provider.BasePriceIn(s, id, r.Model)
				}
			}
		} else {
			v, ok, ws = provider.BasePriceIn(s, id, r.Model)
		}
		if !ok && served != "" {
			v, ok, ws = provider.BasePriceIn(s, id, served)
		}
		if ok {
			pr = &v
			windows[id] = ws
		} else if r.Provider == UnknownProvider && r.SessionProvider != "" {
			// A session file's usage entry names the provider the call went
			// through (omp's model_usage: provider openrouter, model
			// typesafe/jev-1.14, #4278), which its billing provider, the
			// unknown one, never says. The name is the file's word for the
			// route, not an account of ours, so it prices only what the
			// catalog itself lists under it: this very model id, at that
			// provider, at a price. A provider models.dev doesn't know, a
			// model it lists that provider without, and a listing with no
			// price are none: the row stays unpriced, its CSV cost empty,
			// rather than priced at another entry's guess.
			if v, ok := catalog.PriceOf(r.SessionProvider, r.Model); ok {
				pr = &v
				// the failed lookup's windows are the billing provider's,
				// not the session provider's: none holds for this price
				ws = nil
			}
		}
		if pr == nil {
			catalog.Missing() // models.dev may list it by now
		}
		prices[k] = pr
		return atWindow(pr, ws, r.Time)
	}
}

// servedInstead reports whether the vendor answered a call on model with
// another model, served, and not the same one under a name of its own: the
// swap the ledger marks (#5340). It is judged on the model the record is
// priced by and on the name that went out (a provider's wires can send the
// model under another id), and holds only when both read as another model,
// so a row the ledger does not mark swapped is never priced as one.
func servedInstead(wires map[string]string, providerID, model, effort, served string) bool {
	if served == "" || !Swapped(model, served) || ListedAs(providerID, model, served) {
		return false
	}
	sent := provider.SentNameIn(wires, providerID, model, effort)
	return sent == model || Swapped(sent, served)
}

// atWindow prices one record: the base price at the rate of the window
// the record's time falls in (#2463), the price itself when none holds
// or no price is known.
func atWindow(pr *catalog.Price, ws []provider.RateWindow, at time.Time) *catalog.Price {
	if pr == nil {
		return nil
	}
	if r := provider.RateAt(ws, at); r > 0 && r != 1 {
		p := pr.Times(r)
		return &p
	}
	return pr
}

// NewPricer totals requests' token-bearing attempts at one snapshot of the
// effective prices used by the ledger. Reuse it for one page of routes;
// make it again on the next read so a changed tariff re-prices history.
func NewPricer() func([]Record) Totals {
	priceOf, renamed := Pricer(), provider.UsageRenames()
	return func(recs []Record) Totals {
		var sum Totals
		for _, r := range recs {
			r.renameBy(renamed)
			sum.add(r, priceOf(r))
		}
		return sum
	}
}

// CSVHeader is the ledger's columns, as WriteCSV writes them.
var CSVHeader = []string{"time", "agent", "requested_model", "provider", "host", "model", "served_model", "swapped",
	"effort", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "reasoning_tokens",
	"cost_usd", "duration_ms", "ttft_ms", "status", "error", "session", "kind", "provider_key_id", "provider_key_name", "provider_account", "route_id",
	"request_id", "endpoint", "error_message", "error_type", "source", "rejected", "session_provider", "session_account", "session_official_login", "caller_key_id", "caller_key_name", "response_id", "upstream",
	"cache_write_5m_tokens", "cache_write_1h_tokens", "tries", "handed_on", "tier_asked", "tier_sent", "tier_served", "upstream_stop", "credits"}

// WriteCSV writes rows as CSV, a header first (cache_write_tokens is every
// cache write, as it always was, and the last two columns split it into
// 5-minute and 1-hour writes): times in RFC 3339 with
// their offset, the cost in USD at the effective price (empty when unknown), error
// "true" for a call that failed: answered with a status of 400 or more, or
// ended by an error a session file tells (not one its agent canceled, whose
// status is 499: Record.Failed). tries is how many tries the
// gateway made at the request the row ends, when more than one, and
// handed_on "true" for the row of a try that failed and handed the request
// on: the request's outcome is the row with the same route_id after it.
// tier_asked, tier_sent and tier_served are the service tier the client
// asked for, the one sent on the row's try and the one the reply said it
// ran at, each empty when there was none (Record.TierAsked). credits is
// what the reply said the call cost in its plan's credits, empty when it
// said none (Record.Credits).
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
		tries := ""
		if r.Tries > 0 {
			tries = n(r.Tries)
		}
		credits := ""
		if r.Credits != nil {
			credits = strconv.FormatFloat(*r.Credits, 'f', -1, 64)
		}
		routeID := ""
		if r.RouteID != 0 {
			routeID = strconv.FormatInt(r.RouteID, 10)
		}
		cw.Write([]string{r.Time.Format(time.RFC3339), r.Agent, r.Requested, r.Provider, r.Host, r.Model, r.Served,
			strconv.FormatBool(r.Swapped), r.Effort, n(r.Input), n(r.Output), n(r.CacheWrite), n(r.CacheRead), n(r.Reasoning),
			cost, strconv.FormatInt(r.Millis, 10), ttft, n(r.Status), strconv.FormatBool(r.Failed()), r.Session, r.Kind, r.ProviderKeyID, r.ProviderKeyName, r.Account(), routeID,
			r.RequestID, r.Endpoint, r.Error, r.ErrType, r.Source, strconv.FormatBool(r.IsRejected()), r.SessionProvider, r.SessionAccount, strconv.FormatBool(r.SessionOfficialLogin), r.CallerKeyID, r.CallerKeyName, r.ResponseID, r.Upstream,
			n(r.CacheWrite - min(r.CacheWrite1h, r.CacheWrite)), n(min(r.CacheWrite1h, r.CacheWrite)), tries, strconv.FormatBool(r.HandedOn),
			r.TierAsked, r.TierSent, r.TierServed, r.Stop, credits})
	}
	cw.Flush()
	return cw.Error()
}

// timeline is a period's empty timeline as of now, so quiet stretches still
// take their place: a point per hour of today, per day of a week or a month,
// and per week (from a Monday) of a longer time, which starts at first, the
// time of the oldest call. since is when it starts.
func timeline(p Period, now, first time.Time) (since time.Time, bucket string, pts []Point) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	bucket, pts = "day", []Point{}
	var n, hours int
	// a rolling window ends now: an hour each of two days or less, else a
	// day each from when it began (#2075)
	if d := p.Last(); d > 0 {
		since = now.Add(-d)
		if d <= 48*time.Hour {
			bucket = "hour"
			for h := 0; h <= int(d.Hours()); h++ {
				t := since.Add(time.Duration(h) * time.Hour)
				pts = append(pts, Point{Label: hourLabel(t), Time: t})
			}
		} else {
			n = int(d.Hours())/24 + 1
			for i := 0; i < n; i++ {
				t := since.AddDate(0, 0, i)
				pts = append(pts, Point{Label: t.Format("Jan 2"), Time: t})
			}
		}
		return since, bucket, pts
	}
	if first, until, ok := p.bounds(now.Location()); ok {
		// a picked range: an hour each of a single day, else a day each,
		// a week each (from a Monday) past 60 days as All has. One that
		// names its minutes is an hour each from the hour it begins in,
		// up to two days of them, else a day each from its first day.
		last := until.Add(-time.Nanosecond)
		since, n = first, calendarDays(first, last)+1
		if p.timed() {
			hour := time.Date(first.Year(), first.Month(), first.Day(), first.Hour(), 0, 0, 0, first.Location())
			if h := int((until.Sub(hour) + time.Hour - 1) / time.Hour); h <= 48 {
				since, bucket, hours = hour, "hour", h
			} else {
				since = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, first.Location())
			}
		}
		if n == 1 && !p.timed() {
			bucket = "hour"
		} else if n > 60 {
			bucket = "week"
			since = since.AddDate(0, 0, -((int(since.Weekday()) + 6) % 7))
			n = calendarDays(since, last)/7 + 1
		}
		p = ""
	}
	switch p {
	case "":
	case Today:
		since, bucket = day, "hour"
	case Week:
		since, n = day.AddDate(0, 0, -6), 7
	case Month:
		since, n = day.AddDate(0, 0, -29), 30
	default:
		if !first.IsZero() {
			f := first.In(now.Location())
			since = time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, now.Location())
		} else {
			since = day
		}
		if n = calendarDays(since, day) + 1; n > 60 {
			bucket = "week"
			// start on the Monday of the first week
			off := (int(since.Weekday()) + 6) % 7
			since = since.AddDate(0, 0, -off)
			n = calendarDays(since, day)/7 + 1
		}
	}
	switch bucket {
	case "hour":
		end := since.AddDate(0, 0, 1)
		if hours > 0 {
			end = since.Add(time.Duration(hours) * time.Hour)
		}
		for h := 0; since.Add(time.Duration(h) * time.Hour).Before(end); h++ {
			t := since.Add(time.Duration(h) * time.Hour)
			pts = append(pts, Point{Label: hourLabel(t), Time: t})
		}
	case "day":
		for i := 0; i < n; i++ {
			t := since.AddDate(0, 0, i)
			pts = append(pts, Point{Label: t.Format("Jan 2"), Time: t})
		}
	case "week":
		for i := 0; i < n; i++ {
			t := since.AddDate(0, 0, 7*i)
			pts = append(pts, Point{Label: t.Format("Jan 2"), Time: t})
		}
	}
	return since, bucket, pts
}

func calendarDays(from, to time.Time) int {
	date := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	return int(date(to).Sub(date(from)).Hours() / 24)
}

func bucketIndex(bucket string, since, t time.Time) int {
	switch bucket {
	case "hour":
		return int(t.Sub(since).Hours())
	case "week":
		return calendarDays(since, t) / 7
	default:
		return calendarDays(since, t)
	}
}

// addRow adds a ledger row, at the cost it already has.
func (t *Totals) addRow(r Row) {
	if r.TTFT > 0 && r.Answered() {
		t.Timed++
		t.TTFT += r.TTFT
		if n, w := r.Decode(); w > 0 {
			t.DecodeMs += w
			t.DecodeOut += n
			t.DecodeN++
		}
	}
	if n, ms := r.Floor(); ms > 0 {
		t.FloorOut += n
		t.FloorMs += ms
		t.FloorN++
	}
	t.count(r.Record)
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.CacheWrite1h += r.CacheWrite1h
	t.countCache(r.Record)
	t.Reasoning += r.Reasoning
	t.addBytes(&r.Record)
	switch {
	case r.Priced:
		t.Cost += r.Cost
	case r.Input+r.Output > 0:
		t.Unpriced++
	}
}

// plus adds what another sum counted: a row's own, made once by addRow,
// added to each of the many groups it is in instead of each reading the
// row again (#3493).
func (t *Totals) plus(d *Totals) {
	t.Calls += d.Calls
	t.Errors += d.Errors
	t.Retried += d.Retried
	t.Canceled += d.Canceled
	t.Loops += d.Loops
	t.Input += d.Input
	t.Output += d.Output
	t.CacheRead += d.CacheRead
	t.CacheWrite += d.CacheWrite
	t.CacheWrite1h += d.CacheWrite1h
	t.CacheUnknown += d.CacheUnknown
	t.CacheUnknownCalls += d.CacheUnknownCalls
	t.Reasoning += d.Reasoning
	t.Cost += d.Cost
	t.Unpriced += d.Unpriced
	t.Credits += d.Credits
	t.Credited += d.Credited
	t.Timed += d.Timed
	t.TTFT += d.TTFT
	t.DecodeMs += d.DecodeMs
	t.DecodeOut += d.DecodeOut
	t.DecodeN += d.DecodeN
	t.FloorOut += d.FloorOut
	t.FloorMs += d.FloorMs
	t.FloorN += d.FloorN
	t.UpBytes += d.UpBytes
	t.DownBytes += d.DownBytes
	t.ImageFilesSaved += d.ImageFilesSaved
}

// FloorAgents are the agents whose session files time a call from what
// asked for it to its reply's end, never from inside it: Claude Code's and
// Claude Desktop's from the line that asked, written before it was sent;
// Codex's call in a turn of several from what the model had last been given
// before the call's first output item, to its last (sessions' codexTiming).
// Their calls' tokens over that time are a floor of how fast the reply
// wrote. OpenCode's isn't checked against real sessions, so it tells none.
// app.js's LED_FLOOR_AGENTS has the same.
var FloorAgents = map[string]bool{"claude": true, "claude-desktop": true, "codex": true}

// A Codex turn of one call read from its session file is timed by the
// turn's time_to_first_token_ms and its end, and that window can be one
// network push, not the writing: Fallmonkey on Discord had a reply whose
// first token came at 6.8 s, the model working, and the rest 0.2 s later
// in one go, read as thousands of tok/s. Such a row's window after the first
// token tells a speed only when it is CodexWindowMs or more and its tokens
// (its answer, for one that reasoned) CodexBodyTokens or more; else it tells
// the least it wrote at over the whole turn (Row.Floor). app.js's ledDecode
// and ledFloor_ have the same.
const (
	CodexWindowMs   = 1000
	CodexBodyTokens = 100
)

// codexTurn is whether r is a Codex turn of one call read from its session
// file, the turn's first token known.
func (r Row) codexTurn() bool {
	return r.Source == "log" && r.Agent == "codex" && r.TTFT > 0
}

// Decode is the row's DecodeOf, a Codex turn of one call from its session
// file held to CodexWindowMs and CodexBodyTokens.
func (r Row) Decode() (tokens int, w int64) {
	n, w := r.Record.Decode()
	if w > 0 && r.codexTurn() && (w < CodexWindowMs || n < CodexBodyTokens) {
		return 0, 0
	}
	return n, w
}

// Floor is the tokens and ms a row read from a session file tells the least
// its reply wrote at by, when its speed wasn't measured. 0, 0 when it tells
// none. app.js's ledFloorSpeed is the same rule for one row.
//
// Mostly it is its output over its whole time, the wait for the first token
// in it, for a reply of 1,000 tokens or more over a second or more (a
// shorter one's wait is too much of it to tell anything; against Claude
// Code's own stream a floor read 2-5% under for 1,400 tokens or more, 11-33%
// for a shorter one) and no faster than MaxDecodeSpeed. A count of tokens
// is what bounds the share the wait can be, not a duration: a 5 s reply can
// be 4 s of wait (Fallmonkey on Discord asked for 5 s instead).
//
// A Codex turn of one call that reasoned has its first token from the turn's
// end, but not its first text, so DecodeOf tells no speed. Its answer, its
// output less the reasoning, was all written after the first token, so the
// answer over the time from the first token to the end is a floor of the
// answer's speed, the reasoning written after the first token counted in
// its time. Its output over that time, as Fallmonkey asked, would count the
// reasoning written before the first token too and read high: a gateway
// reply of 496 tokens, 224 of them reasoning, 827 ms after a 10.2 s first
// token, reads 600 tok/s so, its measured answer 415, and this 329. That
// window is held to what a measured one is (CodexWindowMs, CodexBodyTokens):
// 827 ms can be one push as well.
//
// A Codex turn of one call that tells neither, its window after the first
// token too short or its body too small, tells its output over the whole
// turn, the wait for the first token in it, by the same rule as any other
// such floor: 1,000 tokens or more over a second or more. It is the same
// formula, so it has the same gate: under 1,000 tokens the wait is too much
// of the time to tell anything (Fallmonkey on Discord: 11 tokens in 2.75 s
// read ≥4 tok/s while an 815-token Claude Code reply told nothing).
func (r Row) Floor() (tokens int, ms int64) {
	if r.Source != "log" || !FloorAgents[r.Agent] || !r.Answered() {
		return 0, 0
	}
	if _, w := r.Decode(); w > 0 {
		return 0, 0
	}
	if r.codexTurn() {
		if n := r.Output - r.Reasoning; r.Reasoning > 0 && n >= CodexBodyTokens {
			if w := DecodeWindow(n, r.Millis, r.TTFT); w >= CodexWindowMs {
				return n, w
			}
		}
	}
	if r.Output < 1000 || r.Millis < 1000 || int64(r.Output)*1000 > MaxDecodeSpeed*r.Millis {
		return 0, 0
	}
	return r.Output, r.Millis
}

// Dimensions are what calls are told apart by: the provider they went to,
// the agent that made them, the model, the model at the provider it
// went to ("modelAt"), so one model's speed at each provider can be compared
// (inaction on Discord), and the gateway key that made them ("callerKey"),
// so the clients a shared gateway serves can be compared (#2521).
var Dimensions = []string{"provider", "agent", "model", "modelAt", "callerKey"}

// NoCallerKey is the "callerKey" of a call that carried no gateway key: one
// that wasn't counted against any, as this computer's own agents' are, or
// that came before keys were recorded. As a filter it keeps only those.
const NoCallerKey = "none"

// Without is f with the pick of dimension d cleared: what d's shares are
// counted over, so that the others are still there to switch to.
func (f Filter) Without(d string) Filter {
	switch d {
	case "provider":
		f.Provider = ""
	case "agent":
		f.Agent = ""
	case "model":
		f.Model = ""
	case "modelAt":
		f.Model, f.Provider = "", ""
	case "callerKey":
		f.CallerKey = ""
	}
	return f
}

// ModelAtKey is a call's "modelAt": its provider's id and its model, as
// "provider/model" (a provider's id has no "/").
func ModelAtKey(provider, model string) string { return provider + "/" + model }

// key is the row's part of a dimension: its provider's id, its agent, its model.
func (r *Row) key(by string) string {
	switch by {
	case "provider":
		return r.Provider
	case "agent":
		return r.Agent
	case "modelAt":
		return ModelAtKey(r.Provider, r.Model)
	case "callerKey":
		if r.CallerKeyID == "" {
			return NoCallerKey
		}
		return r.CallerKeyID
	case "computer":
		if r.Computer == "" {
			return ThisComputer
		}
		return r.Computer
	}
	return r.Model
}

// keyer is Row.key for many rows, made once for each provider and model:
// a "modelAt" is a string built of two, which a page of thousands of rows
// would otherwise build again for each.
type keyer struct {
	public func(string) string
	models map[string]string
	at     map[[2]string]string
}

func newKeyer() keyer {
	return keyer{public: provider.PublicRefs(), models: map[string]string{}, at: map[[2]string]string{}}
}

func (m keyer) key(r *Row, by string) string {
	if by != "modelAt" && by != "model" {
		return r.key(by)
	}
	model := r.Model
	if m.public != nil {
		var ok bool
		model, ok = m.models[r.Model]
		if !ok {
			model = m.public(r.Model)
			m.models[r.Model] = model
		}
	}
	if by == "model" {
		return model
	}
	k, ok := m.at[[2]string{r.Provider, r.Model}]
	if !ok {
		k = ModelAtKey(r.Provider, model)
		m.at[[2]string{r.Provider, r.Model}] = k
	}
	return k
}

// AllTokens is what went in and out and through the cache.
func (t Totals) AllTokens() int { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// Share is one provider's, agent's or model's part of some rows.
type Share struct {
	ID string `json:"id"`
	Totals
}

// Breakdown sums rows by a dimension, the one with the most tokens first (then
// by calls, then by name).
func Breakdown(rows []Row, by string) []Share {
	at := map[string]*Share{}
	var out []*Share
	keys := newKeyer()
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		k := keys.key(&r, by)
		s := at[k]
		if s == nil {
			s = &Share{ID: k}
			at[k], out = s, append(out, s)
		}
		s.addRow(r)
	}
	slices.SortFunc(out, func(a, b *Share) int {
		switch {
		case a.AllTokens() != b.AllTokens():
			return b.AllTokens() - a.AllTokens()
		case a.Calls != b.Calls:
			return b.Calls - a.Calls
		}
		return strings.Compare(a.ID, b.ID)
	})
	res := make([]Share, len(out))
	for i, s := range out {
		res[i] = *s
	}
	return res
}

// Part is what one provider, agent or model had of a point of the timeline.
type Part struct {
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
	// how fast its replies came, as Totals has it: a chart of speed says
	// each one's at each point (#860)
	Timed     int   `json:"timed,omitempty"`
	TTFT      int64 `json:"ttft_ms,omitempty"`
	DecodeMs  int64 `json:"decode_ms,omitempty"`
	DecodeOut int   `json:"decode_out,omitempty"`
	// the bytes its requests sent up and read back (Totals.UpBytes), for
	// a chart of the traffic over time
	UpBytes   int64 `json:"up_bytes,omitempty"`
	DownBytes int64 `json:"down_bytes,omitempty"`
}

// add counts a row in the part.
func (p *Part) add(r Row) {
	if !r.HandedOn {
		p.Calls++
	}
	p.Tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
	if r.Priced {
		p.Cost += r.Cost
	}
	p.UpBytes += int64(r.UpBytes)
	p.DownBytes += int64(r.DownBytes)
	if r.TTFT > 0 && r.Answered() {
		p.Timed++
		p.TTFT += r.TTFT
		if n, w := r.Decode(); w > 0 {
			p.DecodeMs += w
			p.DecodeOut += n
		}
	}
}

// plus counts in the part a row whose own sums are d (Totals.addRow): what
// add counts, read from them.
func (p *Part) plus(d *Totals) {
	p.Calls += d.Calls
	p.Tokens += d.AllTokens()
	p.Cost += d.Cost
	p.Timed += d.Timed
	p.TTFT += d.TTFT
	p.DecodeMs += d.DecodeMs
	p.DecodeOut += d.DecodeOut
	p.UpBytes += d.UpBytes
	p.DownBytes += d.DownBytes
}

// SeriesPoint is a point of the timeline with its calls told apart, by each
// dimension, for a chart to stack them: the models of one point, say.
type SeriesPoint struct {
	Point
	By map[string]map[string]Part `json:"by"`
}

// seriesKeep is how many providers, agents or models a point tells apart,
// those with the most tokens over the period; the rest are what is left of
// the point's own sums.
const seriesKeep = 24

// LedgerSeries is the rows of a period, the ledger's or a filter's, summed by
// hour, day or week, for the chart over them. A row is of the point its
// time falls in; the rows are in any order.
func LedgerSeries(p Period, rows []Row) (bucket string, pts []SeriesPoint) {
	return ledgerSeriesAt(p, rows, localNow())
}

// ledgerSeriesAt is LedgerSeries with the period read at now, the moment the
// rows were read at, so that the chart is of the rows' day.
func ledgerSeriesAt(p Period, rows []Row, now time.Time) (bucket string, pts []SeriesPoint) {
	var first time.Time
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		if first.IsZero() || r.Time.Before(first) {
			first = r.Time
		}
	}
	since, bucket, base := timeline(p, now, first)
	pts = make([]SeriesPoint, len(base))
	for i := range base {
		pts[i] = SeriesPoint{Point: base[i], By: map[string]map[string]Part{}}
		for _, d := range Dimensions {
			pts[i].By[d] = map[string]Part{}
		}
	}
	kept := map[string]map[string]bool{}
	keys := newKeyer()
	for _, d := range Dimensions {
		kept[d] = map[string]bool{}
		for i, s := range Breakdown(rows, d) {
			if i < seriesKeep {
				kept[d][s.ID] = true
			}
		}
	}
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		t := r.Time.In(now.Location())
		if t.Before(since) {
			continue
		}
		i := bucketIndex(bucket, since, t)
		if i < 0 || i >= len(pts) {
			continue
		}
		pts[i].addRow(r)
		for _, d := range Dimensions {
			if k := keys.key(&r, d); kept[d][k] {
				part := pts[i].By[d][k]
				part.add(r)
				pts[i].By[d][k] = part
			}
		}
	}
	return bucket, pts
}

// Visit calls fn with each record from since on, as the ledger's index
// reads usage.jsonl: only its blocks that reach since are decoded.
// Each record is as it was recorded: a provider renamed since is named
// by the id it had then, which provider.UsageRenames tells (NewCoster
// does so itself).
func Visit(since time.Time, fn func(Record)) { readLogSnapshot().visit(since, fn) }

// VisitSessions is Visit for the records whose Session or NativeSession
// is one of sessions, read without making a Record of any other.
func VisitSessions(since time.Time, sessions []string, fn func(Record)) {
	readLogSnapshot().visitSessions(since, sessions, fn)
}

// NewCoster prices one record at a time at the effective prices the
// ledger uses, read once: its cost in USD, and false for a call with
// tokens but no known price.
func NewCoster() func(Record) (float64, bool) {
	priceOf, renamed := Pricer(), provider.UsageRenames()
	return func(r Record) (float64, bool) {
		r.renameBy(renamed)
		var t Totals
		t.add(r, priceOf(r))
		return t.Cost, t.Unpriced == 0
	}
}

// hourLabel is the hour of the real day a point stands for: in a zone whose
// day starts later (dayzone), its clock is that many hours behind.
func hourLabel(t time.Time) string {
	return t.Add(dayzone.Offset(t.Location())).Format("15")
}
