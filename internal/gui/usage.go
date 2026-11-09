package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// The Usage page's JSON is built in usageapi, which the gateway's
// /v1/magpie/usage answers from too.
type (
	usageGroup = usageapi.Group
	usageJSON  = usageapi.Usage
	ledgerRow  = usageapi.Row
	ledgerJSON = usageapi.Ledger
)

var (
	usageState   = usageapi.PageIn
	ledgerPage   = usageapi.LedgerPage
	periodOf     = usageapi.PeriodOf
	ledgerFilter = usageapi.FilterIn
)

func init() {
	usageapi.Clients = func() []usageapi.Client {
		var out []usageapi.Client
		for _, a := range agent.Clients() {
			out = append(out, usageapi.Client{ID: a.ID, Name: a.Name, Icon: a.Icon})
		}
		return out
	}
}

// csvStamp names the selected day, or, when no day is selected, the period
// and the day of now, the moment its rows were read at.
func csvStamp(p usage.Period, day string, now time.Time) string {
	if _, err := time.Parse(time.DateOnly, day); err == nil {
		return "magpie-requests-day-" + day
	}
	if p.IsRange() { // the days picked, which already say when
		// a minute an end names ("T09:30") without the colon Windows
		// doesn't take in a file name
		return "magpie-requests-" + strings.ReplaceAll(strings.Replace(string(p), "..", "-to-", 1), ":", "")
	}
	// a rolling window ("last:24h0m0s") without the colon, which Windows
	// doesn't take in a file name (#2075)
	return "magpie-requests-" + strings.ReplaceAll(string(p), ":", "-") + "-" + now.Format(time.DateOnly)
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
	mux.HandleFunc("GET /api/usage", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		// the days and hours of the reader's zone, which the page names
		writeJSON(rw, usageState(periodOf(r.URL.Query().Get("period")), usageapi.ZoneOf(r)))
	}))
	// the providers a deleted one's calls can be merged into: those there
	// are now and shown, by name
	mux.HandleFunc("GET /api/usage/deleted/into", func(rw http.ResponseWriter, r *http.Request) {
		type target struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Icon string `json:"icon"`
		}
		out := []target{}
		for _, p := range provider.All() {
			if p.Hidden {
				continue
			}
			name, icon := p.Name, p.Icon
			if name == "" {
				n, i := provider.KnownFace(p.ID)
				name = n
				if icon == "" {
					icon = i
				}
			}
			if name == "" {
				name = p.ID
			}
			out = append(out, target{p.ID, name, icon})
		}
		writeJSON(rw, out)
	})
	// a clear of usage from the Usage page (CrazyMouse_TT on X): the calls up to
	// now of one provider's model, or of everything with both "", stop
	// being counted on the usage pages. usage.jsonl keeps them, and a key's
	// spending limit and the allowances still count them
	// (settings.UsageCleared).
	mux.HandleFunc("POST /api/usage/clear", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Provider, Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := settings.ClearUsage(in.Provider, in.Model, usage.Clock()); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"ok": true})
	})
	// a deleted provider's calls, as the Usage page lists them under id:
	// merged into a provider there is now (Into, "" to part them again)
	// or shown by a name of the user's (Name, "" for its own); one not
	// sent stays as it is (CyberCaius on Discord)
	mux.HandleFunc("POST /api/usage/deleted", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID   string
			Into *string
			Name *string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Into != nil {
			if err := provider.MergeDeleted(in.ID, *in.Into); err != nil {
				fail(rw, err)
				return
			}
		}
		if in.Name != nil {
			if err := provider.NameDeleted(in.ID, *in.Name); err != nil {
				fail(rw, err)
				return
			}
		}
		writeJSON(rw, map[string]bool{"ok": true})
	})
	// the ledger: the period's calls, newest first, a page at a time
	mux.HandleFunc("GET /api/usage/requests", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		writeJSON(rw, ledgerPage(periodOf(q.Get("period")), ledgerFilter(r), offset, limit))
	}))
	// the gateway's bytes up and down so far and its requests in flight,
	// which the Requests read a moment apart for how fast its traffic flows
	// now; {} when another magpie serves the gateway
	mux.HandleFunc("GET /api/usage/traffic", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, trafficNow())
	})
	// the heatmap (#1369): the last 53 weeks a day each, of the requests the
	// page's filters keep, read from the same index as the page
	mux.HandleFunc("GET /api/usage/heatmap", coldTurn(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, usage.HeatmapOf(ledgerFilter(r)))
	}))
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
		// one moment for the rows and the day in the file's name, the
		// reader's day
		f := ledgerFilter(r)
		now := usage.Clock().In(f.Zone)
		rows := usage.LedgerOfAt(p, f, now).Rows
		rw.Header().Set("Content-Type", "text/csv; charset=utf-8")
		rw.Header().Set("Content-Disposition", `attachment; filename="`+csvStamp(p, q.Get("day"), now)+`.csv"`)
		usage.WriteCSV(rw, rows)
	})
	// the rows the ledger shows, all its pages, as a CSV in Downloads
	mux.HandleFunc("POST /api/usage/requests/export", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := periodOf(q.Get("period"))
		f := ledgerFilter(r)
		now := usage.Clock().In(f.Zone)
		rows := usage.LedgerOfAt(p, f, now).Rows
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
	// Only then are they read while the user has allowances read only
	// when asked (#1518).
	// &all=1 is the user's Refresh of every allowance by hand (the tray
	// panel's, the Usage page's, routing's): each saved Claude account's
	// /usage is run too, as its card's refresh runs it (#5380); opening
	// the page asks without it.
	mux.HandleFunc("GET /api/usage/quotas", func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if q := r.URL.Query(); q.Get("asked") != "" {
			if q.Get("all") != "" {
				provider.AskEveryClaudeUsage()
			} else {
				provider.AskClaudeUsage()
			}
			ctx = provider.Asked(ctx)
		}
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		// a WorkBuddy (China) account's card says how its daily check-in
		// went (#694)
		qs := provider.InProviderOrder(provider.WithCheckins(provider.Quotas(ctx)))
		// and what each window holds whole, by what magpie routed in it
		qs = usage.WithWindowHolds(qs, usage.Clock())
		// and when each began, for its Pace number (fflake33)
		qs = provider.WithWindowStarts(qs, usage.Clock())
		// and which an account can't spend for another window used up,
		// for the accounts' sum (#4791)
		qs = provider.WithHeldWindows(qs, usage.Clock())
		// and which the menu bar can show, by kind, for Settings to pick
		// among (settings.TrayWindows)
		qs = withTrayKinds(qs)
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
		writeJSON(rw, withTrayKinds(provider.WithHeldWindows(provider.WithWindowStarts(usage.WithWindowHolds(provider.InProviderOrder(provider.WithCheckins(provider.Quotas(ctx))), usage.Clock()), usage.Clock()), usage.Clock())))
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
	// and one account's, from Settings' Check in now (xiaowu_95660), by
	// its vendor and key as /api/settings lists them: asked once now,
	// whatever its switch says, which it leaves as it is
	mux.HandleFunc("POST /api/usage/checkin-account", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ By, Key string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Key == "" {
			fail(rw, errors.New("no account"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		rs := provider.CheckInAccount(ctx, in.By, in.Key)
		if len(rs) == 0 {
			fail(rw, errors.New("No daily check-in reaches this account"))
			return
		}
		writeJSON(rw, rs[0])
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
	// Historical whole-window estimates come only from this computer's
	// readings and ledger. Selecting a cycle never asks a vendor.
	mux.HandleFunc("GET /api/usage/quotas/history/holds", func(rw http.ResponseWriter, r *http.Request) {
		args := r.URL.Query()
		from, e1 := time.Parse(time.RFC3339Nano, args.Get("from"))
		to, e2 := time.Parse(time.RFC3339Nano, args.Get("to"))
		now := usage.Clock()
		if e1 != nil || e2 != nil || !from.Before(to) || to.After(now) || args.Get("provider") == "" || args.Get("user") == "" {
			http.Error(rw, "provider, user and a completed time range are required", http.StatusBadRequest)
			return
		}
		hist := provider.QuotaHistories(now.Add(-45*24*time.Hour), args.Get("provider"), args.Get("user"))
		writeJSON(rw, usage.HistoricalWindowHolds(provider.CachedCards(now), hist, from, to))
	})
	// spends one of a Codex account's rate-limit resets, which the page
	// has asked the user about first: the one they chose (Credit, an id
	// of its resets.each), else the one that runs out first; what it did
	// comes back. A card from a remote magpie (Provider "office/codex")
	// has that magpie spend it, on its account (#3850)
	mux.HandleFunc("POST /api/usage/codex-reset", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Provider, User, Credit string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Provider != "" && in.Provider != "codex" {
			ctx, cancel := context.WithTimeout(r.Context(), provider.RemoteResetWait+10*time.Second)
			defer cancel()
			out, err := provider.UseRemoteCodexReset(ctx, in.Provider, in.User, in.Credit)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, out)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		out, err := provider.SpendCodexReset(ctx, in.User, in.Credit)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, out)
	})
}

// trafficJSON is the gateway's bytes so far (gateway.Traffic) with its
// requests in flight; all left out when this process serves no gateway.
type trafficJSON struct {
	*gateway.Traffic
	Requests *int `json:"requests,omitempty"`
}

// trafficNow is what /api/usage/traffic answers.
func trafficNow() trafficJSON {
	gw := served.Load()
	if gw == nil {
		return trafficJSON{}
	}
	t, n := gw.Traffic(), gw.Busy().Requests
	return trafficJSON{Traffic: &t, Requests: &n}
}
