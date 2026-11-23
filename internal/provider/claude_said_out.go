package provider

// What Claude Code itself said of a Claude account being out ("You've hit
// your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)"), kept till
// the reset it named. The switch off a spent account (NextLogin) took a
// spare by its last reading, and the owner's spares' last readings were
// days old (11-10 to 11-16) with every window's reset passed since, so
// each read as unused: the warm-up had been told worktrial@ was out till
// Nov 24 five times that day, and the switch could still move Claude Code
// onto it, to stop again at its first request. A /usage read that says it
// and one that reads the account well since (claudeWindows) keep this up
// to date.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

func claudeSaidOutPath() string { return filepath.Join(filepath.Dir(Path()), "claude-limits.json") }

// claudeSaid is one account's: until when it is out, and Claude Code's
// words.
type claudeSaid struct {
	Until time.Time `json:"until"`
	Said  string    `json:"said"`
}

var claudeSaidMu sync.Mutex

// claudeSaidUnknownFor is how long an account Claude Code said is out
// stays out when its words name no reset magpie can read: then it is
// looked at again (the next /usage or warm-up says it once more).
const claudeSaidUnknownFor = time.Hour

// claudeSaidReset is the reset Claude Code names after the limit: "·
// resets Nov 24 at 5am (Asia/Shanghai)", "resets 5pm".
var claudeSaidReset = lazyre.New(`(?i)\bresets\s+(.+?)\s*$`)

// claudeSaidUntil is when the limit s says the account hit is over: the
// reset it names, read in a weekly window's span, or in a 5-hour one's
// for the session limit; else claudeSaidUnknownFor from now.
func claudeSaidUntil(s string, now time.Time) time.Time {
	if t, ok := claudeSaidResetAt(s, now); ok {
		return t
	}
	return now.Add(claudeSaidUnknownFor)
}

// claudeSaidResetAt is the reset s names, when it names one magpie can
// read that is still to come.
func claudeSaidResetAt(s string, now time.Time) (time.Time, bool) {
	if m := claudeSaidReset.FindStringSubmatch(s); m != nil {
		span := 7 * 24 * time.Hour
		if lower := strings.ToLower(s); strings.Contains(lower, "session") || strings.Contains(lower, "5-hour") {
			span = 5 * time.Hour
		}
		if t, ok := claudeResetTime(strings.TrimRight(strings.TrimSpace(m[1]), "."), now, span); ok && t.After(now) {
			return t, true
		}
	}
	return time.Time{}, false
}

func readClaudeSaid() map[string]claudeSaid {
	m := map[string]claudeSaid{}
	if b, err := os.ReadFile(claudeSaidOutPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeClaudeSaid(m map[string]claudeSaid) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(claudeSaidOutPath()), 0o700)
	writeFileAtomic(claudeSaidOutPath(), b)
}

// noteClaudeSaid keeps what s, a run of Claude Code as the account user,
// said: its limit hit (claudeLimitSaid, "(not your usage limit)" aside)
// keeps it out till the reset named. Anything else is left as it was.
func noteClaudeSaid(user, s string, now time.Time) {
	if user == "" {
		return
	}
	said := claudeNotYourLimit.ReplaceAllString(s, "")
	if !claudeLimitSaid.MatchString(said) {
		return
	}
	keepClaudeOut(user, claudeSaidUntil(said, now), said, now)
}

// keepClaudeOut keeps the account user out till until, for said, Claude
// Code's or Anthropic's words, in claude-limits.json. The latest word
// stands: a refusal for the session limit after one for the week says the
// week's is over.
func keepClaudeOut(user string, until time.Time, said string, now time.Time) {
	if user == "" || !until.After(now) {
		return
	}
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	m := readClaudeSaid()
	for k, v := range m {
		if !v.Until.After(now) {
			delete(m, k)
		}
	}
	m[strings.ToLower(user)] = claudeSaid{Until: until, Said: clipLine(strings.TrimSpace(said))}
	writeClaudeSaid(m)
}

// clearClaudeSaid drops what was kept for user: /usage read the account
// well since, and its reading decides.
func clearClaudeSaid(user string) {
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	m := readClaudeSaid()
	if _, ok := m[strings.ToLower(user)]; !ok {
		return
	}
	delete(m, strings.ToLower(user))
	writeClaudeSaid(m)
}

// claudeSaidOut says Claude Code said the account user is out, and the
// reset it named hasn't come yet.
func claudeSaidOut(user string, now time.Time) bool {
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	s, ok := readClaudeSaid()[strings.ToLower(user)]
	return ok && s.Until.After(now)
}

// claudeStaleDefault is the shortest window a Claude account has (its 5
// hours), for a reading that kept no window's span.
const claudeStaleDefault = 5 * time.Hour

// claudeStale says windows ws, read at at, are too old to go by at now:
// read longer ago than the shortest of them (claudeStaleDefault when none
// says). A reading from days back whose resets have all passed reads as
// unused (elapsed), and the switch moved Claude Code onto an account that
// was out.
func claudeStale(ws []QuotaWindow, at, now time.Time) bool {
	if at.IsZero() {
		return false
	}
	span := time.Duration(0)
	for _, w := range ws {
		if w.Span > 0 && (span == 0 || w.Span < span) {
			span = w.Span
		}
	}
	if span == 0 {
		span = claudeStaleDefault
	}
	return now.Sub(at) > span
}

// staleSpare says q, a saved Claude account's reading as routing has it,
// is too old to go by (claudeStale): one kept on disk from before (AsOf),
// or one read longer ago than its shortest window. It is read again before
// it is chosen (readSpare), and when it is still that old, every spare
// read freshly goes first.
func staleSpare(q SubscriptionQuota, now time.Time) bool {
	if q.Error != "" {
		return false
	}
	switch {
	case q.AsOf != nil:
		return claudeStale(q.Windows, *q.AsOf, now)
	case q.ReadAt != nil:
		return claudeStale(q.Windows, *q.ReadAt, now)
	}
	return false
}

// NoteClaudeOut keeps the Claude account user out till until because the
// gateway rested the whole account for its allowance (not one model's):
// the bridge's Claude Code was refused, and the switch must not move Claude
// Code onto an account the gateway won't ask either. named is whether
// Anthropic said until (its resetsAt, a reset header, its words, the
// window it reported full); a guess never shortens what is kept already.
func NoteClaudeOut(user string, until time.Time, said string, named bool) {
	now := time.Now()
	if user == "" || !until.After(now) {
		return
	}
	if strings.TrimSpace(said) == "" {
		said = "Anthropic refused the account for its usage limit"
	}
	if !named {
		claudeSaidMu.Lock()
		s, ok := readClaudeSaid()[strings.ToLower(user)]
		claudeSaidMu.Unlock()
		if ok && !s.Until.Before(until) {
			return
		}
	}
	keepClaudeOut(user, until, said, now)
}
