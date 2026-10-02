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
		if t, ok := claudeResetTime(m[4], now); ok {
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

// claudeResetTime reads "Oct 1 at 3:30pm (Asia/Shanghai)" or "3pm
// (Asia/Shanghai)", in the zone named, else local time; a date with no
// year is the next one from a day before now.
func claudeResetTime(s string, now time.Time) (time.Time, bool) {
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
	for _, layout := range []string{"Jan 2 at 3:04pm", "Jan 2 at 3pm", "Jan 2, 2006 at 3:04pm", "Jan 2, 2006 at 3pm"} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		if t.Year() == 0 {
			t = t.AddDate(ref.Year(), 0, 0)
			if t.Before(ref.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
		}
		return t, true
	}
	for _, layout := range []string{"3:04pm", "3pm"} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		t = time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if t.Before(ref) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	return time.Time{}, false
}
