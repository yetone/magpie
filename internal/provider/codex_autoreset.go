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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// CodexAutoReset says whether the Codex account user spends a reset by
// itself once its week is used up.
func CodexAutoReset(user string) bool {
	return user != "" && slices.Contains(settings.Load().CodexAutoReset, strings.ToLower(user))
}

// AutoResets says whether agent's account user spends its resets by
// itself; only a Codex account does.
func AutoResets(agent, user string) bool {
	return agent == "codex" && CodexAutoReset(user)
}

// CodexSignedIn is the ChatGPT account Codex is signed in to now.
func CodexSignedIn() (string, bool) {
	l, ok := liveLogin("codex")
	return l.User, ok
}

// SetCodexAutoReset turns that on or off for user.
func SetCodexAutoReset(user string, on bool) error {
	user = strings.ToLower(strings.TrimSpace(user))
	s := settings.Load()
	s.CodexAutoReset = slices.DeleteFunc(s.CodexAutoReset, func(u string) bool { return u == user })
	if on && user != "" {
		s.CodexAutoReset = append(s.CodexAutoReset, user)
	}
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
	if !CodexAutoReset(user) {
		return ResetOutcome{}, nil
	}
	var who string
	return autoReset.use(user, func(now time.Time) (*time.Time, bool, error) {
		var tok, accountID string
		var err error
		who, tok, accountID, err = codexUserToken(ViaLogin(ctx, "codex", user), user)
		if err != nil {
			return nil, false, err
		}
		_, windows, resets, _, _, err := codexWindows(ViaLogin(ctx, "codex", who), tok, accountID)
		if err != nil {
			return nil, false, err
		}
		return weekUsedUp(windows, now), resets != nil && resets.Count > 0, nil
	}, func() (ResetOutcome, error) { return UseCodexReset(ctx, who) })
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

// load reads what was spent, once; a.Lock is held.
func (a *autoResets) load() {
	if a.path == a.where() {
		return
	}
	a.path = a.where()
	a.spent, a.next = map[string]autoResetSpent{}, map[string]time.Time{}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &a.spent)
		if a.spent == nil {
			a.spent = map[string]autoResetSpent{}
		}
	}
}

// save writes it; a.Lock is held.
func (a *autoResets) save() {
	now := time.Now()
	for k, s := range a.spent { // weeks gone by say nothing any more
		if now.After(s.Until) {
			delete(a.spent, k)
		}
	}
	b, _ := json.MarshalIndent(a.spent, "", "  ")
	_ = os.MkdirAll(settings.Dir(), 0o755)
	_ = os.WriteFile(a.path, append(b, '\n'), 0o644)
}
