package provider

// Claude Code on its own login asks Anthropic itself, so magpie learns the
// account it is on is out only from a /usage run, which unasked comes
// claudeUsageWait (5 to 15 minutes) after the last, and the switch looked
// only every loginSwitchEvery. An account burnt from 4% left to its limit
// in two minutes, and Claude Code said "You've hit your 5-hour limit" for
// over a minute while the other account had all of it left (#4443,
// mydosu). So while Claude has an account to move to, its sign-in is
// looked at every claudeLookEvery, and two things make that look read
// /usage at once rather than at its wait:
//
//   - the account near out (claudeNearOut): read every claudeNearOutWait;
//   - Claude Code having written a rate_limit refusal into one of its
//     sessions since the last read (scanClaudeLimits), which is what it
//     shows as "You've hit your … limit". Which account it was refused on
//     the transcript doesn't say, so one written since the switch found
//     Claude Code on the account is taken as that account's: it is kept
//     out till the reset the refusal names (keepClaudeRefusal), and the
//     read cues; a read that comes out well since ends it.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// claudeLookEvery is how often Claude's sign-in is looked at between
// KeepOnAnAccountWithRoom's full looks, while it has an account to move to.
// A var so tests can say.
var claudeLookEvery = time.Minute

// claudeNearOutWait is how long after a /usage run the next unasked one is
// due while the account Claude Code is on is near out, in place of
// claudeUsageWait.
const claudeNearOutWait = time.Minute

// claudeNearOut says the windows ws, of the account Claude Code is on, are
// near out: one that stops the account for every model is at
// codexNearShare or past.
func claudeNearOut(ws []QuotaWindow, now time.Time) bool {
	return usedPast(SubscriptionQuota{Windows: ws}, codexNearShare, now)
}

// claudeWaitOf is how long after e's last /usage run the next unasked one
// of the account Claude Code is on is due.
func claudeWaitOf(e claudeUsageEntry, now time.Time) time.Duration {
	if e.ws != nil && claudeNearOut(e.windows(now), now) {
		return min(e.wait, claudeNearOutWait)
	}
	return e.wait
}

// claudeLimitHit is when Claude Code last wrote a rate_limit refusal into
// one of its sessions, as unix nanoseconds (zero: none seen).
var claudeLimitHit atomic.Int64

// claudeLimitHitAfter says Claude Code was refused for its limit after t.
func claudeLimitHitAfter(t time.Time) bool {
	n := claudeLimitHit.Load()
	return n != 0 && time.Unix(0, n).After(t)
}

// claudeTranscripts is where scanClaudeLimits got to: the sessions written
// to are read on from where the last scan stopped, so each line is read
// once.
var claudeTranscripts struct {
	sync.Mutex
	started bool
	off     map[string]int64
}

// claudeScanTail is the most of one session read by one scan: a refusal is
// the last line Claude Code writes, and a session that grew more than this
// since is read from its end.
const claudeScanTail = 1 << 20

// scanClaudeLimits reads what Claude Code wrote to its sessions (each
// project's *.jsonl) since the last scan and keeps the time of the latest
// rate_limit refusal in claudeLimitHit. The first scan only marks where the
// sessions end: what was refused before magpie looked is the startup
// read's to tell. Its cost is a stat of each session and a read of what
// was added to the ones written to.
func scanClaudeLimits() {
	c := &claudeTranscripts
	c.Lock()
	defer c.Unlock()
	projects := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects")
	dirs, _ := os.ReadDir(projects)
	off := make(map[string]int64, len(c.off))
	for _, d := range dirs {
		root := filepath.Join(projects, d.Name())
		if !d.IsDir() {
			if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
				continue
			}
		}
		files, _ := os.ReadDir(root)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(root, f.Name())
			was, known := c.off[path]
			switch {
			case !c.started:
				off[path] = fi.Size()
			case known && was == fi.Size():
				off[path] = was
			default:
				if !known {
					was = 0 // a session begun since the last scan
				}
				off[path] = scanClaudeSession(path, was, fi.Size())
			}
		}
	}
	c.off, c.started = off, true
}

