package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// Settings → Allowances in the menu bar: the windows of the subscriptions and
// plans ticked there, beside the tray icon, for keeping an eye on them
// without opening magpie. The Mac's menu bar draws each as its logo with
// its two windows stacked, "42%" over "18%", side by side (trayimage), or
// as the windows alone, a thin line between cards, when Settings turns the
// logos off; a tray elsewhere gets them as text ("42% · 18% | 10% · 5%"), Windows' in
// the icon's tooltip only, having no room for any.

// trayUsageEvery is how often the text is brought up to date, as Settings
// says (every 3 minutes unless told otherwise). The vendors are asked no
// more than the Usage page asks them: their answers are cached for a
// minute, and this reads the cache.
func trayUsageEvery() time.Duration {
	return time.Duration(settings.Shared().TrayUsageEvery) * time.Minute
}

// trayCardID names a card for settings.TrayUsage: its provider, and the
// account when there is one, as two of one vendor can be signed in.
func trayCardID(q provider.SubscriptionQuota) string {
	if q.User == "" {
		return q.Provider
	}
	return q.Provider + "|" + q.User
}

// trayWindows are the windows a card's text shows: those that stop the
// account (not on-demand spending, nor one that counts a single model's
// use), the shortest first where the vendor says how long they run. A card
// with none of those shows what it has in their place (trayShared, then
// trayModels): Antigravity reports only its pools, which routing sets
// aside, and its models' own windows, and its card was left out of the
// menu bar for that though ticked (nivekxft on X).
func trayWindows(q provider.SubscriptionQuota) []provider.QuotaWindow {
	var ws []provider.QuotaWindow
	for _, w := range q.Windows {
		if !w.Aside && w.Model == "" {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		ws = trayShared(q.Windows)
	}
	if len(ws) == 0 {
		return trayModels(q.Windows)
	}
	slices.SortStableFunc(ws, func(a, b provider.QuotaWindow) int {
		if a.Span == 0 || b.Span == 0 {
			return 0
		}
		return cmp.Compare(a.Span, b.Span)
	})
	return ws
}

// trayCounted is whether a window tells a share used, one the menu bar can
// say as a percent: not a row of text, an unlimited one, a wallet's
// balance, nor a figure of money with no share (Factory's extra usage).
func trayCounted(w provider.QuotaWindow) bool {
	return !w.Text && !w.Unlimited && w.Balance == nil && !(w.Display != "" && w.Used == 0 && w.Limit == 0)
}

// trayShared are a card's windows over all its models that routing sets
// aside, for a card with no other: a group of models' shared allowance
// (Antigravity's "Gemini", "Claude & GPT" pools, named with their pool as
// the Usage page names them), or a plan's windows set aside while bought
// credits carry the account past them (Ollama's).
func trayShared(all []provider.QuotaWindow) []provider.QuotaWindow {
	var ws []provider.QuotaWindow
	for _, w := range all {
		if w.Model != "" || !trayCounted(w) {
			continue
		}
		if w.Pool != "" {
			w.Name = w.Pool + " · " + w.Name
		}
		ws = append(ws, w)
	}
	return ws
}

// trayModels are a card's per-model windows, for a card that has nothing
// else (Gemini CLI's, Antigravity's when its pools couldn't be read): the
// tightest of each model family, as the Usage page shows one figure a
// family, the most used first.
func trayModels(all []provider.QuotaWindow) []provider.QuotaWindow {
	var ws []provider.QuotaWindow
	for _, w := range all {
		if w.Model == "" || w.Aside || !trayCounted(w) {
			continue
		}
		if i := slices.IndexFunc(ws, func(x provider.QuotaWindow) bool { return w.Family != "" && x.Family == w.Family }); i >= 0 {
			if w.Used > ws[i].Used {
				ws[i] = w
			}
			continue
		}
		ws = append(ws, w)
	}
	for i := range ws {
		if f := ws[i].Family; f != "" && !strings.HasPrefix(strings.ToLower(ws[i].Name), strings.ToLower(f)) {
			ws[i].Name = f + " · " + ws[i].Name
		}
	}
	slices.SortStableFunc(ws, func(a, b provider.QuotaWindow) int { return cmp.Compare(b.Used, a.Used) })
	return ws
}

// trayWindowKind names a window by how long it runs, as
// settings.TrayWindows keeps a choice of them: "5h", "1d", "7d", "month",
// which stay when the vendor renames or reorders its windows; where the
// span isn't known, the vendor's name for it after "name:".
func trayWindowKind(w provider.QuotaWindow) string {
	d := w.Span
	if d >= 28*24*time.Hour && d <= 31*24*time.Hour {
		return "month"
	}
	if h := int64(d.Round(time.Hour) / time.Hour); h >= 24 && h%24 == 0 {
		return fmt.Sprintf("%dd", h/24)
	} else if h >= 1 {
		return fmt.Sprintf("%dh", h)
	}
	if d > 0 {
		return fmt.Sprintf("%dm", max(1, int64(d.Round(time.Minute)/time.Minute)))
	}
	return "name:" + strings.ToLower(strings.TrimSpace(w.Name))
}

// trayChoice is a window Settings can pick for a card's rows alone, by
// its kind, and whether the card shows it only when picked (extra).
type trayChoice struct {
	w     provider.QuotaWindow
	kind  string
	extra bool
}

// trayChoices are every window of a card that the menu bar can say as a
// percent, for Settings to pick among (settings.TrayWindows): those it
// shows unpicked (trayWindows) first, by how long they run, then the rest
// the Usage card has (trayCounted), which it shows only when picked: money
// aside, one model's window, Cursor's Grok Bot (CrazyJal on X, which the
// picker never offered for being aside). Every choice has a kind of its
// own. One shown unpicked keeps its span's kind ("7d") where it is the
// only one of that span; another of that span, and every extra one, is
// named by its model or name as well ("7d:grok bot"), so a vendor adding a
// window never moves a pick onto another.
func trayChoices(q provider.SubscriptionQuota) []trayChoice {
	base := trayWindows(q)
	var cs []trayChoice
	n := map[string]int{}
	for _, w := range base {
		n[trayWindowKind(w)]++
	}
	for _, w := range base {
		k := trayWindowKind(w)
		if n[k] > 1 {
			k = trayNamedKind(k, w)
		}
		cs = append(cs, trayChoice{w: w, kind: k})
	}
	for _, w := range q.Windows {
		if !trayCounted(w) || slices.ContainsFunc(base, func(x provider.QuotaWindow) bool { return sameTrayWindow(x, w) }) {
			continue
		}
		cs = append(cs, trayChoice{w: w, kind: trayNamedKind(trayWindowKind(w), w), extra: true})
	}
	// a kind twice still (two windows of one name): the later is left out
	// rather than picked along with the first
	seen := map[string]bool{}
	return slices.DeleteFunc(cs, func(c trayChoice) bool {
		dup := seen[c.kind]
		seen[c.kind] = true
		return dup
	})
}

// trayNamedKind is kind with w's model, or else its pool and name, after
// it: "7d:grok bot", "7d:opus"; a kind already of the name alone stays so.
func trayNamedKind(kind string, w provider.QuotaWindow) string {
	if strings.HasPrefix(kind, "name:") {
		return kind
	}
	who := w.Model
	if who == "" {
		who = strings.TrimSpace(w.Name)
		if w.Pool != "" && !strings.HasPrefix(who, w.Pool+" · ") { // trayShared named it so already
			who = w.Pool + " · " + who
		}
	}
	return kind + ":" + strings.ToLower(strings.TrimSpace(who))
}

// sameTrayWindow is whether a and b are one window, though trayWindows
// may have renamed it (with its pool, its family).
func sameTrayWindow(a, b provider.QuotaWindow) bool {
	return a.Model == b.Model && a.Pool == b.Pool && a.Span == b.Span && a.Used == b.Used && a.Aside == b.Aside && a.Family == b.Family
}

// trayShownKinds are the kinds among cs a card's rows show, as chosen in
// Settings (settings.TrayWindows): nil, the windows shown unpicked, when
// none was chosen or none chosen is among cs now, so a card is never left
// empty.
func trayShownKinds(cs []trayChoice, kinds []string) []string {
	for _, c := range cs {
		if slices.Contains(kinds, c.kind) {
			return kinds
		}
	}
	return nil
}

// withTrayKinds is qs with each window the menu bar can show named by its
// kind (TrayKind), for Settings to offer them; qs and its windows, which
// the caches share, are left as they were.
func withTrayKinds(qs []provider.SubscriptionQuota) []provider.SubscriptionQuota {
	out := make([]provider.SubscriptionQuota, len(qs))
	for i, q := range qs {
		if len(q.Windows) > 0 {
			ws := slices.Clone(q.Windows)
			for _, c := range trayChoices(q) {
				// the window as the menu bar has it, named with its pool
				if j := slices.IndexFunc(ws, func(x provider.QuotaWindow) bool {
					return x.TrayKind == "" && sameTrayWindow(x, c.w)
				}); j >= 0 {
					ws[j].TrayKind, ws[j].TrayExtra = c.kind, c.extra
				}
			}
			q.Windows = ws
		}
		out[i] = q
	}
	return out
}

// trayUsageText is trayCardText with every window shown, in English.
func trayUsageText(q provider.SubscriptionQuota, now time.Time, left, pace bool) (label, tip string) {
	return trayCardText("en", q, now, left, pace, nil)
}

// trayCardText is the menu bar's text for a card and the tooltip that
// spells it out: each window's use, or what is left of it (left), and when
// it starts again. With pace (settings.TrayPace) each window's Pace number
// follows its figure, in both, as the Usage page's cards show it. The text
// has the windows of the kinds given alone (trayShownKinds); the tooltip
// has them all. The tooltip's own words are in lang (trayLang), as the
// tray menu's are, and so are a window's name and unit where the page has
// a word for them, as its card says them (pageWord); an error is the
// card's.
func trayCardText(lang string, q provider.SubscriptionQuota, now time.Time, left, pace bool, kinds []string) (label, tip string) {
	if strings.HasPrefix(q.Provider, trayTotal) {
		return q.Balance, q.Name // trayTotalCard's: its rows, and its tooltip
	}
	q.Name = trayName(q)
	if q.Error != "" {
		return "", q.Name + ": " + q.Error
	}
	ws := trayWindows(q)
	if len(ws) == 0 {
		if q.Balance == "" {
			return "", ""
		}
		return q.Balance, q.Name + " · " + q.Balance + trayAsOf(lang, q)
	}
	readAt := now
	if q.ReadAt != nil && !q.ReadAt.After(now) {
		readAt = *q.ReadAt
	}
	cs := trayChoices(q)
	kinds = trayShownKinds(cs, kinds)
	var short, long []string
	for _, c := range cs {
		w, picked := c.w, kinds != nil && slices.Contains(kinds, c.kind)
		if c.extra && !picked {
			continue // shown nowhere unless picked
		}
		used := math.Max(0, math.Min(100, w.Used))
		n := int(math.Round(used))
		word := "%s used"
		if left {
			used = 100 - used
			n, word = 100-n, "%s left"
		}
		pct := fmt.Sprintf("%d%%", n)
		row, paceNote := pct, ""
		if p, ok := w.Pace(readAt, now); pace && ok {
			row = pct + " " + trayPace(p, left)
			paceNote = " · " + trayText(lang, "pace %s", trayPace(p, left))
		}
		if kinds == nil || picked {
			short = append(short, row)
		}
		tipPct := pct
		if used != math.Trunc(used) {
			tipPct = fmt.Sprintf("%.1f%%", used)
		}
		// the window's name and its count's unit as its card says them
		// (t(w.name), t(w.unit) in app.js)
		name := trayWindowName(lang, w.Name)
		w.Unit = pageWord(lang, w.Unit)
		line := name + " " + trayText(lang, word, tipPct)
		if c := w.Count(left); c != "" {
			line = name + " " + c + " · " + trayText(lang, word, tipPct)
		}
		line += paceNote
		if at := resetAt(w, now); !at.IsZero() && at.After(now) {
			line += " · " + trayText(lang, "resets in %s", until(lang, at.Sub(now)))
		}
		long = append(long, line)
	}
	if len(short) > 2 {
		short = short[:2]
	}
	return strings.Join(short, " · "), q.Name + "\n" + strings.Join(long, "\n") + trayAsOf(lang, q)
}

// trayPace is a window's Pace number as the Usage page writes it
// (paceNumber in app.js): "+12%", "−8%" with a true minus, "±0%". It
// follows the figure beside it, so where what is left is shown (left) its
// sign is turned: +12% left is twelve points more to spare than an even
// pace would have.
func trayPace(p float64, left bool) string {
	if left {
		p = -p
	}
	switch n := int(math.Round(p)); {
	case n > 0:
		return fmt.Sprintf("+%d%%", n)
	case n < 0:
		return fmt.Sprintf("−%d%%", -n)
	default:
		return "±0%"
	}
}

// trayAsOf is the tooltip's word that a card stands in for one that
// couldn't be read just now, and when it was read (the Usage page's
// "As of …"); nothing for a reading just made.
func trayAsOf(lang string, q provider.SubscriptionQuota) string {
	if q.AsOf == nil {
		return ""
	}
	return "\n" + trayText(lang, "as of %s, couldn't be read just now", q.AsOf.Local().Format(trayText(lang, "Jan 2 15:04")))
}

func resetAt(w provider.QuotaWindow, now time.Time) time.Time {
	if w.ResetsAt != nil {
		return *w.ResetsAt
	}
	if w.ResetSecs > 0 {
		return now.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	return time.Time{}
}

// until says a wait in the largest two units, in lang: 3d 4h, 2h 10m, 7m.
func until(lang string, d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return trayText(lang, "%dd %dh", days, hours)
	case hours > 0:
		return trayText(lang, "%dh %dm", hours, mins)
	}
	return trayText(lang, "%dm", max(mins, 1))
}

// trayUsageCards are the cards settings.TrayUsages names, in its order,
// among the Usage page's (cached as there), and magpie's own totals it
// names (trayTotal); one gone is left out, and so is one the Usage page's
// Overview hides, as are the rows it hides (trayShown). The vendors aren't
// asked when only totals are named.
func trayUsageCards(ctx context.Context, lang string, ids []string) []provider.SubscriptionQuota {
	if len(ids) == 0 {
		return nil
	}
	var cards []provider.SubscriptionQuota
	if slices.ContainsFunc(ids, func(id string) bool { return !strings.HasPrefix(id, trayTotal) }) {
		cards = slices.Clip(provider.Quotas(ctx)) // appended to below: never into the cache's array
	}
	var currency string
	var rate float64
	var noCost bool
	for _, id := range ids {
		p, ok := trayTotalPeriod(id)
		if !ok {
			continue
		}
		if currency == "" {
			currency, rate = trayCurrency()
			noCost = settings.Shared().TrayNoCost
		}
		cards = append(cards, trayTotalCard(lang, id, trayMoney(trayTotalSum(p), noCost), currency, rate))
	}
	return trayShown(cards, ids, time.Now(), settings.Shared())
}

// trayShown is trayPick less what the Usage page's Overview hides
// (settings.UsageCardsHidden, UsageWindowsHidden): a card hidden there
// showed on in the menu bar, its tooltip and Windows' tray figure, though
// the Overview and the tray panel left it out (onkashiko on X). The cells
// ticked are kept, so a card shown again comes back to its cell. A named
// cell takes the account's card the Overview still shows (its balance,
// say, with its plan hidden); a cell following the account in use picks
// that account among them all, as before, and is left empty when its card
// is hidden, rather than moving to another account.
func trayShown(cards []provider.SubscriptionQuota, ids []string, now time.Time, s settings.Settings) []provider.SubscriptionQuota {
	if len(s.UsageCardsHidden)+len(s.UsageWindowsHidden) == 0 {
		return trayPick(cards, ids, now)
	}
	shown := provider.ShownQuotas(cards, s)
	var out []provider.SubscriptionQuota
	for _, id := range ids {
		if !strings.HasSuffix(id, trayInUse) {
			out = append(out, trayPick(shown, []string{id}, now)...)
			continue
		}
		for _, q := range trayPick(cards, []string{id}, now) {
			if !provider.QuotaCardHidden(q, s) {
				out = append(out, provider.QuotaRowsShown(q, s))
			}
		}
	}
	return out
}

// trayTotal starts the id of a card of magpie's own count instead of a
// subscription's (#1984): "usage:today", "usage:7d", the tokens every
// agent's requests came to over the Usage page's period, over what they
// cost. A provider's id has no ":", so no subscription's card is taken for
// one.
const trayTotal = "usage:"

// trayTotalPeriod is the period a total's id names: today or the last 7
// days, the Usage page's first two, the ones worth watching from the menu
// bar (the page has no 3-day window to agree with).
func trayTotalPeriod(id string) (usage.Period, bool) {
	p, ok := strings.CutPrefix(id, trayTotal)
	if !ok || usage.Period(p) != usage.Today && usage.Period(p) != usage.Week {
		return "", false
	}
	return usage.Period(p), true
}

// trayTotalSum is a period's sum as the Usage page's Requests tab and the
// panel's Usage tab have it, the pages a click on it opens: the gateway's
// requests and the calls the agents' session files record beside them,
// from the ledger's cached index; a var for tests. The Overview's summary,
// summed before, left those calls out (NextCommit on Discord).
var trayTotalSum = func(p usage.Period) usage.Totals { return trayTotals.sum(p, time.Now()) }

// trayTotalFresh is how long a total is shown again before it is summed
// anew. The menu bar is read again on every allowance that lands and every
// account that answers, seconds apart on a busy day, and each sum read
// every session file's state (readRequestState) and built a page of the
// Requests tab: 7s of CPU in 30s on the reporter's computer (#4366).
const trayTotalFresh = time.Minute

var trayTotals trayTotalMemo

// trayTotalMemo is each period's last sum, the day it was for and when.
type trayTotalMemo struct {
	sync.Mutex
	got map[usage.Period]trayTotalGot
}

type trayTotalGot struct {
	day string
	at  time.Time
	sum usage.Totals
}

// sum is p's total as the Requests tab has it, summed again once it is a
// minute old or of another day.
func (m *trayTotalMemo) sum(p usage.Period, now time.Time) usage.Totals {
	day := now.Format(time.DateOnly)
	m.Lock()
	g, ok := m.got[p]
	m.Unlock()
	if ok && g.day == day && now.Sub(g.at) >= 0 && now.Sub(g.at) < trayTotalFresh {
		return g.sum
	}
	sum := trayQuerySum(p)
	m.Lock()
	if m.got == nil {
		m.got = map[usage.Period]trayTotalGot{}
	}
	m.got[p] = trayTotalGot{day: day, at: now, sum: sum}
	m.Unlock()
	return sum
}

// trayQuerySum is what a sum reads; a var for tests.
var trayQuerySum = func(p usage.Period) usage.Totals { return usage.QueryPage(p, usage.Filter{}, 0, 1).Sum }

// trayTotalCard is a total as a card: its rows are every token, the
// cache's too, as the Requests tab's and the panel's Tokens count them,
// marked Σ so it doesn't read as the input alone (NextCommit on Discord),
// over the cost, in the currency Settings says, "+" when some calls had no
// known price; nothing beneath the tokens when none had one. Its name is
// the tooltip, which spells out the input, output and cache, in lang.
func trayTotalCard(lang, id string, t usage.Totals, currency string, rate float64) provider.SubscriptionQuota {
	when := "Today in magpie"
	if p, _ := trayTotalPeriod(id); p == usage.Week {
		when = "Last 7 days in magpie"
	}
	rows := []string{"Σ" + trayCount(lang, t.Tokens()+t.CacheRead+t.CacheWrite)}
	tip := trayText(lang, when) + "\n" + trayTokens(lang, t)
	if t.Cost > 0 {
		c := usage.FormatCost(t.Cost, currency, rate)
		if t.Unpriced > 0 {
			c += "+"
		}
		rows = append(rows, c)
		tip += "\n≈" + c
	}
	tip += "\n" + trayText(lang, "%d requests", t.Calls)
	if t.Unpriced > 0 {
		tip += trayText(lang, ", %d with no known price", t.Unpriced)
	}
	return provider.SubscriptionQuota{Provider: id, Name: tip, Balance: strings.Join(rows, " · ")}
}

// trayMoney is a total as the menu bar says it: with its cost, or with no
// cost and no count of calls with no known price when Settings › Menu bar
// cost is off (settings.TrayNoCost, BussanQ on Discord), the tokens alone.
func trayMoney(t usage.Totals, noCost bool) usage.Totals {
	if noCost {
		t.Cost, t.Unpriced = 0, 0
	}
	return t
}

// trayCurrency is the currency the menu bar and its tooltip show magpie's
// totals' cost in (Settings' Menu bar currency, else the totals'), and the
// yuan's rate when that is CNY.
func trayCurrency() (currency string, rate float64) {
	return currencyRate(settings.Load().TrayCostIn())
}

// totalsCurrency is the currency Settings shows a sum of costs in (a
// total's, of every provider), and the yuan's rate when that is CNY.
func totalsCurrency() (currency string, rate float64) {
	return currencyRate(settings.Load().TotalCostIn())
}

func currencyRate(currency string) (string, float64) {
	var rate float64
	if currency == "cny" {
		rate = fx.Soon().CNYPerUSD // never waited for, as the Settings page's
	}
	return currency, rate
}

// trayTokens is a total's every token, the cache's too, with what they
// were, in lang: "6.3M tokens (912K in · 318K out · 5.1M cached)".
func trayTokens(lang string, t usage.Totals) string {
	parts := []string{trayText(lang, "%s in", trayCount(lang, t.Input)), trayText(lang, "%s out", trayCount(lang, t.Output))}
	if cached := t.CacheRead + t.CacheWrite; cached > 0 {
		parts = append(parts, trayText(lang, "%s cached", trayCount(lang, cached)))
	}
	return trayText(lang, "%s tokens (%s)", trayCount(lang, t.Tokens()+t.CacheRead+t.CacheWrite), strings.Join(parts, " · "))
}

// trayIdleTip is the tooltip while the menu bar shows no cards: magpie's
// name over what went through it today, as the panel's Usage tab counts it
// (ylorn on Discord: a Linux bar's tooltip said only "magpie"): every
// token, split into input, output and cache as the usage:today card's
// tooltip does (NextCommit on Discord), then the cost and the requests.
// It is in lang, magpie's language as the tray menu's is (yi_time on
// Discord: in English on a Windows magpie set to Chinese).
func trayIdleTip(lang string, t usage.Totals, currency string, rate float64) string {
	if t.Calls == 0 {
		return "magpie\n" + trayText(lang, "Today: no requests yet")
	}
	line := trayText(lang, "Today: %s", trayTokens(lang, t)) + "\n"
	if t.Cost > 0 {
		c := usage.FormatCost(t.Cost, currency, rate)
		if t.Unpriced > 0 {
			c += "+"
		}
		line += "≈" + c + " · "
	}
	n := "%d requests"
	if t.Calls == 1 {
		n = "%d request"
	}
	return "magpie\n" + line + trayText(lang, n, t.Calls)
}

// trayChineseUnits is Settings' 万/亿 choice, read when a count is said.
var trayChineseUnits = func() bool { return settings.Shared().ChineseUnits }

// trayCount is a count as the Usage page says it (fmtN in app.js): 133M,
// 1.2M, 456K, 8.2K, 950; with Settings' 万/亿 units and magpie in Chinese,
// 1.33 亿, 68.1 万 (億 and 萬 in Traditional).
func trayCount(lang string, n int) string {
	f := float64(n)
	if (lang == "zh" || lang == "zh-TW") && trayChineseUnits() {
		hundredM, tenK := " 亿", " 万"
		if lang == "zh-TW" {
			hundredM, tenK = " 億", " 萬"
		}
		switch {
		case n >= 1e8:
			return strconv.FormatFloat(math.Round(f/1e8*100)/100, 'f', -1, 64) + hundredM
		case n >= 1e4:
			return strconv.FormatFloat(math.Round(f/1e4*10)/10, 'f', -1, 64) + tenK
		}
		return fmt.Sprint(n)
	}
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case n >= 1e7:
		return fmt.Sprintf("%.0fM", math.Round(f/1e6))
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case n >= 1e5:
		return fmt.Sprintf("%.0fK", math.Round(f/1e3))
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", f/1e3)
	}
	return fmt.Sprint(n)
}

