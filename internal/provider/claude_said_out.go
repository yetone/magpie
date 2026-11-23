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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

func claudeSaidOutPath() string { return filepath.Join(filepath.Dir(Path()), "claude-limits.json") }

// claudeSaid is one account's: until when it is out, and Claude Code's
// words. Models is what is out for one model family only, by family
// ("fable": Claude Code's "You've hit your Fable limit", its
// seven_day_overage_included window), each till its own reset: the
// account still serves its other models, so Claude Code is moved off it
// only while it is on that family (claudeSaidOutFor). An entry of one
// family alone has no Until. A file written before Models was kept reads
// as whole-account entries, as it always did, and a magpie from before
// reads an entry of one family alone as not out.
type claudeSaid struct {
	Until  time.Time             `json:"until,omitzero"`
	Said   string                `json:"said,omitempty"`
	Models map[string]claudeSaid `json:"models,omitempty"`
}

// out says the entry, for one family (empty: the whole account) or the
// whole, stands at now.
func (s claudeSaid) out(family string, now time.Time) bool {
	if s.Until.After(now) {
		return true
	}
	return family != "" && s.Models[family].Until.After(now)
}

// claudeFamilies is the model families Anthropic gives a limit of their
// own, as a model id or alias names them.
var claudeFamilies = lazyre.New(`(?i)(opus|sonnet|haiku|fable|mythos)`)

// claudeModelFamily is the family of a Claude model id or alias
// ("claude-fable-5-2[1m]", "fable", "opusplan"): fable, opus, sonnet,
// haiku or mythos; empty for none of them ("default", "<synthetic>").
func claudeModelFamily(model string) string {
	if m := claudeFamilies.FindStringSubmatch(model); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// ClaudeModelFamily is claudeModelFamily, for the gateway.
func ClaudeModelFamily(model string) string { return claudeModelFamily(model) }

// claudeFamilyLimitSaid is Claude Code naming one family's limit: "You've
// hit your Fable limit · resets …", "… Opus limit", "You've reached your
// Fable limit for this week".
var claudeFamilyLimitSaid = lazyre.New(`(?i)(?:hit|reached) your (opus|sonnet|haiku|fable|mythos) limit`)

// claudeLimitFamily is the one model family a refusal is for: the family
// of the window Anthropic named (kind, its rateLimitType:
// seven_day_overage_included is Fable's, as claudeKinds has it), else the
// one Claude Code's words name (claudeFamilyLimitSaid). Empty: the whole
// account (the 5-hour, weekly or usage limit, or a window it doesn't
// say).
func claudeLimitFamily(kind, said string) string {
	if kind != "" {
		for _, k := range claudeKinds {
			if k.kind == kind {
				return k.model
			}
		}
		return ""
	}
	if m := claudeFamilyLimitSaid.FindStringSubmatch(said); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
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

// readClaudeSaid is what claude-limits.json keeps. A file that can't be
// read or parsed is unknown, not "none out": ok is false then, and the
// map empty, so nothing is taken as out on its word. One that isn't there
// is none kept (ok).
func readClaudeSaid() (map[string]claudeSaid, bool) {
	m := map[string]claudeSaid{}
	b, err := os.ReadFile(claudeSaidOutPath())
	if errors.Is(err, fs.ErrNotExist) {
		return m, true
	}
	if err != nil || json.Unmarshal(b, &m) != nil {
		return map[string]claudeSaid{}, false
	}
	return m, true
}

// claudeSaidNow is readClaudeSaid's map alone.
func claudeSaidNow() map[string]claudeSaid {
	m, _ := readClaudeSaid()
	return m
}

func writeClaudeSaid(m map[string]claudeSaid) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(claudeSaidOutPath()), 0o700)
	writeFileAtomic(claudeSaidOutPath(), b) // temp, then renamed over
	claudeSaidCache.Lock()
	claudeSaidCache.at = time.Time{} // read again on the next ask
	claudeSaidCache.Unlock()
}

