package provider

// Spending a Codex rate-limit reset by itself, for the accounts the user
// said may: when a request finds the account's weekly window used up and
// no other account can take it, one reset starts the windows again and the
// request goes through. Only the week's window counts — five hours pass on
// their own — and one a week at most, so a week's heavy use doesn't eat
// every reset the account holds. What was spent, and until when that week
// ran, is kept in codex-autoreset.json, so a restart doesn't spend another.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// The two times an account the user lets spend its resets by itself
// spends one, each of which can be on alone (#2078): its week used up and
// no other account left (AutoUseCodexReset), and the one about to run out
// unused (SpendExpiringCodexResets).
const (
	ResetCaseWeek     = "week"
	ResetCaseExpiring = "expiring"
)

// CodexAutoResetWhen says at which of those times the Codex account user
// spends a reset by itself: both unless the user kept it to one.
func CodexAutoResetWhen(user string) (week, expiring bool) {
	user = strings.ToLower(user)
	s := settings.Load()
	if user == "" || !slices.Contains(s.CodexAutoReset, user) {
		return false, false
	}
	switch s.CodexAutoResetOnly[user] {
	case ResetCaseWeek:
		return true, false
	case ResetCaseExpiring:
		return false, true
	}
	return true, true
}

// CodexAutoReset says whether the Codex account user spends a reset by
// itself at either time.
func CodexAutoReset(user string) bool {
	week, expiring := CodexAutoResetWhen(user)
	return week || expiring
}

// AutoResets says whether agent's account user spends its resets by
// itself at either time; only a Codex account does.
func AutoResets(agent, user string) bool {
	return agent == "codex" && CodexAutoReset(user)
}

// AutoResetsWeek says whether agent's account user spends a reset by itself
// once its week is used up and no other account can answer.
func AutoResetsWeek(agent, user string) bool {
	week, _ := CodexAutoResetWhen(user)
	return agent == "codex" && week
}

// autoResetsExpiring says whether agent's account user spends the reset
// about to run out by itself.
func autoResetsExpiring(agent, user string) bool {
	_, expiring := CodexAutoResetWhen(user)
	return agent == "codex" && expiring
}

// AutoResetWords says in a few words when user spends one by itself, for
// the CLI and TUI: "" when it never does.
func AutoResetWords(user string) string {
	switch week, expiring := CodexAutoResetWhen(user); {
	case week && expiring:
		return "auto"
	case week:
		return "auto when the week runs out"
	case expiring:
		return "auto before one expires"
	}
	return ""
}

// CodexSignedIn is the ChatGPT account Codex is signed in to now.
func CodexSignedIn() (string, bool) {
	l, ok := liveLogin("codex")
	return l.User, ok
}

// SetCodexAutoReset turns that on or off for user, at both times.
func SetCodexAutoReset(user string, on bool) error {
	return setCodexAutoReset(user, on, on)
}

// SetCodexAutoResetCase turns one of the two times (ResetCaseWeek or
// ResetCaseExpiring) on or off for user, the other kept as it is.
func SetCodexAutoResetCase(user, when string, on bool) error {
	week, expiring := CodexAutoResetWhen(strings.TrimSpace(user))
	switch when {
	case ResetCaseWeek:
		week = on
	case ResetCaseExpiring:
		expiring = on
	default:
		return fmt.Errorf("a reset is used by itself when the week runs out (%s) or before one expires (%s), not %q", ResetCaseWeek, ResetCaseExpiring, when)
	}
	return setCodexAutoReset(user, week, expiring)
}

func setCodexAutoReset(user string, week, expiring bool) error {
	user = strings.ToLower(strings.TrimSpace(user))
	s := settings.Load()
	s.CodexAutoReset = slices.DeleteFunc(slices.Clone(s.CodexAutoReset), func(u string) bool { return u == user })
	only := map[string]string{}
	for u, c := range s.CodexAutoResetOnly {
		if u != user {
			only[u] = c
		}
	}
	if user != "" && (week || expiring) {
		s.CodexAutoReset = append(s.CodexAutoReset, user)
		switch {
		case !expiring:
			only[user] = ResetCaseWeek
		case !week:
			only[user] = ResetCaseExpiring
		}
	}
	s.CodexAutoResetOnly = only
	return settings.Save(s)
}

