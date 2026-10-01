// Package usage keeps the token count of every call the gateway serves, so
// magpie can show what each agent and model consumed and roughly what it cost.
// Records go to one JSON-lines file next to providers.json. Metadata leaves
// the machine only when OTLP export is explicitly enabled.
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Record is one call.
type Record struct {
	Operation       string    `json:"operation,omitempty"`
	RouteID         int64     `json:"route_id,omitempty"` // the gateway Route, shared by its attempts
	Time            time.Time `json:"t"`
	Agent           string    `json:"agent"` // magpie agent id, or the client's product name
	Provider        string    `json:"provider"`
	Host            string    `json:"host,omitempty"`          // where the call went: provider.Where then
	ProviderKeyID   string    `json:"providerKeyId,omitempty"` // fingerprint of the API key actually used
	ProviderKeyName string    `json:"providerKeyName,omitempty"`
	CallerKeyID     string    `json:"callerKeyId,omitempty"`
	CallerKeyName   string    `json:"callerKeyName,omitempty"`
	// SessionProvider is the provider ID recorded by the agent. SessionAccount
	// identifies the session's creator, not the account used for an API request.
	// Neither field establishes an upstream route from today's configuration.
	SessionProvider string `json:"session_provider,omitempty"`
	SessionAccount  string `json:"session_account,omitempty"`
	// SessionOfficialLogin marks a matched account's explicit official login
	// metadata, not this call's endpoint or billing authentication.
	SessionOfficialLogin bool   `json:"session_official_login,omitempty"`
	Model                string `json:"model"` // the provider's model id
	// Requested is the model id the agent asked for (a magpie alias, a
	// routing group, provider/model…), and Served the model the vendor's
	// reply says answered, when it named one: a ledger to set beside the
	// vendor's own bill. Neither is in a record written before they were.
	Requested  string `json:"req,omitempty"`
	Served     string `json:"served,omitempty"`
	Input      int    `json:"in"`
	Output     int    `json:"out"`
	CacheRead  int    `json:"cache_read,omitempty"`
	CacheWrite int    `json:"cache_write,omitempty"`
	Reasoning  int    `json:"reasoning,omitempty"`
	// Effort is the reasoning the model was asked for — a routing group's
	// pick for the turn, or the agent's own — as it takes it; "" for none
	Effort string `json:"effort,omitempty"`
	Millis int64  `json:"ms"`
	// TTFT: ms from the request to its reply's first content — text,
	// reasoning or a tool call — and FirstText to its first text, counted
	// as Millis is, so Millis-TTFT is how long the reply took to write;
	// none for a reply that wasn't streamed (#196)
	TTFT      int64 `json:"ttft_ms,omitempty"`
	FirstText int64 `json:"first_text_ms,omitempty"`
	Status    int   `json:"status"`
	// Error is why a call failed, in the vendor's words and cut short;
	// ErrType what its body called the error (rate_limit_error,
	// usage_limit_reached); RequestID the id the vendor gave the call; and
	// Endpoint the path the agent called, with the one it was sent to when
	// it went out in another protocol (/v1/messages → /v1/chat/completions)
	Error     string `json:"err,omitempty"`
	ErrType   string `json:"err_type,omitempty"`
	RequestID string `json:"rid,omitempty"`
	Endpoint  string `json:"ep,omitempty"`
	// Session is the conversation the call was part of, as its agent names
	// it (X-Magpie-Session, or the session header Claude Code, Codex or
	// OpenCode sends): several sessions on one model told apart
	Session string `json:"session,omitempty"`
	// NativeSession retains the client header when X-Magpie-Session overrides it.
	NativeSession string `json:"native_session,omitempty"`
	// Rejected is a request refused locally before an upstream was contacted.
	Rejected bool `json:"rejected,omitempty"`
	// Kind is what the agent made the call for when it isn't a turn of
	// the conversation: a Codex subagent's (review, compact, guardian…)
	Kind string `json:"kind,omitempty"`
	// Via is the computer whose magpie passed the call on to this one (a
	// Remote magpie provider there), Agent being the agent's on it; "" for
	// a call made on this computer
	Via string `json:"via,omitempty"`
	// Archive is where the request archive keeps the call, "<date>/<id>"
	// (gateway/archive.go), when it was on: the Usage page reads it back
	// by it long after Recent calls has let the call go (#447)
	Archive string `json:"archive,omitempty"`
}

// Path is the log file: ~/.config/magpie/usage.jsonl (XDG-aware).
func Path() string { return filepath.Join(filepath.Dir(provider.Path()), "usage.jsonl") }

var mu sync.Mutex

