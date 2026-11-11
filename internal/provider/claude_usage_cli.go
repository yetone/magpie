package provider

// A Claude account's allowance, as Claude Code's own /usage tells it:
// magpie runs `claude -p /usage` (a command Claude Code answers itself,
// asking no model) and reads its lines, never asking Anthropic itself.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	// the zone /usage names its resets in: Windows has no zone database,
	// and without one a reset was read in the machine's own zone
	_ "time/tzdata"

	"github.com/yetone/magpie/internal/lazyre"
)

// claudeCLIUsage runs Claude Code's /usage in configDir, or its own
// directory when empty, and is what it printed. The gateway sets it
// (UsageClaudeVia). It is held for reading while it runs, as
// loginUsageFor is: Allowances reads Claude's usage in the background, and
// tests swap it.
var claudeCLIUsage struct {
	sync.RWMutex
	f func(ctx context.Context, configDir string) (string, error)
}

// UsageClaudeVia sets how Claude Code's /usage runs. It returns once no
// run is still going through the one it replaces.
func UsageClaudeVia(f func(ctx context.Context, configDir string) (string, error)) {
	claudeCLIUsage.Lock()
	claudeCLIUsage.f = f
	claudeCLIUsage.Unlock()
}

// runClaudeUsage is what Claude Code's /usage printed, through
// claudeCLIUsage; errClaudeCannotRun when nothing set it.
func runClaudeUsage(ctx context.Context, configDir string) (string, error) {
	claudeCLIUsage.RLock()
	defer claudeCLIUsage.RUnlock()
	if claudeCLIUsage.f == nil {
		return "", errClaudeCannotRun
	}
	return claudeCLIUsage.f(ctx, configDir)
}

// readClaudeUsage is the allowance of the account in configDir, from
// Claude Code's /usage. Empty reads the account it is signed in to.
func readClaudeUsage(ctx context.Context, configDir string) ([]QuotaWindow, error) {
	text, err := runClaudeUsage(ctx, configDir)
	if err != nil {
		return []QuotaWindow{}, err
	}
	return parseClaudeUsage(text, time.Now())
}

// "Current session: 13% used · resets Oct 1 at 3:30pm (Asia/Shanghai)",
// "Current week (all models): 4% used · resets Oct 3 at 2pm (Asia/Shanghai)",
// "Current week (Fable): 0% used"
var claudeUsageLineRE = lazyre.New(`^Current (session|week(?: \(([^)]+)\))?):\s*([0-9.]+)% used(?:\s*·\s*resets (.+))?$`)

// A successful /usage run can tell only how the subscription is billed,
// without an allowance. This says nothing about the account's limits.
var errClaudeUsageUnavailable = errors.New("Claude Code's /usage is temporarily unavailable")

// claudeUsageUnknown is a /usage read that told nothing of the account,
// for a reason that passes: Claude Code's own "temporarily unavailable",
// or a panel that couldn't load its figures (the helper's "/usage
// couldn't load the plan's usage": still "Loading usage data…", or
// "Failed to load usage data" after a retry). The card keeps the
// account's last reading. Windows' pseudo console showed 2.1.316's panel
// while it still loaded, and the card said the account had no allowance
// (Seagull fries).
var claudeUsageUnknown = lazyre.New(`^(?:Claude Code: )?(?:` + regexp.QuoteMeta(errClaudeUsageUnavailable.Error()) + `|/usage couldn't load the plan's usage)`)

var errClaudeCannotRun = errors.New("Claude Code can't be run from here")

// An account error stays visible even when it also mentions a timeout or
// rate limit. It is not a temporary failure to read the usage endpoint.
var claudeUsageDenied = lazyre.New(`(?i)\b(401|403)\b|not (logged|signed) in|signed out|sign-in (has )?expired|unauthorized|forbidden|authentication (failed|required)|invalid (access )?token|(session|usage) limit|hit your limit`)