// scanClaudeSession reads path from from to size for rate_limit refusals,
// and answers where it got to: the end of its last whole line, so a line
// still being written is read whole next time.
func scanClaudeSession(path string, from, size int64) int64 {
	if size < from {
		from = 0 // written over
	}
	start := max(from, size-claudeScanTail)
	f, err := os.Open(path)
	if err != nil {
		return from
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return from
	}
	r := bufio.NewReader(io.LimitReader(f, size-start))
	pos := start
	if start > from {
		// started mid-line: what is before the first newline is cut
		skipped, err := r.ReadBytes('\n')
		pos += int64(len(skipped))
		if err != nil {
			return size // all one line not ended, longer than any refusal
		}
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return pos // the rest is a line not yet ended
		}
		pos += int64(len(line))
		noteClaudeModel(line)
		noteClaudeRefusal(line)
	}
}

// claudeOnModels is the model families Claude Code answered on lately, by
// the latest time the scan read one of its replies on each ("fable":
// "message":{"model":"claude-fable-5-1",…}): what claudeOnFamilies says
// Claude Code is on, every session counted, as two sessions on two models
// both stop on an account one of them is out for.
var claudeOnModels struct {
	sync.Mutex
	at map[string]time.Time
}

// claudeOnRecent is how long a model Claude Code answered on counts as one
// it is on.
const claudeOnRecent = time.Hour

// claudeReplyModel is what marks a line of Claude Code's that names the
// model it answered on, cheaply found before it is read.
var claudeReplyModel = []byte(`"model":"`)

// noteClaudeModel keeps the family of the model line, a reply Claude Code
// wrote, was answered on, by the line's own time. A refusal's is
// "<synthetic>", no family.
func noteClaudeModel(line []byte) {
	if !bytes.Contains(line, []byte(`"type":"assistant"`)) {
		return
	}
	i := bytes.Index(line, claudeReplyModel)
	if i < 0 {
		return
	}
	rest := line[i+len(claudeReplyModel):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 || j > 200 {
		return
	}
	family := claudeModelFamily(string(rest[:j]))
	if family == "" {
		return
	}
	at := time.Now()
	if k := bytes.Index(line, []byte(`"timestamp":"`)); k >= 0 {
		ts := line[k+len(`"timestamp":"`):]
		if e := bytes.IndexByte(ts, '"'); e > 0 && e < 64 {
			if t, err := time.Parse(time.RFC3339Nano, string(ts[:e])); err == nil {
				at = t
			}
		}
	}
	c := &claudeOnModels
	c.Lock()
	if c.at == nil {
		c.at = map[string]time.Time{}
	}
	if at.After(c.at[family]) {
		c.at[family] = at
	}
	c.Unlock()
}

// claudeOnFamilies is the model families Claude Code is on at now: those
// it answered on in the last claudeOnRecent, else the one its settings
// name ("model": "opus" in settings.json, read, never written), else nil,
// not known. Used to tell whether an account out for one family's limit
// alone (claudeSaidOutFor) stops Claude Code.
func claudeOnFamilies(now time.Time) []string {
	var fams []string
	c := &claudeOnModels
	c.Lock()
	for f, at := range c.at {
		if now.Sub(at) < claudeOnRecent {
			fams = append(fams, f)
		}
	}
	c.Unlock()
	if len(fams) > 0 {
		slices.Sort(fams)
		return fams
	}
	if f := claudeSettingsFamily(); f != "" {
		return []string{f}
	}
	return nil
}

// claudeSettingsFamily is the family of the model Claude Code's own
// settings.json names, read only; empty when it names none, one of no
// family ("default"), or can't be read.
func claudeSettingsFamily() string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(claudeCredentialsPath()), "settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return claudeModelFamily(s.Model)
}

// claudeRefusal is what marks a line Claude Code writes for a refusal for
// the account's limit ("You've hit your session limit"), cheaply found
// before it is parsed.
var claudeRefusal = []byte(`"rate_limit"`)