// Append writes one record. Errors are swallowed: accounting must never
// break a call.
func Append(r Record) {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	offerOTel(r)
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// IsRejected also recognizes local rejections written before the explicit flag.
func (r Record) IsRejected() bool { return r.Rejected || r.Provider == "" && r.Status >= 400 }

// Load streams complete records from the log. Historical request bodies are
// not retained by a global cache after the caller finishes with this snapshot.
func Load(since time.Time) []Record {
	mu.Lock()
	f, err := os.Open(Path())
	mu.Unlock()
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Record
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := reader.ReadBytes('\n')
		if err != nil {
			break
		} // an unfinished line is left for the next read
		var r Record
		if json.Unmarshal(b, &r) == nil && (since.IsZero() || !r.Time.Before(since)) {
			out = append(out, r)
		}
	}
	return out
}

// Known is an agent as its requests name it.
type Known struct {
	ID    string
	Names []string // its id and aliases, which a record may already carry
	UA    []string // what its User-Agent begins with
}

// Agents lists the agents magpie knows. Package agent sets it from each
// agent's own description, so they are listed in one place only.
var Agents func() []Known

var knownAgents = sync.OnceValue(func() []Known {
	if Agents == nil {
		return nil
	}
	return Agents()
})

// AgentOf names the agent behind a client User-Agent. Known agents map to
// their magpie id; anything else keeps its product name.
func AgentOf(ua string) string {
	ua = strings.TrimSpace(ua)
	name, _, _ := strings.Cut(ua, "/")
	name, _, _ = strings.Cut(name, " ")
	l := strings.ToLower(name)
	for _, k := range knownAgents() {
		if slices.Contains(k.Names, l) || slices.ContainsFunc(k.UA, func(p string) bool { return strings.HasPrefix(l, p) }) {
			return k.ID
		}
	}
	if name == "" {
		return "other"
	}
	return name
}

// ---- summaries --------------------------------------------------------------

// Period is a window of time to sum over.
type Period string

const (
	Today Period = "today"
	Week  Period = "7d"
	Month Period = "30d"
	All   Period = "all"
)

// Periods lists the windows in order.
var Periods = []Period{Today, Week, Month, All}

// Totals is a sum of calls.
type Totals struct {
	Calls      int     `json:"calls"`
	Errors     int     `json:"errors"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`     // USD at the effective price, for the priced calls
	Unpriced   int     `json:"unpriced"` // calls with tokens but no known price
	// Timed: the answered calls whose first token was timed (streamed),
	// TTFT the sum of their ttft_ms; DecodeMs the time from it to the end
	// of those that wrote any, over which DecodeOut tokens came: their
	// mean wait, and how fast they wrote (#196)
	Timed     int   `json:"timed,omitempty"`
	TTFT      int64 `json:"ttft_ms,omitempty"`
	DecodeMs  int64 `json:"decode_ms,omitempty"`
	DecodeOut int   `json:"decode_out,omitempty"`
}

// Tokens is what went in and out, excluding cache traffic.
func (t Totals) Tokens() int { return t.Input + t.Output }

// MeanTTFT is the mean ms to the first token of the timed calls, 0 for none.
func (t Totals) MeanTTFT() int64 {
	if t.Timed == 0 {
		return 0
	}
	return t.TTFT / int64(t.Timed)
}

// Speed is how fast the timed calls wrote, in tokens a second after their
// first: 0 for none.
func (t Totals) Speed() float64 {
	if t.DecodeMs <= 0 {
		return 0
	}
	return float64(t.DecodeOut) / (float64(t.DecodeMs) / 1000)
}

// FormatCost renders an effective-price cost, kept in USD everywhere it's
// stored, as the CLI and TUI show it: at amountUSD's own price when
// currency isn't "cny", else converted at rate (CNY per one USD, from
// internal/fx; a rate of 0 or below also falls back to USD, a stale or
// missing rate being no reason to hide the number). Whole dollars or yuan
// above 100, cents above 1, else thousandths, so a fraction of a cent
// still shows as something.
func FormatCost(amountUSD float64, currency string, rate float64) string {
	amount, sign := amountUSD, "$"
	if currency == "cny" && rate > 0 {
		amount, sign = amountUSD*rate, "¥"
	}
	switch {
	case amount >= 100:
		return fmt.Sprintf("%s%.0f", sign, amount)
	case amount >= 1:
		return fmt.Sprintf("%s%.2f", sign, amount)
	default:
		return fmt.Sprintf("%s%.3f", sign, amount)
	}
}

func (t *Totals) add(r Record, price *catalog.Price) {
	t.Calls++
	if r.Failed() {
		t.Errors++
	}
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.Reasoning += r.Reasoning
	if r.TTFT > 0 && !r.Failed() {
		t.Timed++
		t.TTFT += r.TTFT
		if r.Output > 0 && r.Millis > r.TTFT {
			t.DecodeMs += r.Millis - r.TTFT
			t.DecodeOut += r.Output
		}
	}
	if r.Input+r.Output == 0 {
		return
	}
	if price == nil {
		t.Unpriced++
		return
	}
	t.Cost += price.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
}