func trayPick(cards []provider.SubscriptionQuota, ids []string, now time.Time) []provider.SubscriptionQuota {
	var out []provider.SubscriptionQuota
	for _, id := range ids {
		if pid, ok := strings.CutSuffix(id, trayInUse); ok {
			if q, ok := trayInUseCard(cards, pid, now); ok {
				out = append(out, q)
			}
			continue
		}
		if i := slices.IndexFunc(cards, func(q provider.SubscriptionQuota) bool { return trayCardID(q) == id }); i >= 0 {
			out = append(out, cards[i])
		}
	}
	return out
}

// trayInUse ends a card id that follows a subscription's account in use
// ("claude|*") rather than naming one: with several accounts, the one the
// gateway is sending to is the one worth watching, whichever it is now.
const trayInUse = "|*"

// trayInUseAccount is the account of an agent's the gateway goes to first;
// a var for tests.
var trayInUseAccount = provider.InUseLogin

// trayServedFresh is how lately an account must have answered through the
// gateway for the menu bar to take it as the one in use; a var for tests.
var trayServedFresh = time.Hour

// trayInUseCard is the card of the provider's account in use, named with
// the account so the tooltip says which it is: the one that answered a
// request through the gateway last, within trayServedFresh — the gateway
// moves on from a spent account, and under Smart picks any, as soon as it
// asks, which the account the agent is signed in to can lag (okingkee on
// X: A used up, B answering, the menu bar still on A) — else the one
// InUseLogin reckons the gateway goes to first; else the one that answered
// last at all (a plugin's accounts, which InUseLogin doesn't know); the
// provider's first card when magpie can't tell (the account in use first
// among them).
func trayInUseCard(cards []provider.SubscriptionQuota, pid string, now time.Time) (provider.SubscriptionQuota, bool) {
	var mine []provider.SubscriptionQuota
	for _, q := range cards {
		if q.Provider == pid {
			mine = append(mine, q)
		}
	}
	if len(mine) == 0 {
		return provider.SubscriptionQuota{}, false
	}
	q := mine[0]
	if len(mine) > 1 {
		last := -1
		for i, c := range mine {
			if c.LastServedAt != nil && (last < 0 || c.LastServedAt.After(*mine[last].LastServedAt)) {
				last = i
			}
		}
		in := -1
		if user := trayInUseAccount(pid); user != "" {
			in = slices.IndexFunc(mine, func(q provider.SubscriptionQuota) bool { return strings.EqualFold(q.User, user) })
		}
		switch {
		case last >= 0 && now.Sub(*mine[last].LastServedAt) < trayServedFresh:
			q = mine[last]
		case in >= 0:
			q = mine[in]
		case last >= 0:
			// a plugin's accounts are no sign-in InUseLogin knows (#1516):
			// the one that answered last, however long ago, over the first
			q = mine[last]
		}
	}
	if q.User != "" && q.Alias == "" {
		q.Name += " · " + q.User // a named account's name is trayName's
	}
	return q, true
}