// pruneClaudeSaid drops from m what is over at now: an account's whole
// entry and each family's on their own, and the account once nothing of
// it stands.
func pruneClaudeSaid(m map[string]claudeSaid, now time.Time) {
	for k, v := range m {
		for f, e := range v.Models {
			if !e.Until.After(now) {
				delete(v.Models, f)
			}
		}
		if len(v.Models) == 0 {
			v.Models = nil
		}
		if !v.Until.After(now) {
			v.Until, v.Said = time.Time{}, ""
		}
		if v.Until.IsZero() && v.Models == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
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
	keepClaudeOutFor(user, claudeLimitFamily("", said), claudeSaidUntil(said, now), said, now)
}

// keepClaudeOut keeps the account user out till until, for said, Claude
// Code's or Anthropic's words, in claude-limits.json. The latest word
// stands: a refusal for the session limit after one for the week says the
// week's is over.
func keepClaudeOut(user string, until time.Time, said string, now time.Time) {
	keepClaudeOutFor(user, "", until, said, now)
}

// keepClaudeOutFor is keepClaudeOut for one model family's limit only
// (family; empty is the whole account's): the account's entry for that
// family is kept, and its whole entry, and the other families', stay as
// they were.
func keepClaudeOutFor(user, family string, until time.Time, said string, now time.Time) {
	if user == "" || !until.After(now) {
		return
	}
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	// one that can't be read is written over: what it held is unknown
	// either way, and the word just said is known
	m := claudeSaidNow()
	pruneClaudeSaid(m, now)
	key := strings.ToLower(user)
	e := claudeSaid{Until: until, Said: clipLine(strings.TrimSpace(said))}
	v := m[key]
	if family == "" {
		e.Models = v.Models
		m[key] = e
	} else {
		if v.Models == nil {
			v.Models = map[string]claudeSaid{}
		}
		v.Models[family] = e
		m[key] = v
	}
	writeClaudeSaid(m)
}

// clearClaudeSaid drops what was kept for user: /usage read the account
// well since, as ws, and its reading decides. A family's limit is kept
// while ws reads that family's own window full (Fable's "7 days · Fable"
// at 100, not reset) or has none of it: the switch goes by the windows
// of every model alone (usedPast), so a family's is known from here
// only.
func clearClaudeSaid(user string, ws []QuotaWindow) {
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	m, ok := readClaudeSaid()
	key := strings.ToLower(user)
	v, kept := m[key]
	if !ok || !kept {
		return
	}
	now := time.Now()
	left := map[string]claudeSaid{}
	for f, e := range v.Models {
		if !e.Until.After(now) {
			continue
		}
		read, full := false, false
		for _, w := range ws {
			if w.Model != "" && claudeModelFamily(w.Model) == f {
				read = true
				full = full || w.Used >= 100 && !resetPassed(w, now)
			}
		}
		if !read || full {
			left[f] = e
		}
	}
	if len(left) == 0 {
		delete(m, key)
	} else {
		m[key] = claudeSaid{Models: left}
	}
	writeClaudeSaid(m)
}

// claudeSaidOut says Claude Code said the account user is out, and the
// reset it named hasn't come yet, with the model Claude Code is on not
// known: a family's limit counts too (claudeSaidOutFor with no
// families).
func claudeSaidOut(user string, now time.Time) bool {
	return claudeSaidOutFor(user, nil, now)
}

// claudeSaidOutFor says the account user is out for Claude Code on the
// model families fams (claudeOnFamilies): out whole, or out for one of
// them. fams nil is not known, and then any family's limit counts, as the
// whole account's did before a limit was kept per family: Claude Code
// held on an account it may be refused on stops mid-task.
func claudeSaidOutFor(user string, fams []string, now time.Time) bool {
	claudeSaidMu.Lock()
	defer claudeSaidMu.Unlock()
	s, ok := claudeSaidNow()[strings.ToLower(user)]
	if !ok {
		return false
	}
	if s.out("", now) {
		return true
	}
	if fams == nil {
		for f := range s.Models {
			if s.out(f, now) {
				return true
			}
		}
		return false
	}
	for _, f := range fams {
		if s.out(f, now) {
			return true
		}
	}
	return false
}

// claudeSaidCache is claude-limits.json as the gateway last read it, for
// ClaudeModelOutUntil, which every request's plan of a Claude account
// asks: read again once a second at most, and at once after this magpie
// writes it.
var claudeSaidCache struct {
	sync.Mutex
	at  time.Time
	mod time.Time
	n   int64
	m   map[string]claudeSaid
}

// claudeSaidCacheFor is how long the gateway goes by the file it read
// before it looks at it again.
const claudeSaidCacheFor = time.Second

// ClaudeModelOutUntil is until when the Claude account user is out for
// model's family alone (Claude Code's "You've hit your Fable limit", kept
// by the warm-up, a /usage run, a refusal Claude Code wrote, or the
// gateway itself), zero when it isn't: the gateway puts the account
// behind those with room for that model, as for a window it read full,
// and still asks it for the others. A whole-account entry isn't this: the
// gateway rests an account whole by its own refusals and readings.
func ClaudeModelOutUntil(user, model string, now time.Time) time.Time {
	family := claudeModelFamily(model)
	if user == "" || family == "" {
		return time.Time{}
	}
	c := &claudeSaidCache
	c.Lock()
	defer c.Unlock()
	if now.Sub(c.at) > claudeSaidCacheFor || now.Before(c.at) {
		c.at = now
		fi, err := os.Stat(claudeSaidOutPath())
		switch {
		case err != nil:
			c.m, c.mod, c.n = nil, time.Time{}, 0
		case c.m == nil || !fi.ModTime().Equal(c.mod) || fi.Size() != c.n:
			c.m, c.mod, c.n = claudeSaidNow(), fi.ModTime(), fi.Size()
		}
	}
	if t := c.m[strings.ToLower(user)].Models[family].Until; t.After(now) {
		return t
	}
	return time.Time{}
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
	noteClaudeOutFor(user, "", until, said, named)
}

// NoteClaudeModelOut is NoteClaudeOut for the gateway resting one model of
// the Claude account user, and not the account, for its family's own
// limit (Anthropic's "You've hit your Fable limit"): the account is kept
// out for that family alone, so the switch passes over it as a spare for
// Claude Code on that family only.
func NoteClaudeModelOut(user, model string, until time.Time, said string, named bool) {
	if family := claudeModelFamily(model); family != "" {
		noteClaudeOutFor(user, family, until, said, named)
	}
}

func noteClaudeOutFor(user, family string, until time.Time, said string, named bool) {
	now := time.Now()
	if user == "" || !until.After(now) {
		return
	}
	if strings.TrimSpace(said) == "" {
		said = "Anthropic refused the account for its usage limit"
	}
	if !named {
		claudeSaidMu.Lock()
		s, ok := claudeSaidNow()[strings.ToLower(user)]
		claudeSaidMu.Unlock()
		if family != "" {
			s, ok = s.Models[family], ok && s.Models[family].Until.After(now)
		}
		if ok && !s.Until.Before(until) {
			return
		}
	}
	keepClaudeOutFor(user, family, until, said, now)
}