// Group is the share of one agent or model.
type Group struct {
	ID              string `json:"id"`
	Provider        string `json:"provider,omitempty"` // models only
	Model           string `json:"model,omitempty"`
	ProviderKeyID   string `json:"providerKeyId,omitempty"`
	ProviderKeyName string `json:"providerKeyName,omitempty"`
	CallerKeyID     string `json:"callerKeyId,omitempty"`
	CallerKeyName   string `json:"callerKeyName,omitempty"`
	// Host is where the calls went, when the provider's id has gone to
	// more than one place, or elsewhere than the provider goes now: its
	// calls are then told apart by it, not summed under the id.
	Host string `json:"host,omitempty"`
	// Agent is the agent a session is of (sessions only).
	Agent string `json:"agent,omitempty"`
	Totals
}

// who is where a subscription's calls went as the account signed in
// ("dee@example.com" of "api.factory.ai as dee@example.com"), the host
// alone for the rest. The account is what tells one place from another: a
// built-in moved onto its plugin sends the same account's calls through
// plugin://…, which isn't another place.
func who(where string) string {
	if i := strings.LastIndex(where, " as "); i >= 0 {
		return where[i+4:]
	}
	return where
}

// Point is one bar of the timeline.
type Point struct {
	Label string    `json:"label"`
	Time  time.Time `json:"time"`
	Totals
}

// Summary is a period, summed up.
type Summary struct {
	Period Period    `json:"period"`
	Since  time.Time `json:"since"`
	Totals
	Bucket       string  `json:"bucket"` // hour | day | week
	Series       []Point `json:"series"`
	Agents       []Group `json:"agents"`
	Models       []Group `json:"models"`
	ProviderKeys []Group `json:"providerKeys"`
	CallerKeys   []Group `json:"callerKeys"`
	// Sessions are the calls that named their session, by session.
	Sessions []Group `json:"sessions"`
}

// Summarize sums the log over a period, as of now.
func Summarize(p Period) Summary {
	return summarize(p, time.Now(), Load(time.Time{}))
}

