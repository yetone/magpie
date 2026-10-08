package provider

// A Claude account's allowance, as Claude Code's own /usage tells it:
// magpie runs `claude -p /usage` (a command Claude Code answers itself,
// asking no model) and reads its lines, never asking Anthropic itself.

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	// the zone /usage names its resets in: Windows has no zone database,
	// and without one a reset was read in the machine's own zone
	_ "time/tzdata"
)

// claudeCLIUsage runs Claude Code's /usage for the account it is signed in
// to and is what it printed. The gateway, which runs Claude Code, sets it
// (UsageClaudeVia).
var claudeCLIUsage func(ctx context.Context) (string, error)

// UsageClaudeVia sets how Claude Code's /usage runs.
func UsageClaudeVia(f func(ctx context.Context) (string, error)) { claudeCLIUsage = f }

// readClaudeUsage is the allowance of the account Claude Code is signed in
// to, from its /usage.
func readClaudeUsage(ctx context.Context) ([]QuotaWindow, error) {
	if claudeCLIUsage == nil {
		return []QuotaWindow{}, errClaudeCannotRun
	}
	text, err := claudeCLIUsage(ctx)
	if err != nil {
		return []QuotaWindow{}, err
	}
	return parseClaudeUsage(text, time.Now())
}

// "Current session: 13% used · resets Oct 1 at 3:30pm (Asia/Shanghai)",
// "Current week (all models): 4% used · resets Oct 3 at 2pm (Asia/Shanghai)",
// "Current week (Fable): 0% used"
var claudeUsageLineRE = regexp.MustCompile(`^Current (session|week(?: \(([^)]+)\))?):\s*([0-9.]+)% used(?:\s*·\s*resets (.+))?$`)

// A successful /usage run can tell only how the subscription is billed,
// without an allowance. This says nothing about the account's limits.
var errClaudeUsageUnavailable = errors.New("Claude Code's /usage is temporarily unavailable")

var errClaudeCannotRun = errors.New("Claude Code can't be run from here")

// An account error stays visible even when it also mentions a timeout or
// rate limit. It is not a temporary failure to read the usage endpoint.
var claudeUsageDenied = regexp.MustCompile(`(?i)\b(401|403)\b|not (logged|signed) in|signed out|sign-in (has )?expired|unauthorized|forbidden|authentication (failed|required)|invalid (access )?token|(session|usage) limit|hit your limit|using your overages`)

// parseClaudeUsage reads /usage's windows; now dates a reset that names no
// year.
func parseClaudeUsage(text string, now time.Time) ([]QuotaWindow, error) {
	out := []QuotaWindow{}
	text = strings.TrimSpace(ansi.ReplaceAllString(text, ""))
	lines := strings.Split(text, "\n")
	// Account errors take precedence over notices (and partial readings).
	for _, line := range lines {
		if claudeUsageDenied.MatchString(line) {
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
			if scope := strings.TrimSpace(m[2]); scope != "" && !strings.EqualFold(scope, "all models") {
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
	if len(out) == 0 {
		if text == "" || slices.ContainsFunc(lines, func(line string) bool {
			return strings.TrimSuffix(strings.TrimSpace(line), ".") == "You are currently using your subscription to power your Claude Code usage"
		}) {
			return out, errClaudeUsageUnavailable
		}
		first, _, _ := strings.Cut(text, "\n")
		return out, errors.New("Claude Code's /usage told no allowance: " + clipLine(first))
	}
	return out, nil
}

func clipLine(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
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