// noteClaudeRefusal keeps the time of line in claudeLimitHit when it is
// Claude Code's own message for a rate_limit refusal.
func noteClaudeRefusal(line []byte) {
	if !bytes.Contains(line, claudeRefusal) {
		return
	}
	var m struct {
		Error     string `json:"error"`
		APIError  bool   `json:"isApiErrorMessage"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		Quota  claudeRefusalQuota `json:"quotaLimits"`
		Params struct {
			Info claudeRefusalQuota `json:"rate_limit_info"`
		} `json:"apiErrorParams"`
	}
	if json.Unmarshal(line, &m) != nil || m.Error != "rate_limit" || !m.APIError {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, m.Timestamp)
	if err != nil {
		at = time.Now()
	}
	var said string
	for _, c := range m.Message.Content {
		if c.Type == "text" && c.Text != "" {
			said = c.Text
			break
		}
	}
	q := m.Quota
	if q.ResetsAt == 0 && q.Type == "" {
		q = m.Params.Info
	}
	claudeRefused.Lock()
	if at.After(claudeRefused.at) {
		claudeRefused.at, claudeRefused.said, claudeRefused.until, claudeRefused.kept = at, said, claudeRefusalUntil(q, said, at), false
		claudeRefused.family = claudeLimitFamily(q.Type, said)
	}
	claudeRefused.Unlock()
	for {
		was := claudeLimitHit.Load()
		if at.UnixNano() <= was || claudeLimitHit.CompareAndSwap(was, at.UnixNano()) {
			return
		}
	}
}

// claudeRefusalQuota is what Claude Code keeps of the limit a refusal
// was for: its quotaLimits, or apiErrorParams' rate_limit_info, as
// Anthropic's unified headers had it ("seven_day", resetsAt in unix
// seconds).
type claudeRefusalQuota struct {
	Status   string `json:"status"`
	ResetsAt int64  `json:"resetsAt"`
	Type     string `json:"rateLimitType"`
}

// claudeRefused is the latest refusal the scan read: when, until when it
// says the account is out (claudeRefusalUntil), its words, and whether it
// was kept for the account Claude Code was on (keepClaudeRefusal).
var claudeRefused struct {
	sync.Mutex
	at, until time.Time
	said      string
	kept      bool
	// family is the one model family the refusal was for
	// (claudeLimitFamily: seven_day_overage_included, "your Fable
	// limit"); empty for the whole account
	family string
}

// claudeRefusalUntil is until when the refusal at at says the account is
// out: the reset Anthropic named (resetsAt), else the one its words name
// ("· resets Nov 12 at 2pm (Asia/Shanghai)"), else as long as the window
// it named can be out from at, five hours for the 5-hour one and a day
// for any other, where a week guessed would write the account off for
// longer than it may be out; else claudeSaidUnknownFor. A reading of the
// account that comes out well since (clearClaudeSaid) ends it sooner.
func claudeRefusalUntil(q claudeRefusalQuota, said string, at time.Time) time.Time {
	if q.ResetsAt > 0 {
		if t := time.Unix(q.ResetsAt, 0); t.After(at) {
			return t
		}
	}
	if t, ok := claudeSaidResetAt(said, at); ok {
		return t
	}
	switch {
	case q.Type == "five_hour":
		return at.Add(5 * time.Hour)
	case q.Type != "":
		return at.Add(24 * time.Hour)
	}
	return at.Add(claudeSaidUnknownFor)
}

// claudeSwitchSettles is how long after the switch found Claude Code on an
// account a refusal may still be the account it was on before: a Claude
// Code already running reads its keychain again only every half minute,
// and a request it sent before is answered after.
const claudeSwitchSettles = time.Minute

// keepClaudeRefusal keeps the account Claude Code is on, user, out till
// the reset the latest refusal named (claude-limits.json), when Claude
// Code was refused since it has been on it (since) and settled there
// (claudeSwitchSettles): the transcript doesn't name the account, and one
// written before is another's, as is one that names the very reset kept
// for another account already. The switch then
// passes over the account till its reset, however its /usage reads fail,
// rather than taking it again as the account to go back to, or as a spare
// once Claude Code is moved off. Not while the gateway serves Claude
// Code's requests from its accounts: a refusal is then the gateway's, of
// whichever accounts it tried.
func keepClaudeRefusal(user string, since, now time.Time) {
	if ClaudeThroughMagpie() && !SwitchOnly("claude") {
		return
	}
	r := &claudeRefused
	r.Lock()
	due := !r.kept && r.at.After(since.Add(claudeSwitchSettles)) && r.until.After(now)
	until, said, family := r.until, r.said, r.family
	r.kept = r.kept || due
	r.Unlock()
	if !due {
		return
	}
	claudeSaidMu.Lock()
	kept := claudeSaidNow()
	claudeSaidMu.Unlock()
	for k, v := range kept {
		if k != strings.ToLower(user) && (family == "" && v.Until.Equal(until) || family != "" && v.Models[family].Until.Equal(until)) {
			return // the account moved off's, refused once more
		}
	}
	if said == "" {
		said = "Claude Code was refused for its limit"
	}
	keepClaudeOutFor(user, family, until, said, now)
}

// claudeLookDue says Claude's sign-in is to be looked at before the next
// full look: it has an account to move to, and the account Claude Code is
// on is near out, or Claude Code was refused for its limit since that
// account's last /usage run. A refusal makes the account's reading stale
// (StaleAllowance), so the look reads it even with allowances read only
// when asked.
func claudeLookDue() bool {
	if leftAlone("claude") {
		return false
	}
	var active string
	spare := false
	for _, l := range Logins("claude") {
		switch {
		case l.Active:
			active = l.User
		case l.On && l.Lapsed == "" && l.home == "" && !l.Token:
			spare = true
		}
	}
	if active == "" || !spare {
		return false
	}
	since := claudeOnSince(active, time.Now())
	scanClaudeLimits()
	keepClaudeRefusal(active, since, time.Now())
	claudeUsage.Lock()
	e := claudeUsage.m[strings.ToLower(active)]
	claudeUsage.Unlock()
	if claudeLimitHitAfter(e.tried) {
		StaleAllowance("claude", active)
		return true
	}
	return e.ws != nil && claudeNearOut(e.windows(time.Now()), time.Now())
}

// claudeOn is the account the switch last found Claude Code on, and since
// when: a refusal Claude Code wrote before is another account's.
var claudeOn struct {
	sync.Mutex
	user  string
	since time.Time
}

// claudeOnSince is since when the switch has found Claude Code on user,
// now when it finds it there first.
func claudeOnSince(user string, now time.Time) time.Time {
	claudeOn.Lock()
	defer claudeOn.Unlock()
	if claudeOn.since.IsZero() || !strings.EqualFold(claudeOn.user, user) {
		claudeOn.user, claudeOn.since = user, now
	}
	return claudeOn.since
}

// claudeLimitSaid is Claude Code's own word that the account is out:
// "You've hit your session limit", "… weekly limit", "… Fable limit for
// this week", "… limit · resets 5pm" (2.1.327 builds it as "You've hit
// your " and the window's words), and the older "usage limit reached".
var claudeLimitSaid = lazyre.New(`(?i)(?:hit|reached) your (?:[\w.-]+ ){0,3}limit|\b(?:session|weekly|usage) limit (?:reached|hit)`)

// claudeOutUnread says the account Claude Code is on, user, whose reading
// q (known: there is one) tells no allowance, is out all the same: the
// reading is Claude Code's word that it hit its limit, or it failed (a
// /usage run killed at its deadline, not read yet) while Claude Code was
// refused for its limit since it has been on user (claudeLimitHit). Both
// were read as "not known" and kept Claude Code on the account for good:
// /usage killed at its 20 seconds on every look, Claude Code stopped
// mid-task on the owner's accounts with every other one having room, and
// only marking it limited by hand moved it on. A refusal of the account
// itself (signed out, 401) is not this: nothing says it is out.
//
// A limit of one model family alone (Claude Code's "You've hit your
// Fable limit") is out only for Claude Code on that family (fams,
// claudeOnFamilies; nil, not known, counts every family).
func claudeOutUnread(user string, fams []string, known bool, q SubscriptionQuota, now time.Time) bool {
	if known && q.Error == "" {
		return false // read: the reading decides
	}
	said := claudeNotYourLimit.ReplaceAllString(q.Error, "")
	if claudeLimitSaid.MatchString(said) {
		return claudeFamilyStops(claudeLimitFamily("", said), fams)
	}
	if claudeDenied(said) || !claudeLimitHitAfter(claudeOnSince(user, now)) {
		return false
	}
	claudeRefused.Lock()
	family := claudeRefused.family
	claudeRefused.Unlock()
	return claudeFamilyStops(family, fams)
}

// claudeFamilyStops says a limit of family (empty: the whole account's)
// stops Claude Code on the families fams (nil: not known, so it does).
func claudeFamilyStops(family string, fams []string) bool {
	return family == "" || fams == nil || slices.Contains(fams, family)
}