// claudeDenied says s tells an account error (claudeUsageDenied). Claude
// Code's "(not your usage limit)", which it says of Anthropic turning the
// usage page's requests away for a while ("Claude is temporarily limiting
// requests to the usage page (not your usage limit)"), tells none: read as
// one, a /usage that couldn't load had the account refused.
func claudeDenied(s string) bool {
	return claudeUsageDenied.MatchString(claudeNotYourLimit.ReplaceAllString(s, ""))
}

var claudeNotYourLimit = lazyre.New(`(?i)\(not your usage limit\)`)

// claudeUsageCounts is /usage's closing line of the account's local
// activity ("Last 7d · 403 requests · 388 sessions"), with the " · "
// before it wherever it sits in a line: counts, not the HTTP statuses a
// count of 401 or 403 reads as (SCIYYDy, #4995). Taken out, what is left
// of the line is read: "Last 7d · 403 requests · 388 sessions · HTTP 403
// Forbidden" is an account error. Any span and a count with thousands
// separators ("1,403 requests") are counts too.
var claudeUsageCounts = lazyre.New(`(?: · )?Last [0-9]+[a-z]+ · [0-9][0-9,]* requests? · [0-9][0-9,]* sessions?`)

// claudeOverages is /usage's line for an account past its plan's limits
// and billed for what it uses ("You are currently using your overages to
// power your Claude Code usage. We will automatically switch you back to
// your subscription rate limits when they reset"), which Claude Code
// prints above the account's windows. It is no account error: read as
// one, the reading had no windows and an error, and the switch, which
// stays on an account it can't read, left Claude Code spending overages
// with other accounts that had room (StringKe, #4532).
var claudeOverages = lazyre.New(`(?i)using your overages`)

// claudeExtraLineRE is the line the gateway adds under /usage's text for an
// account with extra usage on ("usage credits"): Claude Code's -p /usage
// prints no figure for it, but the usage_report it carries (verbose json)
// has the month's cap and spend, which Claude Code read from Anthropic, so
// magpie asks Anthropic nothing. "Extra usage: 271.40 / 500.00 USD spent".
// ClaudeExtraLine writes it.
var claudeExtraLineRE = lazyre.New(`^Extra usage: ([0-9.]+) / ([0-9.]+) ([A-Za-z]{3}) spent$`)

// ClaudeExtraLine is the line for spent and limit, in cents of currency.
func ClaudeExtraLine(spent, limit float64, currency string) string {
	if currency == "" {
		currency = "USD"
	}
	return fmt.Sprintf("Extra usage: %.2f / %.2f %s spent", spent/100, limit/100, strings.ToUpper(currency))
}

// claudeExtraWindow reads that line, when text has it: the month's spend of
// the cap set aside (Aside), so a cap spent never stops the account the way
// a plan window used up does: only Anthropic decides whether a request
// spends it.
func claudeExtraWindow(lines []string) (QuotaWindow, bool) {
	for _, line := range lines {
		m := claudeExtraLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		spent, err1 := strconv.ParseFloat(m[1], 64)
		limit, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil || limit <= 0 {
			continue
		}
		return QuotaWindow{Name: "Usage credits", Used: math.Min(100, spent/limit*100), Aside: true,
			Amount: spent, Limit: limit, Unit: strings.ToUpper(m[3])}, true
	}
	return QuotaWindow{}, false
}

// claudeOveragesWindow is the window an account on overages is read with,
// beside ws: spent (usedPast, roomLeft), so the switch moves Claude Code
// off it as off one whose window is used up, and told as its text. It
// ends when the last of ws's used-up windows renews, as Claude Code's
// overages do; none named, at the next reading.
func claudeOveragesWindow(ws []QuotaWindow) QuotaWindow {
	// the GUI says a Text row used 100 "In use" in the reader's language
	w := QuotaWindow{Name: "Extra usage", Used: 100, Display: "In use", Text: true}
	for _, x := range ws {
		if x.Used >= 100 && x.ResetsAt != nil && (w.ResetsAt == nil || x.ResetsAt.After(*w.ResetsAt)) {
			w.ResetsAt = x.ResetsAt
		}
	}
	return w
}

