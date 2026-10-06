package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// usageGroup is a usage.Group with what the UI needs to draw it.
type usageGroup struct {
	usage.Group
	Name string `json:"name"`
	Sub  string `json:"sub,omitempty"`  // models: the provider's name
	Icon string `json:"icon,omitempty"` // a real logo, or "generic" for an unknown client
}

type usageJSON struct {
	usage.Summary
	Agents     []usageGroup `json:"agents"`
	Models     []usageGroup `json:"models"`
	Accounts   []usageGroup `json:"accounts"`
	CallerKeys []usageGroup `json:"callerKeys"`
	Path       string       `json:"path"`
}

func usageState(p usage.Period) usageJSON {
	s := usage.Summarize(p)
	out := usageJSON{Summary: s, Agents: []usageGroup{}, Models: []usageGroup{}, Accounts: []usageGroup{}, Path: tilde(usage.Path())}
	agents := map[string]*agent.Agent{}
	for _, a := range agent.Clients() {
		agents[a.ID] = a
	}
	for _, g := range s.Agents {
		ug := usageGroup{Group: g, Name: g.ID, Icon: "generic"}
		if a := agents[g.ID]; a != nil {
			ug.Name, ug.Icon = a.Name, a.Icon
		}
		out.Agents = append(out.Agents, ug)
	}
	providers := map[string]provider.Provider{}
	for _, p := range provider.All() {
		providers[p.ID] = p
	}
	for _, g := range s.Models {
		ug := usageGroup{Group: g, Name: g.Model, Sub: g.Provider, Icon: "generic"}
		if p, ok := providers[g.Provider]; ok {
			ug.Sub = p.Name
			if p.Icon != "" {
				ug.Icon = p.Icon
			}
		}
		if g.Host != "" {
			ug.Sub += " · " + g.Host
		}
		out.Models = append(out.Models, ug)
	}
	// each subscription account's share, by the account that answered
	// (#557); Name "" is the calls whose record names none, the page
	// saying "account not recorded"
	for _, g := range s.Accounts {
		ug := usageGroup{Group: g, Name: g.Account, Sub: g.Provider, Icon: "generic"}
		if p, ok := providers[g.Provider]; ok {
			ug.Sub = p.Name
			if p.Icon != "" {
				ug.Icon = p.Icon
			}
		}
		out.Accounts = append(out.Accounts, ug)
	}
	out.CallerKeys = callerUsageGroups(s)
	return out
}

func callerUsageGroups(s usage.Summary) []usageGroup {
	keys := []usageGroup{}
	current, _ := access.List()
	names := map[string]string{}
	for _, k := range current {
		names[k.ID] = k.Name
	}
	for _, g := range s.CallerKeys {
		n := names[g.CallerKeyID]
		if n == "" {
			n = g.CallerKeyName
		}
		if n == "" {
			n = g.CallerKeyID
		}
		keys = append(keys, usageGroup{Group: g, Name: n, Icon: "generic"})
	}
	return keys
}

func periodOf(s string) usage.Period {
	switch p := usage.Period(s); p {
	case usage.Today, usage.Week, usage.Month, usage.All:
		return p
	}
	return usage.Month
}

// csvStamp names the selected day, or, when no day is selected, the period
// and the day of now, the moment its rows were read at.
func csvStamp(p usage.Period, day string, now time.Time) string {
	if _, err := time.Parse(time.DateOnly, day); err == nil {
		return "magpie-requests-day-" + day
	}
	return "magpie-requests-" + string(p) + "-" + now.Format(time.DateOnly)
}

func ledgerFilter(q url.Values) usage.Filter {
	id, _ := strconv.ParseInt(q.Get("route"), 10, 64)
	return usage.Filter{Day: q.Get("day"), RouteID: id, Model: q.Get("model"), Agent: q.Get("agent"), Provider: q.Get("provider"), Purpose: q.Get("purpose"), Account: q.Get("account"), CallerKey: q.Get("callerKey"), Failed: q.Get("failed") == "1", Query: q.Get("q"), Computer: q.Get("computer")}
}