// trayName is how the tooltip names a card: by the name the user gave its
// account, or the key plan it stands in for, after the card's (#1515).
func trayName(q provider.SubscriptionQuota) string {
	if q.Alias != "" {
		return q.Name + " · " + q.Alias
	}
	return q.Name
}

// trayCell is a card as the Mac's menu bar draws it: its logo, and its
// label's parts stacked, the shortest window over the longer ("42%" over
// "18%"), or its balance alone.
type trayCell struct {
	ID     string // stable provider/account identity, captured with the drawn cell
	Icon   []byte // the logo as the Usage page has it (SVG or PNG); nil for none
	Mono   bool   // a black glyph, drawn in the menu bar's text colour
	Letter string // drawn in its place when there is no logo, or it can't be read
	Plain  bool   // no logo nor letter, as Settings says: the rows alone, a line before
	Rows   []string
}

// trayPlain are the cells without their logos (settings.TrayNoLogos):
// each its rows alone, told from the one before it by a thin line.
func trayPlain(cells []trayCell) []trayCell {
	out := make([]trayCell, len(cells))
	for i, c := range cells {
		out[i] = trayCell{ID: c.ID, Plain: true, Rows: c.Rows}
	}
	return out
}

// trayUsageView is trayCardsView with every window shown, in English.
func trayUsageView(cards []provider.SubscriptionQuota, now time.Time, left, pace bool) (cells []trayCell, label, tip string) {
	return trayCardsView("en", cards, now, left, pace, nil)
}

