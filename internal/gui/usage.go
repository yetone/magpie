package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
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
	Keys       []usageGroup `json:"keys"`
	CallerKeys []usageGroup `json:"callerKeys"`
	Path       string       `json:"path"`
}

func usageState(p usage.Period) usageJSON {
	s := usage.Summarize(p)
	out := usageJSON{Summary: s, Agents: []usageGroup{}, Models: []usageGroup{}, Keys: []usageGroup{}, Path: tilde(usage.Path())}
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
	for _, g := range s.Keys {
		out.Keys = append(out.Keys, keyUsageGroup(g, providers))
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

func keyUsageGroup(g usage.Group, providers map[string]provider.Provider) usageGroup {
	ug := usageGroup{Group: g, Name: g.KeyName, Sub: g.Provider, Icon: "generic"}
	if g.KeyID == "" {
		ug.Name = "Key not recorded"
	} else if ug.Name == "" {
		ug.Name = g.KeyID
	}
	if p, ok := providers[g.Provider]; ok {
		ug.Sub = p.Name
		if p.Icon != "" {
			ug.Icon = p.Icon
		}
		for _, k := range p.KeyList() {
			if k.ID == g.KeyID {
				ug.KeyName = k.Name
				ug.Name = k.Name
				if ug.Name == "" {
					ug.Name = k.Masked
				}
				break
			}
		}
	}
	return ug
}

func periodOf(s string) usage.Period {
	switch p := usage.Period(s); p {
	case usage.Today, usage.Week, usage.Month, usage.All:
		return p
	}
	return usage.Month
}

func ledgerFilter(q url.Values) usage.Filter {
	return usage.Filter{Agent: q.Get("agent"), Key: q.Get("key"), CallerKey: q.Get("callerKey"), Failed: q.Get("failed") == "1", Query: q.Get("q")}
}

// ledgerRow is a usage.Row with the names the page shows it by.
type ledgerRow struct {
	usage.Row
	AgentName      string `json:"agentName"`
	Icon           string `json:"icon"` // the agent's
	ProviderName   string `json:"providerName"`
	KeyLabel       string `json:"keyLabel,omitempty"`
	CallerKeyLabel string `json:"callerKeyLabel,omitempty"`
}

type ledgerJSON struct {
	Period usage.Period `json:"period"`
	Rows   []ledgerRow  `json:"rows"`
	Offset int          `json:"offset"`
	Total  int          `json:"total"` // the rows the filter keeps, on every page
	usage.Totals
	// Agents: the agents with calls in the period, for the filter
	Agents     []ledgerAgent `json:"agents"`
	Keys       []usageGroup  `json:"keys"`
	Users      []usageGroup  `json:"users"`
	CallerKeys []usageGroup  `json:"callerKeys"`
}

type ledgerAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// ledgerPage is one page of the ledger: limit rows (100 when none is
// given, 500 at most) from offset.
func ledgerPage(p usage.Period, f usage.Filter, offset, limit int) ledgerJSON {
	rows, sum, ids, summary := usage.LedgerWithCallers(p, f)
	keys := summary.Keys
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	offset = max(0, min(offset, len(rows)))
	page := rows[offset:min(len(rows), offset+limit)]
	agents := map[string]*agent.Agent{}
	for _, a := range agent.Clients() {
		agents[a.ID] = a
	}
	names := map[string]string{}
	providers := map[string]provider.Provider{}
	for _, pr := range provider.All() {
		names[pr.ID] = pr.Name
		providers[pr.ID] = pr
	}
	who := func(id string) ledgerAgent {
		if a := agents[id]; a != nil {
			return ledgerAgent{ID: id, Name: a.Name, Icon: a.Icon}
		}
		return ledgerAgent{ID: id, Name: id, Icon: "generic"}
	}
	out := ledgerJSON{Period: p, Rows: make([]ledgerRow, 0, len(page)), Offset: offset, Total: len(rows), Totals: sum, Agents: []ledgerAgent{}, Keys: []usageGroup{}}
	out.CallerKeys = callerUsageGroups(summary)
	callerLabels := map[string]string{}
	for _, g := range out.CallerKeys {
		callerLabels[g.CallerKeyID] = g.Name
	}
	keyLabels := map[string]string{}
	for _, g := range keys {
		ug := keyUsageGroup(g, providers)
		out.Keys = append(out.Keys, ug)
		keyLabels[g.ID] = ug.Name
	}
	for _, r := range page {
		a := who(r.Agent)
		lr := ledgerRow{Row: r, AgentName: a.Name, Icon: a.Icon, ProviderName: names[r.Provider]}
		if lr.ProviderName == "" {
			lr.ProviderName = r.Provider
		}
		lr.KeyLabel = keyLabels[r.Provider+"#"+r.KeyID]
		lr.CallerKeyLabel = callerLabels[r.CallerKeyID]
		out.Rows = append(out.Rows, lr)
	}
	for _, id := range ids {
		out.Agents = append(out.Agents, who(id))
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
	// the same CSV to a browser (magpie web), which saves it itself
	mux.HandleFunc("GET /api/usage/requests.csv", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := periodOf(q.Get("period"))
		rows, _, _ := usage.Ledger(p, ledgerFilter(q))
		rw.Header().Set("Content-Type", "text/csv; charset=utf-8")
		rw.Header().Set("Content-Disposition", `attachment; filename="magpie-requests-`+string(p)+"-"+time.Now().Format("2006-01-02")+`.csv"`)
		usage.WriteCSV(rw, rows)
	})
	// the rows the ledger shows, all its pages, as a CSV in Downloads
	mux.HandleFunc("POST /api/usage/requests/export", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := periodOf(q.Get("period"))
		rows, _, _ := usage.Ledger(p, ledgerFilter(q))
		var b bytes.Buffer
		if err := usage.WriteCSV(&b, rows); err != nil {
			fail(rw, err)
			return
		}
		dir := downloads()
		stamp := "magpie-requests-" + string(p) + "-" + time.Now().Format("2006-01-02")
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
	// ?asked=1 is the user opening or refreshing the page, the one time
	// Claude Code's own /usage is run (provider.AskClaudeUsage).
	mux.HandleFunc("GET /api/usage/quotas", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("asked") != "" {
			provider.AskClaudeUsage()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		writeJSON(rw, provider.Quotas(ctx))
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