// SetCodexResetLead sets how many minutes before a reset about to run out
// unused it is spent (resetLead), for every account: from
// settings.CodexResetLeadLeast to CodexResetLeadMost, or 0 for the default
// half hour.
func SetCodexResetLead(minutes int) error {
	if minutes != 0 && (minutes < settings.CodexResetLeadLeast || minutes > settings.CodexResetLeadMost) {
		return fmt.Errorf("a reset about to expire is used from %d minutes to %d hours before it does, not %d minutes", settings.CodexResetLeadLeast, settings.CodexResetLeadMost/60, minutes)
	}
	s := settings.Load()
	s.CodexResetLead = minutes
	return settings.Save(s)
}

var autoReset = autoResets{file: "codex-autoreset.json"}

type autoResets struct {
	sync.Mutex
	file string // its name in magpie's folder
	path string // the file read, read again when that changes (a new HOME)
	// spent: by account, the end of the week a reset was spent in, when,
	// and what spending it did — for the requests that found the same week
	// used up while it was spent, which go through on it too
	spent map[string]autoResetSpent
	// next: by account, when to look again after a look that spent none
	next map[string]time.Time
}

type autoResetSpent struct {
	Until time.Time    `json:"until"`
	At    time.Time    `json:"at"`
	Out   ResetOutcome `json:"outcome"`
}

// autoResetWait is how long an account is left before it is looked at
// again: its week not used up, or a reset that couldn't be spent.
var autoResetWait = map[bool]time.Duration{false: time.Minute, true: 10 * time.Minute}

func (a *autoResets) where() string { return filepath.Join(settings.Dir(), a.file) }

// AutoUseCodexReset spends one of the Codex account user's resets (the one
// Codex is signed in to when "") if the user turned that on for it, its
// weekly window is used up, it holds one, and none was spent by itself in
// this week yet. Code is "" when none was tried; the caller asks again
// only on "reset".
func AutoUseCodexReset(ctx context.Context, user string) (ResetOutcome, error) {
	if user == "" {
		live, ok := liveLogin("codex")
		if !ok {
			return ResetOutcome{}, nil
		}
		user = live.User
	}
	if week, _ := CodexAutoResetWhen(user); !week {
		return ResetOutcome{}, nil
	}
	var who string
	return autoReset.use(user, func(now time.Time) (*time.Time, bool, error) {
		var windows []QuotaWindow
		var resets *ResetCredits
		var err error
		who, windows, resets, err = codexUserWindows(ctx, user)
		if err != nil {
			return nil, false, err
		}
		return weekUsedUp(windows, now), resets != nil && resets.Count > 0, nil
	}, func() (ResetOutcome, error) { return useCodexReset(ctx, who, ResetUsedWeek) })
}

// use spends one of user's resets by itself if look finds its week used
// up (and when that week ends) and a reset held, and none was spent by
// itself in that week yet; spend spends it.
func (a *autoResets) use(user string, look func(now time.Time) (week *time.Time, held bool, err error), spend func() (ResetOutcome, error)) (ResetOutcome, error) {
	key := strings.ToLower(user)
	asked := time.Now()
	a.Lock() // one look at a time: those waiting take what it did
	defer a.Unlock()
	a.load()
	now := time.Now()
	if s, ok := a.spent[autoResetAny]; ok && now.Before(s.Until) {
		return ResetOutcome{}, nil
	}
	if s, ok := a.spent[key]; ok {
		if asked.Before(s.At) {
			// asked while it was being spent, for the same week used up
			return s.Out, nil
		}
		if now.Before(s.Until) {
			return ResetOutcome{}, nil
		}
	}
	if now.Before(a.next[key]) {
		return ResetOutcome{}, nil
	}
	week, held, err := look(now)
	if err != nil {
		a.next[key] = now.Add(autoResetWait[true])
		return ResetOutcome{}, err
	}
	if week == nil || !held {
		a.next[key] = now.Add(autoResetWait[week != nil])
		return ResetOutcome{}, nil
	}
	out, err := spend()
	if err != nil || out.Code != "reset" {
		a.next[key] = now.Add(autoResetWait[true])
		return out, err
	}
	a.spent[key] = autoResetSpent{Until: *week, At: time.Now(), Out: out}
	a.save()
	return out, nil
}