// trayCardsView is what the tray shows for the cards: a cell for each
// that has a label, with its stable identity for clicks; labels joined by
// " | " for a tray that shows text; and a tooltip spelling each out. A
// card's rows are the windows its provider's kinds in pick name
// (settings.TrayWindows), all when it names none.
func trayCardsView(lang string, cards []provider.SubscriptionQuota, now time.Time, left, pace bool, pick map[string][]string) (cells []trayCell, label, tip string) {
	var labels, tips []string
	for _, q := range cards {
		l, t := trayCardText(lang, q, now, left, pace, pick[q.Provider])
		if t != "" {
			tips = append(tips, t)
		}
		if l == "" {
			continue
		}
		labels = append(labels, l)
		c := trayCell{ID: trayCardID(q), Rows: strings.Split(l, " · ")}
		for i, r := range c.Rows {
			c.Rows[i] = trayRow(r)
		}
		if strings.HasPrefix(q.Provider, trayTotal) {
			// magpie's own: the bird beside it says whose, no logo of a vendor's
			c.Plain = true
			cells = append(cells, c)
			continue
		}
		c.Icon, c.Mono = trayIconFile(q.Icon)
		if r := []rune(strings.TrimSpace(q.Name)); len(r) > 0 {
			c.Letter = strings.ToUpper(string(r[0]))
		}
		cells = append(cells, c)
	}
	return cells, strings.Join(labels, " | "), strings.Join(tips, "\n\n")
}