// parseClaudeUsage reads /usage's windows; now dates a reset that names no
// year.
func parseClaudeUsage(text string, now time.Time) ([]QuotaWindow, error) {
	out := []QuotaWindow{}
	text = strings.TrimSpace(ansi.ReplaceAllString(text, ""))
	lines := strings.Split(text, "\n")
	// Account errors take precedence over notices (and partial readings).
	for _, line := range lines {
		// the counts are no HTTP status, wherever the line has them: the
		// interactive panel's words come joined on one line
		// (claudetui.usagePart)
		if claudeDenied(claudeUsageCounts.ReplaceAllString(line, "")) {
			return out, errors.New("Claude Code's /usage told no allowance: " + clipLine(strings.TrimSpace(line)))
		}
	}
	const week = 7 * 24 * time.Hour
	for _, line := range lines {
		m := claudeUsageLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		used, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		w := QuotaWindow{Name: "5 hours", Used: used, Span: 5 * time.Hour}
		if m[1] != "session" {
			w.Name, w.Span = "7 days", week
			// "Sonnet only" (Claude Code 2.1.316's week of Sonnet) is the
			// Sonnet week: "7 days · Sonnet", counting sonnet, the window
			// its seven_day_sonnet header keeps (claudeKinds). Read whole,
			// it counted models named "sonnet-only", which none is, so
			// routing never saw the Sonnet week used up (StringKe, #4532)
			scope := strings.TrimSpace(m[2])
			if s, ok := strings.CutSuffix(strings.ToLower(scope), " only"); ok && s != "" {
				scope = strings.TrimSpace(scope[:len(s)])
			}
			if scope != "" && !strings.EqualFold(scope, "all models") {
				w.Name, w.Model = "7 days · "+scope, claudeScopeModel(scope)
			}
		}
		if slices.ContainsFunc(out, func(x QuotaWindow) bool { return x.Name == w.Name }) {
			continue
		}
		if t, ok := claudeResetTime(m[4], now, w.Span); ok {
			w.ResetsAt = &t
		}
		out = append(out, w)
	}
	if slices.ContainsFunc(lines, claudeOverages.MatchString) {
		out = append(out, claudeOveragesWindow(out))
	}
	if len(out) == 0 {
		if text == claudeNoWindow {
			// the interactive panel loaded with none of the account's
			// windows: none has started (claudeWithUnstarted)
			return out, nil
		}
		if text == "" || slices.ContainsFunc(lines, func(line string) bool {
			return strings.TrimSuffix(strings.TrimSpace(line), ".") == "You are currently using your subscription to power your Claude Code usage"
		}) {
			return out, errClaudeUsageUnavailable
		}
		// a panel with no figure says nothing of the account's allowance:
		// unknown, its words told whole, not the first line clipped, which
		// left out why (koohoko5924); its counts, told in the error, would
		// read as a 401 or 403 where the error is matched again
		// (claudeUsageDenied)
		var said []string
		for _, line := range lines {
			line = strings.TrimSpace(claudeUsageCounts.ReplaceAllString(line, ""))
			if line != "" {
				said = append(said, line)
			}
		}
		if len(said) == 0 {
			return out, errors.New("Claude Code's /usage showed no usage figures")
		}
		return out, errors.New("Claude Code's /usage showed no usage figures: " + clipReason(strings.Join(said, " · ")))
	}
	if w, ok := claudeExtraWindow(lines); ok {
		out = append(out, w)
	}
	return out, nil
}

// claudeNoWindow is the helper's word for an interactive /usage panel that
// loaded with no window drawn (claudetui.NoUsageWindow). The gateway reads
// /usage through -p since #5582, which says no such thing.
const claudeNoWindow = "No usage window has started"