// weekUsedUp is when the used-up weekly window of windows ends, nil when
// none is used up: the five hours' being full doesn't count.
func weekUsedUp(windows []QuotaWindow, now time.Time) *time.Time {
	for _, w := range windows {
		if w.Span >= 24*time.Hour && !w.Aside && w.Used >= 100 && w.ResetsAt != nil && w.ResetsAt.After(now) {
			return w.ResetsAt
		}
	}
	return nil
}

// load reads what was spent, once; a.Lock is held. A file that can't be
// read or doesn't parse is unknown, never none: read as none, the week's
// auto-use would spend a second reset (#2017). What was spent is then
// taken from codex-resets-used.json, which keeps every reset used for a
// week used up, and when that can't be read either no account spends one
// by itself for a week, the longest a spend the file held could still
// count. A file that doesn't parse is copied aside, as noteResetUsed keeps
// codex-resets-used.json's, and only then written over.
func (a *autoResets) load() {
	if a.path == a.where() {
		return
	}
	a.path = a.where()
	a.spent, a.next = map[string]autoResetSpent{}, map[string]time.Time{}
	b, err := os.ReadFile(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	kept := false
	if err == nil {
		if err = json.Unmarshal(b, &a.spent); err == nil {
			if a.spent == nil {
				a.spent = map[string]autoResetSpent{}
			}
			return
		}
		aside := a.path + ".bad-" + time.Now().UTC().Format("20060102T150405.000000000")
		if werr := os.WriteFile(aside, b, 0o600); werr != nil {
			err = fmt.Errorf("%w; keeping it aside: %v", err, werr)
		} else {
			kept = true
			err = fmt.Errorf("%w; kept aside as %s", err, aside)
		}
	}
	if a.spent == nil {
		a.spent = map[string]autoResetSpent{}
	}
	now := time.Now()
	uses, uerr := readResetsUsed()
	if uerr != nil {
		a.spent[autoResetAny] = autoResetSpent{Until: now.Add(autoResetWeek), At: now}
		log.Printf("%s: %v; %v too, so no Codex account spends a reset by itself until %s", a.path, err, uerr, now.Add(autoResetWeek).Format(time.RFC3339))
	} else {
		for _, u := range uses {
			key := strings.ToLower(u.User)
			if u.Why != ResetUsedWeek || now.Sub(u.At) >= autoResetWeek || a.spent[key].At.After(u.At) {
				continue
			}
			a.spent[key] = autoResetSpent{Until: u.At.Add(autoResetWeek), At: u.At, Out: ResetOutcome{Code: "reset", Windows: u.Windows}}
		}
		log.Printf("%s: %v; what was spent is taken from %s", a.path, err, resetsUsedPath())
	}
	if kept {
		a.save()
	}
}

// autoResetAny stands for every account in spent, when what was spent
// isn't known; autoResetWeek is the longest a spend counts for.
const (
	autoResetAny  = "*"
	autoResetWeek = 7 * 24 * time.Hour
)

// save writes it; a.Lock is held. Through steady.WriteFile, as every write
// of magpie's own state is (#1505): a crash mid-write left logins.json and
// plugin-auth.json zeroed at full length, and one here would read as no
// reset spent this week, so the next look spent a second one.
func (a *autoResets) save() {
	now := time.Now()
	for k, s := range a.spent { // weeks gone by say nothing any more
		if now.After(s.Until) {
			delete(a.spent, k)
		}
	}
	b, _ := json.MarshalIndent(a.spent, "", "  ")
	_ = os.MkdirAll(settings.Dir(), 0o755)
	if err := steady.WriteFile(a.path, append(b, '\n'), 0o644); err != nil {
		log.Printf("writing what a week's auto-use spent to %s: %v", a.path, err)
	}
}