// sniTip is the tooltip as a Linux tray item's ToolTip title carries it.
// Omarchy's bar shows the title as plain text and waybar as Pango markup,
// where a bare "&" or "<" loses the whole tooltip; their look-alikes read
// the same in both.
func sniTip(tip string) string {
	return strings.NewReplacer("&", "＆", "<", "‹", ">", "›").Replace(tip)
}

// trayRow is a cell's row cut to fit the menu bar: a balance is a few
// figures ("¥12345.67"), but one read from a plan's own page can be any
// text, and the whole of it is in the tooltip.
func trayRow(r string) string {
	if rs := []rune(r); len(rs) > trayRowMax {
		return strings.TrimSpace(string(rs[:trayRowMax-1])) + "…"
	}
	return r
}

const trayRowMax = 10

// trayIconFile is a card's logo among the page's icons, as its icon() has
// it: a "-color" one and the PNGs in their own colours, the others a black
// glyph (a mask there); a stored picture ("file:…", one a plugin or the
// user gave a provider) read as pageIconOf reads it, through its checked
// name only; nil when there is no such file.
func trayIconFile(name string) (b []byte, mono bool) {
	if own, ok := strings.CutPrefix(name, "file:"); ok {
		f, _ := provider.IconServed(own)
		if f == "" {
			return nil, false
		}
		b, err := os.ReadFile(f)
		if err != nil || len(b) > maxPageIcon {
			return nil, false
		}
		if strings.HasSuffix(own, ".svg") {
			b = svgArcFlags(b)
		}
		return b, false
	}
	if name == "" || strings.ContainsAny(name, `/\:`) {
		return nil, false
	}
	if b, err := assets.ReadFile("assets/icons/" + name + ".png"); err == nil {
		return b, false
	}
	b, err := assets.ReadFile("assets/icons/" + name + ".svg")
	if err != nil {
		return nil, false
	}
	return svgArcFlags(b), !strings.HasSuffix(name, "-color")
}