func clipLine(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// clipReason is a failure's words, kept long enough to say why.
func clipReason(s string) string {
	if r := []rune(s); len(r) > 600 {
		return string(r[:600]) + "…"
	}
	return s
}

// claudeResetTime reads "Oct 1 at 3:30pm (Asia/Shanghai)", "Oct 9, 2:59pm
// (UTC)" (Claude Code 2.1.285 on) or "3pm (Asia/Shanghai)", in the zone
// named, else local time. It ends a window span long, so it is never further
// ahead than that (and claudeResetSlack): a date with no year is in the first
// year it is less than a day past and no further ahead, and a time alone is
// the next one from now no further ahead. Where none is, as for a reading
// days old or a clock that is off, it is in the latest year (or day) where
// it is already past, and the window reads as renewed (elapsed). Where the
// clocks go back and read that time twice, the reading not yet past is
// taken.
func claudeResetTime(s string, now time.Time, span time.Duration) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	loc := time.Local
	if i := strings.LastIndex(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		if l, err := time.LoadLocation(s[i+1 : len(s)-1]); err == nil {
			loc = l
		}
		s = strings.TrimSpace(s[:i])
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "AM", "am"), "PM", "pm")
	ref := now.In(loc)
	latest := ref.Add(span + claudeResetSlack)
	for _, layout := range []string{
		"Jan 2 at 3:04pm", "Jan 2 at 3pm", "Jan 2, 2006 at 3:04pm", "Jan 2, 2006 at 3pm",
		"Jan 2, 3:04pm", "Jan 2, 3pm", "Jan 2, 2006, 3:04pm", "Jan 2, 2006, 3pm",
	} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		if t.Year() != 0 {
			return t, true
		}
		// Dec 31 read just after New Year is last year's
		var past time.Time
		for y := ref.Year() - 1; y <= ref.Year()+1; y++ {
			d := time.Date(y, t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if r, ok := nextReading(d, ref, ref.Add(-24*time.Hour)); ok && !r.After(latest) {
				return r, true
			}
			if d.Before(ref) {
				past = d
			}
		}
		return past, true
	}
	for _, layout := range []string{"3:04pm", "3pm"} {
		c, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		// a day whose clocks skip that time is passed over: Santiago's skip
		// midnight, so "12:30am" before it is the day after tomorrow's
		for d := 0; d <= 2; d++ {
			t := time.Date(ref.Year(), ref.Month(), ref.Day()+d, c.Hour(), c.Minute(), 0, 0, loc)
			if t.Hour() != c.Hour() || t.Minute() != c.Minute() {
				continue
			}
			if r, ok := nextReading(t, ref, ref); ok {
				if !r.After(latest) {
					return r, true
				}
				break
			}
		}
		for d := 0; d >= -2; d-- {
			t := time.Date(ref.Year(), ref.Month(), ref.Day()+d, c.Hour(), c.Minute(), 0, 0, loc)
			if t.Hour() == c.Hour() && t.Minute() == c.Minute() && t.Before(ref) {
				return t, true
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// claudeResetSlack is how much further ahead than its window a reset may
// read, as this machine's clock may be behind Anthropic's. Claude Code
// prints the reset from the instant Anthropic gives, seconds dropped, so it
// is never later than that.
const claudeResetSlack = 2 * time.Hour

// nextReading is the first instant whose clock reads as t's does that isn't
// before ref, else the first that isn't before lim. As the clocks go back, an
// hour is read twice, and time.Date may give either instant.
func nextReading(t, ref, lim time.Time) (time.Time, bool) {
	first, last := t, t
	start, end := t.ZoneBounds()
	_, off := t.Zone()
	if !start.IsZero() {
		_, prev := start.Add(-time.Second).Zone()
		if e := t.Add(time.Duration(off-prev) * time.Second); prev > off && e.Before(start) {
			first = e
		}
	}
	if !end.IsZero() {
		_, next := end.Zone()
		if l := t.Add(time.Duration(off-next) * time.Second); next < off && !l.Before(end) {
			last = l
		}
	}
	for _, from := range []time.Time{ref, lim} {
		for _, r := range []time.Time{first, last} {
			if !r.Before(from) {
				return r, true
			}
		}
	}
	return time.Time{}, false
}