// ledgerRow is a usage.Row with the names the page shows it by.
type ledgerRow struct {
	usage.Row
	CallerKeyLabel string `json:"callerKeyLabel,omitempty"`
	AgentName      string `json:"agentName"`
	Icon           string `json:"icon"` // the agent's
	ProviderName   string `json:"providerName"`
	Access         string `json:"access,omitempty"` // known account/route type, independent of model maker
	PricingModel   string `json:"pricing_model,omitempty"`
	// ComputerName is the other computer a call was made on, shared through sync (#542)
	ComputerName string `json:"computerName,omitempty"`
}

type ledgerJSON struct {
	CallerKeys []usageGroup `json:"callerKeys"`
	Period     usage.Period `json:"period"`
	Rows       []ledgerRow  `json:"rows"`
	Offset     int          `json:"offset"`
	Total      int          `json:"total"` // the rows the filter keeps, on every page
	// Series: the period by hour, day or week (Bucket), before the day filter
	Bucket string              `json:"bucket"`
	Series []usage.SeriesPoint `json:"series"`
	// By: the rows told apart by provider, agent, model and model at each
	// provider ("modelAt", named "model · provider"), the most tokens
	// first. The one by a dimension the filter has picked is of the rows
	// without that pick, so the others are still there to switch to.
	By      map[string][]ledgerShare `json:"by"`
	ChartBy map[string][]ledgerShare `json:"chartBy,omitempty"`
	Day     string                   `json:"day,omitempty"`
	usage.Totals
	// Agents, Providers and Purposes: those with calls in the period, for the filters
	Agents    []ledgerAgent `json:"agents"`
	Providers []ledgerAgent `json:"providers"`
	Purposes  []string      `json:"purposes"`
	// Accounts: the subscription accounts that answered calls in the
	// period, for the Account filter (#557)
	Accounts []ledgerAccount `json:"accounts"`
	// Computers: this one ("this", no name) and the others whose usage
	// sync brought (#542), for the filter; none while there are none
	Computers []ledgerShare `json:"computers,omitempty"`
}