var svgPathData = lazyre.New(`\sd="([^"]*[aA][^"]*)"`)

// svgArcFlags spaces out the flags of a path's arcs ("a4.5 4.5 0 004.5 0",
// the flags run into the number after them), which browsers read and the
// Mac's own SVG drawing doesn't: it draws a line for such an arc, and
// Codex's logo comes out a blot.
func svgArcFlags(svg []byte) []byte {
	return svgPathData.ReplaceAllFunc(svg, func(m []byte) []byte {
		d := string(svgPathData.FindSubmatch(m)[1])
		var out []string
		cmd, arg := byte(0), 0
		for i := 0; i < len(d); {
			c := d[i]
			switch {
			case c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r':
				i++
			case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
				if c == 'e' || c == 'E' { // not a command: a number's exponent, never alone
					return m
				}
				cmd, arg = c, 0
				out = append(out, string(c))
				i++
			case (cmd == 'a' || cmd == 'A') && (arg%7 == 3 || arg%7 == 4):
				if c != '0' && c != '1' {
					return m
				}
				out = append(out, string(c))
				arg++
				i++
			default:
				j := svgNumber(d, i)
				if j == i {
					return m
				}
				out = append(out, d[i:j])
				arg++
				i = j
			}
		}
		return []byte(` d="` + strings.Join(out, " ") + `"`)
	})
}

