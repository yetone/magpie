package gui

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings → Usage in the menu bar: the windows of the subscriptions and
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
	return time.Duration(settings.Load().TrayUsageEvery) * time.Minute
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
// use), the shortest first where the vendor says how long they run.
func trayWindows(q provider.SubscriptionQuota) []provider.QuotaWindow {
	var ws []provider.QuotaWindow
	for _, w := range q.Windows {
		if !w.Aside && w.Model == "" {
			ws = append(ws, w)
		}
	}
	slices.SortStableFunc(ws, func(a, b provider.QuotaWindow) int {
		if a.Span == 0 || b.Span == 0 {
			return 0
		}
		return cmp.Compare(a.Span, b.Span)
	})
	return ws
}

// trayUsageText is the menu bar's text for a card and the tooltip that
// spells it out: each window's use, or what is left of it (left), and when
// it starts again.
func trayUsageText(q provider.SubscriptionQuota, now time.Time, left bool) (label, tip string) {
	if q.Error != "" {
		return "", q.Name + ": " + q.Error
	}
	ws := trayWindows(q)
	if len(ws) == 0 {
		if q.Balance == "" {
			return "", ""
		}
		return q.Balance, q.Name + " · " + q.Balance
	}
	var short, long []string
	for _, w := range ws {
		used := math.Max(0, math.Min(100, w.Used))
		n := int(math.Round(used))
		word := "used"
		if left {
			used = 100 - used
			n, word = 100-n, "left"
		}
		pct := fmt.Sprintf("%d%%", n)
		short = append(short, pct)
		tipPct := pct
		if used != math.Trunc(used) {
			tipPct = fmt.Sprintf("%.1f%%", used)
		}
		line := w.Name + " " + tipPct + " " + word
		if w.Display != "" {
			line = w.Name + " " + w.Display + " · " + tipPct + " " + word
		}
		if at := resetAt(w, now); !at.IsZero() && at.After(now) {
			line += " · resets in " + until(at.Sub(now))
		}
		long = append(long, line)
	}
	if len(short) > 2 {
		short = short[:2]
	}
	return strings.Join(short, " · "), q.Name + "\n" + strings.Join(long, "\n")
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

// until says a wait in the largest two units: 3d 4h, 2h 10m, 7m.
func until(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", max(mins, 1))
}

// trayUsageCards are the cards settings.TrayUsages names, in its order,
// among the Usage page's (cached as there); one gone is left out.
func trayUsageCards(ctx context.Context, ids []string) []provider.SubscriptionQuota {
	if len(ids) == 0 {
		return nil
	}
	return trayPick(provider.Quotas(ctx), ids)
}

func trayPick(cards []provider.SubscriptionQuota, ids []string) []provider.SubscriptionQuota {
	var out []provider.SubscriptionQuota
	for _, id := range ids {
		if pid, ok := strings.CutSuffix(id, trayInUse); ok {
			if q, ok := trayInUseCard(cards, pid); ok {
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
// gateway goes to first is the one worth watching, whichever it is now.
const trayInUse = "|*"

// trayInUseAccount is the account of an agent's the gateway goes to first;
// a var for tests.
var trayInUseAccount = provider.InUseLogin

// trayInUseCard is the card of the provider's account in use, named with
// the account so the tooltip says which it is; the provider's first card
// when magpie can't tell (the account in use first among them).
func trayInUseCard(cards []provider.SubscriptionQuota, pid string) (provider.SubscriptionQuota, bool) {
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
		if user := trayInUseAccount(pid); user != "" {
			if i := slices.IndexFunc(mine, func(q provider.SubscriptionQuota) bool { return strings.EqualFold(q.User, user) }); i >= 0 {
				q = mine[i]
			}
		}
	}
	if q.User != "" {
		q.Name += " · " + q.User
	}
	return q, true
}

// trayCell is a card as the Mac's menu bar draws it: its logo, and its
// label's parts stacked, the shortest window over the longer ("42%" over
// "18%"), or its balance alone.
type trayCell struct {
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
		out[i] = trayCell{Plain: true, Rows: c.Rows}
	}
	return out
}

// trayUsageView is what the tray shows for the cards: a cell for each
// that has a label; the labels as one line of text, " | " between cards,
// for a tray that shows text and not the cells; and the tooltip spelling
// each out.
func trayUsageView(cards []provider.SubscriptionQuota, now time.Time, left bool) (cells []trayCell, label, tip string) {
	var labels, tips []string
	for _, q := range cards {
		l, t := trayUsageText(q, now, left)
		if t != "" {
			tips = append(tips, t)
		}
		if l == "" {
			continue
		}
		labels = append(labels, l)
		c := trayCell{Rows: strings.Split(l, " · ")}
		for i, r := range c.Rows {
			c.Rows[i] = trayRow(r)
		}
		c.Icon, c.Mono = trayIconFile(q.Icon)
		if r := []rune(strings.TrimSpace(q.Name)); len(r) > 0 {
			c.Letter = strings.ToUpper(string(r[0]))
		}
		cells = append(cells, c)
	}
	return cells, strings.Join(labels, " | "), strings.Join(tips, "\n\n")
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
// glyph (a mask there); nil when there is no such file (a picture the user
// gave their own provider isn't one).
func trayIconFile(name string) (b []byte, mono bool) {
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

var svgPathData = regexp.MustCompile(`\sd="([^"]*[aA][^"]*)"`)

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