func summarize(p Period, now time.Time, recs []Record) Summary {
	// a provider renamed since is counted under the id it has now
	if renamed := provider.Renamed(); len(renamed) > 0 {
		recs = slices.Clone(recs)
		for i, r := range recs {
			if id, ok := renamed[r.Provider]; ok {
				recs[i].Provider = id
			}
		}
	}
	s := Summary{Period: p, Bucket: "day", Agents: []Group{}, Models: []Group{}, ProviderKeys: []Group{}, CallerKeys: []Group{}, Sessions: []Group{}, Series: []Point{}}
	var first time.Time
	for _, r := range recs {
		if !r.IsRejected() && (first.IsZero() || r.Time.Before(first)) {
			first = r.Time
		}
	}
	s.Since, s.Bucket, s.Series = timeline(p, now, first)
	if p != Today && p != Week && p != Month {
		s.Period = All
	}

	priceOf := pricer()
	// the places each provider id went in the period, and goes now
	hosts := map[string]map[string]bool{}
	for _, r := range recs {
		if r.IsRejected() {
			continue
		}
		if r.Host != "" && !r.Time.Before(s.Since) {
			if hosts[r.Provider] == nil {
				hosts[r.Provider] = map[string]bool{}
			}
			hosts[r.Provider][who(r.Host)] = true
		}
	}
	goesNow := map[string]string{}
	keyProviders := map[string]bool{}
	for _, p := range provider.All() {
		goesNow[p.ID] = who(p.Where())
		keyProviders[p.ID] = p.Account == nil && p.Key != ""
	}
	for _, r := range recs {
		if r.ProviderKeyID != "" {
			keyProviders[r.Provider] = true
		}
	}
	agents := map[string]*Group{}
	models := map[string]*Group{}
	keys := map[string]*Group{}
	callerKeys := map[string]*Group{}
	sessions := map[string]*Group{}
	for _, r := range recs {
		if r.IsRejected() {
			continue
		}
		t := r.Time.In(now.Location())
		if t.Before(s.Since) {
			continue
		}
		pr := priceOf(r)
		s.Totals.add(r, pr)
		i := bucketIndex(s.Bucket, s.Since, t)
		if i >= 0 && i < len(s.Series) {
			s.Series[i].add(r, pr)
		}
		id := AgentOf(r.Agent) // one kept before magpie knew the agent by name
		a := agents[id]
		if a == nil {
			a = &Group{ID: id}
			agents[id] = a
		}
		a.add(r, pr)
		k, host := r.Provider+"/"+r.Model, ""
		if w := who(r.Host); w != "" && (len(hosts[r.Provider]) > 1 || w != goesNow[r.Provider]) {
			k, host = k+" @ "+w, w
		}
		m := models[k]
		if m == nil {
			m = &Group{ID: k, Provider: r.Provider, Model: r.Model, Host: host}
			models[k] = m
		}
		m.add(r, pr)
		if r.CallerKeyID != "" {
			g := callerKeys[r.CallerKeyID]
			if g == nil {
				g = &Group{ID: r.CallerKeyID, CallerKeyID: r.CallerKeyID}
				callerKeys[r.CallerKeyID] = g
			}
			g.CallerKeyName = r.CallerKeyName
			g.add(r, pr)
		}
		if keyProviders[r.Provider] {
			id := r.Provider + "#" + r.ProviderKeyID
			g := keys[id]
			if g == nil {
				g = &Group{ID: id, Provider: r.Provider, ProviderKeyID: r.ProviderKeyID}
				keys[id] = g
			}
			g.ProviderKeyName = r.ProviderKeyName
			g.add(r, pr)
		}
		if r.Session != "" {
			g := sessions[id+"|"+r.Session]
			if g == nil {
				g = &Group{ID: r.Session, Agent: id}
				sessions[id+"|"+r.Session] = g
			}
			g.add(r, pr)
		}
	}
	for _, g := range agents {
		s.Agents = append(s.Agents, *g)
	}
	for _, g := range models {
		s.Models = append(s.Models, *g)
	}
	byTokens := func(gs []Group) {
		sort.SliceStable(gs, func(i, j int) bool {
			if a, b := gs[i].Tokens(), gs[j].Tokens(); a != b {
				return a > b
			}
			if gs[i].Calls != gs[j].Calls {
				return gs[i].Calls > gs[j].Calls
			}
			return gs[i].ID < gs[j].ID
		})
	}
	byTokens(s.Agents)
	byTokens(s.Models)
	for _, g := range keys {
		s.ProviderKeys = append(s.ProviderKeys, *g)
	}
	byTokens(s.ProviderKeys)
	for _, g := range callerKeys {
		s.CallerKeys = append(s.CallerKeys, *g)
	}
	byTokens(s.CallerKeys)
	for _, g := range sessions {
		s.Sessions = append(s.Sessions, *g)
	}
	byTokens(s.Sessions)
	return s
}

// ---- last seen --------------------------------------------------------------

// seen is when each agent's latest request reached the gateway in this
// process — at its start, where a record is written only once it is answered.
var seen sync.Map // agent id → time.Time

// Via is where the gateway sent a session's calls: one model at one
// reasoning effort, how often and how much.
type Via struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Effort   string    `json:"effort,omitempty"`
	Calls    int       `json:"calls"`
	Tokens   int       `json:"tokens"` // in and out, as Totals counts them
	Last     time.Time `json:"last"`
}

// Vias is what the gateway sent each session's calls to since a time, by
// agent id and the session's id ("codex|<id>"), the most calls first.
func Vias(since time.Time) map[string][]Via {
	out := map[string][]Via{}
	for _, r := range Load(since) {
		if r.IsRejected() || r.Session == "" || r.Model == "" {
			continue
		}
		k := AgentOf(r.Agent) + "|" + r.Session
		vs := out[k]
		i := slices.IndexFunc(vs, func(v Via) bool { return v.Provider == r.Provider && v.Model == r.Model && v.Effort == r.Effort })
		if i < 0 {
			vs, i = append(vs, Via{Provider: r.Provider, Model: r.Model, Effort: r.Effort}), len(vs)
		}
		vs[i].Calls++
		vs[i].Tokens += r.Input + r.Output
		if r.Time.After(vs[i].Last) {
			vs[i].Last = r.Time
		}
		out[k] = vs
	}
	for _, vs := range out {
		sort.SliceStable(vs, func(i, j int) bool { return vs[i].Calls > vs[j].Calls })
	}
	return out
}

// Saw notes a request from an agent arriving now.
func Saw(agent string) { seen.Store(agent, time.Now()) }

// LastSeen is when a request from the agent last reached the gateway: this
// process's own, else the newest in the log's last stretch. Zero if none.
func LastSeen(agent string) time.Time {
	var last time.Time
	if t, ok := seen.Load(agent); ok {
		last = t.(time.Time)
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := os.Open(Path())
	if err != nil {
		return last
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(st.Size()-tail, 0)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Agent == agent && r.Via == "" && r.Time.After(last) {
			last = r.Time
		}
	}
	return last
}