// svgNumber is where the number at d[i:] ends: a sign, digits with at most
// one point, an exponent.
func svgNumber(d string, i int) int {
	j := i
	if j < len(d) && (d[j] == '-' || d[j] == '+') {
		j++
	}
	digits, point := 0, false
	for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !point) {
		if d[j] == '.' {
			point = true
		} else {
			digits++
		}
		j++
	}
	if digits == 0 {
		return i
	}
	if j < len(d) && (d[j] == 'e' || d[j] == 'E') {
		k := j + 1
		if k < len(d) && (d[k] == '-' || d[k] == '+') {
			k++
		}
		if k < len(d) && d[k] >= '0' && d[k] <= '9' {
			for j = k; j < len(d) && d[j] >= '0' && d[j] <= '9'; j++ {
			}
		}
	}
	return j
}

// onTrayUsage brings the tray's text up to date at once, when the Settings
// page changes which card it shows; set by the process that has the tray.
var onTrayUsage func()

// onTrayCellClick receives the stable card ID from the native image.
// Set by the tray process; it must schedule window work without blocking.
var onTrayCellClick func(id string)

// panelQuotaJS safely quotes the card ID for the panel's focus handler.
func panelQuotaJS(card string) string {
	if p, ok := trayTotalPeriod(card); ok {
		return `panelUseFocus("` + string(p) + `")` // the Usage tab, at the total's period
	}
	id, _ := json.Marshal(card) // quoted and escaped: a JSON string is a JS one
	return "panelQuotaFocus(" + string(id) + ")"
}
