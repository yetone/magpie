package provider

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func forgetClaudeRefused() {
	claudeRefused.Lock()
	claudeRefused.at, claudeRefused.until, claudeRefused.said, claudeRefused.kept = time.Time{}, time.Time{}, "", false
	claudeRefused.Unlock()
}

// claudeWeeklyRefusal is the line Claude Code 2.1 wrote to the owner's
// session when Anthropic refused the account for its week (11-10, ids
// redacted), at at, its reset at resets.
func claudeWeeklyRefusal(at, resets time.Time) string {
	r := resets.Unix()
	return fmt.Sprintf(`{"parentUuid":"p","isSidechain":false,"userType":"external","cwd":"/work","sessionId":"s","version":"2.1.330","gitBranch":"main","type":"assistant","uuid":"u","timestamp":%q,"message":{"id":"m","container":null,"model":"<synthetic>","role":"assistant","stop_reason":"stop_sequence","stop_sequence":"","type":"message","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"You've hit your weekly limit · resets Nov 12 at 2pm (Asia/Shanghai)"}]},"requestId":"req_r","apiError":"usage_limit_reached","apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":%d,"rateLimitType":"seven_day","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false}},"quotaLimits":{"status":"rejected","resetsAt":%d,"unifiedRateLimitFallbackAvailable":false,"rateLimitType":"seven_day","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`,
		at.UTC().Format(time.RFC3339Nano), r, r)
}

// A refusal Claude Code wrote to its session since it has been on the
// account keeps the account out (claude-limits.json) till the reset
// Anthropic named, not only as the cue for a /usage read: the owner's
// /usage runs were killed at their deadline, and nothing else said the
// account was out once Claude Code was moved off it. One written before
// Claude Code was on it is another account's, and one the gateway made,
// serving Claude Code's requests from its accounts, names none.
func TestClaudeRefusalKeptTillItsReset(t *testing.T) {
	for _, c := range []struct {
		name   string
		on     time.Duration // how long Claude Code has been on the account
		before bool          // written before Claude Code was on the account
		routed bool          // Claude Code's requests served by the gateway
		other  bool          // its reset kept for another account already
		kept   bool
	}{
		{name: "since", on: 2 * time.Minute, kept: true},
		{name: "before", on: 2 * time.Minute, before: true},
		{name: "settling", on: 30 * time.Second},
		{name: "routed", on: 2 * time.Minute, routed: true},
		{name: "another's reset", on: 2 * time.Minute, other: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			claudeOutSoon(t, "50")
			if c.routed {
				old := ClaudeThroughMagpie
				ClaudeThroughMagpie = func() bool { return true }
				t.Cleanup(func() { ClaudeThroughMagpie = old })
				if err := SetSwitchOnly("claude", false); err != nil {
					t.Fatal(err)
				}
			}
			user := claudeActive(t)
			session := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-work", "s1.jsonl")
			writeSession(t, session, `{"type":"user","message":{"role":"user","content":"go"},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
			claudeLookDue() // finds Claude Code on user
			claudeOn.Lock()
			claudeOn.since = claudeOn.since.Add(-c.on)
			claudeOn.Unlock()
			at := time.Now()
			if c.before {
				at = claudeOn.since.Add(-time.Minute)
			}
			resets := time.Now().Add(49 * time.Hour).Truncate(time.Second)
			if c.other {
				keepClaudeOut("max@x.com", resets, "You've hit your weekly limit", time.Now())
			}
			writeSession(t, session, claudeWeeklyRefusal(at, resets))
			claudeLookDue()
			got, ok := readClaudeSaid()[strings.ToLower(user)]
			if ok != c.kept || c.kept && !got.Until.Equal(resets) {
				t.Fatalf("kept %v till %v, want kept %v till %v", ok, got.Until, c.kept, resets)
			}
			if c.kept && !strings.Contains(got.Said, "weekly limit") {
				t.Fatalf("kept as %q, not Claude Code's words", got.Said)
			}
			if c.kept != claudeSaidOut(user, time.Now()) {
				t.Fatalf("out %v, want %v", !c.kept, c.kept)
			}
		})
	}
}

// Until when a refusal says the account is out, from what it holds: the
// reset Anthropic named; else the one its words name; else the window's
// longest from the refusal, five hours for the 5-hour one and a day for
// any other; else an hour. A reset named that is gone already is not it.
func TestClaudeRefusalUntil(t *testing.T) {
	at := time.Date(2026, 11, 10, 7, 19, 36, 0, time.UTC)
	noon := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		q    claudeRefusalQuota
		said string
		want time.Time
	}{
		{"resetsAt", claudeRefusalQuota{ResetsAt: 1794463200, Type: "seven_day"}, "You've hit your weekly limit · resets Nov 12 at 2pm (Asia/Shanghai)", time.Unix(1794463200, 0)},
		{"words", claudeRefusalQuota{}, "You've hit your 5-hour limit · resets 12pm (UTC)", noon},
		{"gone resetsAt, words", claudeRefusalQuota{ResetsAt: at.Add(-time.Minute).Unix(), Type: "five_hour"}, "You've hit your 5-hour limit · resets 12pm (UTC)", noon},
		{"5-hour", claudeRefusalQuota{Type: "five_hour"}, "You've hit your 5-hour limit", at.Add(5 * time.Hour)},
		{"week", claudeRefusalQuota{Type: "seven_day"}, "You've hit your weekly limit", at.Add(24 * time.Hour)},
		{"nothing", claudeRefusalQuota{}, "API Error: Rate limited", at.Add(claudeSaidUnknownFor)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeRefusalUntil(c.q, c.said, at); !got.Equal(c.want) {
				t.Fatalf("until %v, want %v", got, c.want)
			}
		})
	}
}