// ledgerShare is a usage.Share with the name and logo the page shows it by.
type ledgerShare struct {
	usage.Share
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// ledgerAccount is a subscription account the Account filter offers: its
// name, and the providers it answered for, by name.
type ledgerAccount struct {
	ID        string   `json:"id"`
	Providers []string `json:"providers"`
}

type ledgerAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// ledgerPage is one page of the ledger: limit rows (100 when none is
// given, 500 at most) from offset.
func ledgerPage(p usage.Period, f usage.Filter, offset, limit int) ledgerJSON {
	l := usage.QueryPage(p, f, offset, limit)
	offset = max(0, min(offset, l.Total))
	page := l.Rows
	agents := map[string]*agent.Agent{}
	for _, a := range agent.Clients() {
		agents[a.ID] = a
	}
	// A session file without route evidence is a local source, not a supplier.
	names := map[string]string{usage.UnknownProvider: "Local session"}
	icons := map[string]string{}
	access := map[string]string{}
	for _, pr := range provider.All() {
		names[pr.ID], icons[pr.ID] = pr.Name, pr.Icon
		if pr.Account != nil {
			access[pr.ID] = "subscription"
		} else if pr.Key != "" {
			access[pr.ID] = "api"
		}
	}
	who := func(id string) ledgerAgent {
		if a := agents[id]; a != nil {
			return ledgerAgent{ID: id, Name: a.Name, Icon: a.Icon}
		}
		return ledgerAgent{ID: id, Name: id, Icon: "generic"}
	}
	which := func(id string) ledgerAgent {
		a := ledgerAgent{ID: id, Name: names[id], Icon: icons[id]}
		if a.Name == "" {
			a.Name = id
		}
		if a.Icon == "" {
			a.Icon = "generic"
		}
		return a
	}
	out := ledgerJSON{Period: p, Rows: make([]ledgerRow, 0, len(page)), Offset: offset, Total: l.Total, Totals: l.Sum, Agents: []ledgerAgent{}, Providers: []ledgerAgent{}, Accounts: []ledgerAccount{}}
	out.Bucket, out.Series = l.Bucket, l.Series
	out.Purposes = l.Purposes
	out.Day = f.Day
	out.CallerKeys = callerUsageGroups(usage.Summary{CallerKeys: l.CallerKeys})
	callerLabels := map[string]string{}
	for _, g := range out.CallerKeys {
		callerLabels[g.CallerKeyID] = g.Name
	}
	for _, r := range page {
		a := who(r.Agent)
		lr := ledgerRow{Row: r, AgentName: a.Name, Icon: a.Icon, ProviderName: names[r.Provider]}
		if r.Source != "log" {
			lr.Access = access[r.Provider]
		}
		if r.Priced {
			if model := provider.PricedName(r.Model); model != r.Model {
				lr.PricingModel = model
			}
		}
		if lr.ProviderName == "" {
			lr.ProviderName = r.Provider
		}
		lr.CallerKeyLabel = callerLabels[r.CallerKeyID]
		if r.Computer != "" {
			lr.ComputerName = l.Names[r.Computer]
			if lr.ComputerName == "" {
				lr.ComputerName = r.Computer
			}
		}
		out.Rows = append(out.Rows, lr)
	}
	for _, id := range l.Agents {
		out.Agents = append(out.Agents, who(id))
	}
	for _, id := range l.Providers {
		out.Providers = append(out.Providers, which(id))
	}
	// one choice per account, most used first, with the providers it
	// answered for: the same email may be a Codex and a Claude account
	at := map[string]int{}
	for _, g := range l.Accounts {
		name := which(g.Provider).Name
		if i, ok := at[g.Account]; ok {
			if a := &out.Accounts[i]; !slices.Contains(a.Providers, name) {
				a.Providers = append(a.Providers, name)
			}
			continue
		}
		at[g.Account] = len(out.Accounts)
		out.Accounts = append(out.Accounts, ledgerAccount{ID: g.Account, Providers: []string{name}})
	}
	// what each is of: the rows of the filter, or, for the dimension the
	// filter has picked, of the rows without that pick
	sharesJSON := func(by map[string][]usage.Share) map[string][]ledgerShare {
		out := map[string][]ledgerShare{}
		for _, d := range usage.Dimensions {
			shares := []ledgerShare{}
			for _, s := range by[d] {
				ls := ledgerShare{Share: s, Name: s.ID}
				switch d {
				case "provider":
					a := which(s.ID)
					ls.Name, ls.Icon = a.Name, a.Icon
				case "agent":
					a := who(s.ID)
					ls.Name, ls.Icon = a.Name, a.Icon
				case "modelAt":
					// the model, and the provider it went to
					prov, model, _ := strings.Cut(s.ID, "/")
					a := which(prov)
					ls.Name, ls.Icon = model+" · "+a.Name, a.Icon
				}
				shares = append(shares, ls)
			}
			out[d] = shares
		}
		return out
	}
	out.By = sharesJSON(l.By)
	if l.ChartBy != nil {
		out.ChartBy = sharesJSON(l.ChartBy)
	}
	for _, s := range l.Computers {
		ls := ledgerShare{Share: s, Name: l.Names[s.ID]}
		if s.ID != usage.ThisComputer && ls.Name == "" {
			ls.Name = s.ID
		}
		out.Computers = append(out.Computers, ls)
	}
	return out
}

// contentJSON is what was said in a request, or why it can't be told.
type contentJSON struct {
	Found bool `json:"found"`
	// Why not: "session" (the request named none), "agent" (magpie reads the session
	// files of Claude Code, Claude Desktop and Codex only), "missing" (the files have no
	// such call: deleted, moved, or not written yet) or "read" (the file wouldn't read)
	Why   string `json:"why,omitempty"`
	Model string `json:"model,omitempty"`
	sessions.Content
}

// requestContent finds the call a request is in its agent's session files and reads it.
func requestContent(agent, session string, from, to, at time.Time) contentJSON {
	out := contentJSON{Content: sessions.Content{Input: []sessions.Part{}, Output: []sessions.Part{}}}
	switch {
	case session == "":
		out.Why = "session"
	case agent != "claude" && agent != "claude-desktop" && agent != "codex":
		out.Why = "agent"
	default:
		c, ok := sessions.FindCall(session, from, to, at)
		if !ok {
			out.Why = "missing"
			break
		}
		content, err := sessions.ContentOf(c)
		if err != nil {
			out.Why = "read"
			break
		}
		out.Found, out.Model, out.Content = true, c.Model, content
	}
	return out
}

func usageRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/usage", func(rw http.ResponseWriter, r *http.Request) {
		p := usage.Period(r.URL.Query().Get("period"))
		switch p {
		case usage.Today, usage.Week, usage.Month, usage.All:
		default:
			p = usage.Month
		}
		writeJSON(rw, usageState(p))
	})
	// the ledger: the period's calls, newest first, a page at a time
	mux.HandleFunc("GET /api/usage/requests", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		writeJSON(rw, ledgerPage(periodOf(q.Get("period")), ledgerFilter(q), offset, limit))
	})
	// what was said in one request, read from the agent's session file when the
	// row is opened, between two times (the call's own, or a gateway request's
	// span): magpie keeps no copy
	mux.HandleFunc("GET /api/usage/requests/content", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, err1 := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, err2 := time.Parse(time.RFC3339Nano, q.Get("to"))
		if err1 != nil || err2 != nil {
			http.Error(rw, "from and to are times", http.StatusBadRequest)
			return
		}
		// at: when the call should have ended (a gateway request's start and
		// its time); none is the window's end
		at, err := time.Parse(time.RFC3339Nano, q.Get("at"))
		if err != nil {
			at = to
		}
		writeJSON(rw, requestContent(q.Get("agent"), q.Get("session"), from, to, at))
	})
	// the same CSV to a browser (magpie web), which saves it itself
	mux.HandleFunc("GET /api/usage/requests.csv", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := periodOf(q.Get("period"))
		// one moment for the rows and the day in the file's name
		now := usage.Clock()
		rows := usage.LedgerOfAt(p, ledgerFilter(q), now).Rows
		rw.Header().Set("Content-Type", "text/csv; charset=utf-8")
		rw.Header().Set("Content-Disposition", `attachment; filename="`+csvStamp(p, q.Get("day"), now)+`.csv"`)
		usage.WriteCSV(rw, rows)
	})
	// the rows the ledger shows, all its pages, as a CSV in Downloads
	mux.HandleFunc("POST /api/usage/requests/export", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := periodOf(q.Get("period"))
		now := usage.Clock()
		rows := usage.LedgerOfAt(p, ledgerFilter(q), now).Rows
		var b bytes.Buffer
		if err := usage.WriteCSV(&b, rows); err != nil {
			fail(rw, err)
			return
		}
		dir := downloads()
		stamp := csvStamp(p, q.Get("day"), now)
		name := filepath.Join(dir, stamp+".csv")
		for i := 2; ; i++ { // never over an earlier one
			if _, err := os.Stat(name); err != nil {
				break
			}
			name = filepath.Join(dir, fmt.Sprintf("%s-%d.csv", stamp, i))
		}
		if err := edit.WriteAtomic(name, b.Bytes()); err != nil {
			fail(rw, err)
			return
		}
		_ = w.OpenFolder(dir) // saved either way; the path is in the answer
		writeJSON(rw, map[string]any{"path": tilde(name), "rows": len(rows)})
	})
	// The subscriptions' quotas, the plans' bought with a key, and the
	// keys' balances come from the
	// vendors, which can be slow or unreachable, so the page asks for them
	// apart from the local log.
	// ?asked=1 is the user opening or refreshing the page: Claude Code's
	// own /usage is run at once (provider.AskClaudeUsage).
	mux.HandleFunc("GET /api/usage/quotas", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("asked") != "" {
			provider.AskClaudeUsage()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		// a WorkBuddy (China) account's card says how its daily check-in
		// went (#694)
		qs := provider.WithCheckins(provider.Quotas(ctx))
		// an account's card may be the stale copy a read under way will
		// replace: the page asks again until it has landed (#959)
		if provider.SubscriptionUsageReading() {
			rw.Header().Set("X-Magpie-Reading", "1")
		}
		writeJSON(rw, qs)
	})
	// One card read again, from its refresh button (#840): ?provider= and,
	// of a card with several accounts, &user=; the others are left as they
	// were read. The answer is every card, as GET's.
	mux.HandleFunc("POST /api/usage/quotas/refresh", func(rw http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("provider")
		if id == "" {
			http.Error(rw, "provider is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		provider.RefreshUsage(ctx, id, r.URL.Query().Get("user"))
		writeJSON(rw, provider.WithCheckins(provider.Quotas(ctx)))
	})
	// WorkBuddy's daily check-in pressed now, from the Usage card, for
	// each account not in yet today, as `magpie accounts checkin` does; the
	// card is read again after
	mux.HandleFunc("POST /api/usage/workbuddy-checkin", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInWorkBuddy(ctx)
		if rs == nil {
			rs = []provider.WorkBuddyCheckin{}
		}
		writeJSON(rw, rs)
	})
	// and Trae CN's, for each Trae CN account (#694)
	mux.HandleFunc("POST /api/usage/trae-checkin", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInTrae(ctx)
		if rs == nil {
			rs = []provider.WorkBuddyCheckin{}
		}
		writeJSON(rw, rs)
	})
	// and MiniMax Code's, for each MiniMax Code (China) account (#811)
	mux.HandleFunc("POST /api/usage/minimax-checkin", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInMiniMax(ctx)
		if rs == nil {
			rs = []provider.WorkBuddyCheckin{}
		}
		writeJSON(rw, rs)
	})
	// and Qoder's daily credits, for each Qoder and Qoder CN account
	mux.HandleFunc("POST /api/usage/qoder-checkin", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInQoder(ctx)
		if rs == nil {
			rs = []provider.WorkBuddyCheckin{}
		}
		writeJSON(rw, rs)
	})
	// and a plugin's own, for each account of the provider it names
	mux.HandleFunc("POST /api/usage/plugin-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Provider string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Provider == "" {
			fail(rw, errors.New("no provider"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInPlugins(ctx, in.Provider)
		if rs == nil {
			rs = []provider.WorkBuddyCheckin{}
		}
		writeJSON(rw, rs)
	})
	// what was left of each window over time, for the quota cards' curves
	// (#651); ?days= back
	mux.HandleFunc("GET /api/usage/quotas/history", func(rw http.ResponseWriter, r *http.Request) {
		days := r.URL.Query().Get("days")
		hs := provider.QuotaHistories(provider.QuotaHistorySince(days, time.Now()), "", "")
		// and a remote magpie's, for its cards (office/codex)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		writeJSON(rw, append(hs, provider.RemoteQuotaHistories(ctx, days)...))
	})
	// spends one of a Codex account's rate-limit resets, which the page
	// has asked the user about first; what it did comes back
	mux.HandleFunc("POST /api/usage/codex-reset", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		out, err := provider.UseCodexReset(ctx, in.User)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, out)
	})
}
