package provider

// Starting a ChatGPT account's next window as soon as the last one resets.
// A Codex window starts at the account's first request after it resets, not
// at the reset: an account that sits idle for a day after its week ends has
// its next week end a day later too. So, while the setting is on, whichever
// magpie runs the gateway looks at each account's windows every few minutes
// and, when one has started over — its reset time passed, or its use fell
// back (OpenAI resetting everyone's limits early) — and nothing has used it
// since, sends that account one tiny request, as the gateway sends any.
// Once per reset: what it saw and did is kept in codex-warmup.json, so a
// restart doesn't send it again. A window still not started after one —
// the read from before it, a request that didn't start it, or warm-ups
// given up on — is sent another later, each wait twice the last, even if
// the backend keeps reporting a full window's reset time for an idle one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

const (
	// codexWarmEvery is how often the windows are looked at.
	codexWarmEvery = 5 * time.Minute
	// warmSlack is how far into a window it is still taken as unused: an
	// idle window's reset is always a whole window from now.
	warmSlack = 10 * time.Minute
	// warmTries is how many failed warm-ups one reset gets.
	warmTries = 3
	// warmRetry is how long a window a warm-up left not started waits for
	// the next, doubling each time up to warmRetryMax.
	warmRetry    = 15 * time.Minute
	warmRetryMax = 4 * time.Hour
	// warmKickEvery is how often a usage read finding an account's window
	// not started has its windows looked at before the next round.
	warmKickEvery = 10 * time.Minute
)

// warmWindow is one window of an account as last seen, and when magpie
// last started it.
type warmWindow struct {
	ResetsAt time.Time `json:"resetsAt,omitzero"`
	Used     float64   `json:"used"`
	Warmed   time.Time `json:"warmed,omitzero"`
	// Pending is a reset whose warm-up failed, tried again next time
	// until warmTries have failed.
	Pending bool `json:"pending,omitempty"`
	Failed  int  `json:"failed,omitempty"`
	// Daily is the day ("2006-01-02") the time of day last started this
	// window for (warmup_daily.go).
	Daily string `json:"daily,omitempty"`
	// Idle is how many warm-ups in a row left it not started, and Retry
	// when it is sent the next while it still isn't.
	Idle  int       `json:"idle,omitempty"`
	Retry time.Time `json:"retry,omitzero"`
}

// warmState is every account's windows, by lower-cased user and window name.
type warmState map[string]map[string]warmWindow

func codexWarmPath() string { return filepath.Join(filepath.Dir(Path()), "codex-warmup.json") }

func readWarmState(path string) warmState {
	st := warmState{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// CodexWarm is what came of looking at one account: the windows it was
// sent a request for, and why that failed.
type CodexWarm struct {
	User    string   `json:"user"`
	Windows []string `json:"windows"`
	Err     string   `json:"error,omitempty"`
}

// codexWarmer is the warm-up with what it reads and sends given, for tests;
// Claude's (claude_warmup.go) is one too.
type codexWarmer struct {
	path  string
	now   func() time.Time
	usage func(context.Context) map[string]SubscriptionQuota // by user
	send  func(ctx context.Context, user string) error
	// expect is the windows every account has, one its usage leaves out
	// taken as not started (Anthropic has none for an account never used).
	expect []QuotaWindow
	// warmed, when set, is told of an account a request went to, so what
	// it has left is read again rather than taken from before.
	warmed func(user string)
}

// warmNow reads each account's windows, the weekly ones or with which
// "all" the 5-hour ones too, sends a request to each account one of them
// has started over on — or whose 5-hour window isn't running at the time
// of day at ("06:00", "" none) — and keeps what it saw.
func (c codexWarmer) warmNow(ctx context.Context, which, at string) []CodexWarm {
	st := readWarmState(c.path)
	now := c.now()
	usage := c.usage(ctx)
	users := make([]string, 0, len(usage))
	for u := range usage {
		users = append(users, u)
	}
	slices.Sort(users)
	var out []CodexWarm
	changed := false
	for _, user := range users {
		q := usage[user]
		if q.Error != "" {
			continue // nothing known: what was seen stands
		}
		key := strings.ToLower(user)
		prev, next := st[key], map[string]warmWindow{}
		var due []string
		onReset, days := map[string]bool{}, map[string]string{}
		unstarted := map[string]bool{}
		for _, w := range withExpected(q.Windows, c.expect) {
			// the weekly windows on their reset while it is on, the 5-hour
			// ones with "all" and for the day's start
			short := w.Span < 24*time.Hour
			onItsReset := which == "all" || which != "" && !short
			if w.Span <= 0 || w.Aside || w.Model != "" || !onItsReset && !(short && at != "") {
				continue
			}
			cur := asOf(w, now)
			unstarted[w.Name] = idle(cur, now)
			p, seen := prev[w.Name]
			n := warmWindow{Used: cur.Used, Warmed: p.Warmed, Daily: p.Daily}
			if cur.ResetsAt != nil {
				n.ResetsAt = *cur.ResetsAt
			}
			if unstarted[w.Name] {
				n.Idle, n.Retry = p.Idle, p.Retry // not started yet: it waits on
			}
			reset := onItsReset && warmDue(p, seen, cur, now)
			held := reset && short && heldForDay(at, w.Span, now)
			day, daily := "", false
			if short {
				day, daily = dailyDue(at, p.Daily, cur, now)
			}
			if held && !daily {
				// it waits for the day's start, kept as it was till then
				reset = false
			}
			if reset || daily {
				due = append(due, w.Name)
				onReset[w.Name] = reset
				if daily {
					days[w.Name] = day
				}
			}
			if reset || held {
				// kept as it was until a request goes, so it is due again
				n = p
			}
			next[w.Name] = n
		}
		if len(due) > 0 {
			r := CodexWarm{User: user, Windows: due}
			err := c.send(ctx, user)
			for _, name := range due {
				n, p := next[name], prev[name]
				switch {
				case err == nil:
					n = windowSeen(q, name, now)
					n.Warmed, n.Daily = now, p.Daily
					if day, ok := days[name]; ok {
						n.Daily = day
					}
				case !onReset[name]:
					// the day's start only: tried again while it is due
				case p.Failed+1 >= warmTries: // given up on this reset
					n = windowSeen(q, name, now)
					n.Daily = p.Daily
				default:
					n.Pending, n.Failed = true, p.Failed+1
				}
				if onReset[name] && !n.Pending && unstarted[name] {
					// not started as far as is known: another later
					n.Idle = p.Idle + 1
					n.Retry = now.Add(min(warmRetry<<min(p.Idle, 8), warmRetryMax))
				}
				next[name] = n
			}
			if err != nil {
				r.Err = err.Error()
			} else if c.warmed != nil {
				c.warmed(user)
			}
			out = append(out, r)
		}
		if !mapsEqual(prev, next) {
			st[key], changed = next, true
		}
	}
	if changed {
		if b, err := json.MarshalIndent(st, "", "  "); err == nil {
			if err := writePrivate(c.path, append(b, '\n')); err != nil {
				log.Printf("codex warm-up: %v", err)
			}
		}
	}
	return out
}

// withExpected is ws with each of expect it leaves out, as not started.
func withExpected(ws, expect []QuotaWindow) []QuotaWindow {
	out := ws
	for _, e := range expect {
		if !slices.ContainsFunc(ws, func(w QuotaWindow) bool { return w.Name == e.Name }) {
			out = append(slices.Clip(out), QuotaWindow{Name: e.Name, Span: e.Span})
		}
	}
	return out
}

// windowSeen is window name of q as now, as warmNow keeps it; one q
// leaves out is not started.
func windowSeen(q SubscriptionQuota, name string, now time.Time) warmWindow {
	for _, w := range q.Windows {
		if w.Name == name {
			cur := asOf(w, now)
			n := warmWindow{Used: cur.Used}
			if cur.ResetsAt != nil {
				n.ResetsAt = *cur.ResetsAt
			}
			return n
		}
	}
	return warmWindow{}
}

func mapsEqual(a, b map[string]warmWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || !x.ResetsAt.Equal(y.ResetsAt) || x.Used != y.Used || !x.Warmed.Equal(y.Warmed) || x.Failed != y.Failed || x.Pending != y.Pending || x.Daily != y.Daily ||
			x.Idle != y.Idle || !x.Retry.Equal(y.Retry) {
			return false
		}
	}
	return true
}

// asOf is w as of now, its reset made absolute: a window read before a
// reset that has since passed (a cached read, or one kept through a
// failed fetch) has started over, from nothing.
func asOf(w QuotaWindow, now time.Time) QuotaWindow {
	if w.ResetsAt == nil && w.ResetSecs > 0 {
		t := now.Add(time.Duration(w.ResetSecs) * time.Second)
		w.ResetsAt = &t
	}
	if w.ResetsAt != nil && !now.Before(*w.ResetsAt) {
		w.Used, w.ResetsAt = 0, nil
	}
	return w
}

// warmDue says whether a window seen as p (seen false the first time) and
// now as cur wants starting: it is unused, and has started over since p —
// or is seen for the first time. Use falling back is a reset whatever the
// window says, OpenAI resetting everyone's limits early among them. One
// whose warm-up failed is still due, and one still not started is due
// again once its Retry comes, even with a reset time reported.
func warmDue(p warmWindow, seen bool, cur QuotaWindow, now time.Time) bool {
	switch {
	case seen && p.Pending:
		return true
	case !seen:
		return idle(cur, now)
	case p.Idle > 0 && idle(cur, now):
		return !now.Before(p.Retry)
	case p.Used-cur.Used >= 1:
		return true
	case !p.ResetsAt.IsZero() && !now.Before(p.ResetsAt):
		return idle(cur, now)
	case p.ResetsAt.IsZero() && p.Used == 0:
		return idle(cur, now) && !now.Before(p.Retry)
	}
	return false
}

// idle says whether a window (as of now) hasn't started: nothing used,
// and its reset a whole window away, as the backend reports one not yet
// begun — or not known.
func idle(w QuotaWindow, now time.Time) bool {
	return w.Used == 0 && (w.ResetsAt == nil || w.ResetsAt.Sub(now) >= w.Span-warmSlack)
}

// codexWarmUsage is each ChatGPT account's windows, the cached reads the
// Usage page and the switch to an account with room share; an account
// whose sign-in lapsed is left out.
func codexWarmUsage(ctx context.Context) map[string]SubscriptionQuota {
	u := LoginUsage(ctx, "codex")
	for _, l := range Logins("codex") {
		if l.Lapsed != "" {
			delete(u, l.User)
		}
	}
	return u
}

// warmCodexLogin sends the ChatGPT account user one tiny request, with its
// own sign-in: Codex's for the account Codex is on, logins.json's else.
func warmCodexLogin(ctx context.Context, user string) error {
	ls := Logins("codex")
	i := slices.IndexFunc(ls, func(l Login) bool { return strings.EqualFold(l.User, user) })
	if i < 0 {
		return fmt.Errorf("no ChatGPT account %q", user)
	}
	l := ls[i]
	token := func(ctx context.Context) (string, string, error) { return savedLoginToken(ctx, "codex", l.User) }
	if l.Active {
		token = func(ctx context.Context) (string, string, error) { return codexToken(ctx, codexAuthPath()) }
	}
	model, effort, err := warmModel(l.User)
	if err != nil {
		return err
	}
	return warmCodex(ctx, codexSign(token), model, effort)
}

// warmModel is the model a warm-up asks, and at what effort: the account's
// smallest (a mini) or else the first it lists, at low.
func warmModel(user string) (model, effort string, err error) {
	ms, _, ok := catalog.Live(accountModels("codex", user))
	if !ok {
		ms, _, _ = catalog.Live("codex")
	}
	if len(ms) == 0 {
		ms = catalog.Codex()
	}
	if len(ms) == 0 {
		return "", "", errors.New("no Codex model is known yet")
	}
	m := ms[0]
	if i := slices.IndexFunc(ms, func(m catalog.Model) bool { return strings.Contains(m.ID, "mini") }); i >= 0 {
		m = ms[i]
	}
	switch {
	case slices.Contains(m.Efforts, "low"):
		effort = "low"
	case len(m.Efforts) > 0:
		effort = m.Efforts[0]
	}
	return m.ID, effort, nil
}

// warmCodex sends one "hi" to the ChatGPT backend as a Codex account's
// requests go (codexBody: Codex's instructions, not stored), and reads the
// reply to its end.
func warmCodex(ctx context.Context, sign func(context.Context, *http.Request, []byte) error, model, effort string) error {
	in := map[string]any{"model": model, "input": "hi"}
	if effort != "" {
		in["reasoning"] = map[string]any{"effort": effort}
	}
	b, _ := json.Marshal(in)
	body := codexBody(b)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexBase+"/responses", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := sign(ctx, req, body); err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return err
}

// CodexWarmed is when magpie last started a window of each ChatGPT
// account, by user as kept (lower-cased).
func CodexWarmed() map[string]time.Time { return codexWarmedIn(codexWarmPath()) }

func codexWarmedIn(path string) map[string]time.Time {
	out := map[string]time.Time{}
	for user, ws := range readWarmState(path) {
		for _, w := range ws {
			if w.Warmed.After(out[user]) {
				out[user] = w.Warmed
			}
		}
	}
	return out
}

// KeepCodexWindowsWarm starts the ChatGPT accounts' windows as they reset,
// while settings say to, two minutes after it starts and every
// codexWarmEvery after that, until ctx ends.
func KeepCodexWindowsWarm(ctx context.Context) {
	w := codexWarmer{path: codexWarmPath(), now: time.Now, usage: codexWarmUsage, send: warmCodexLogin,
		warmed: func(user string) { StaleAllowance("codex", user) }}
	keepWarm(ctx, "codex", w, func() (string, string) { s := settings.Load(); return s.CodexWarmup, s.CodexWarmAt })
}

// keepWarm runs w while prefs (the settings: which windows on their
// reset, and the time of day to start the 5-hour one at) say to: two
// minutes after it starts and every codexWarmEvery after that, and as soon
// as the time of day comes, until ctx ends. The time is the wall clock's,
// looked at every minute: a machine that slept through it notices on
// waking. A usage read that finds an account's window not started — one
// never used, which routing leaves for last — has it run at once too, for
// each account at most every warmKickEvery.
func keepWarm(ctx context.Context, name string, w codexWarmer, prefs func() (which, at string)) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	kick := make(chan string, 16)
	defer onNotStarted(name, w.expect, func(user string) {
		select {
		case kick <- user:
		default: // enough are waiting
		}
	})()
	kicked := map[string]time.Time{}
	var last time.Time
	for {
		tick, now1 := false, false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick = true
		case user := <-kick:
			key, now := strings.ToLower(user), w.now().Round(0)
			if which, _ := prefs(); which == "" || !kicked[key].IsZero() && now.Sub(kicked[key]) < warmKickEvery {
				continue
			}
			kicked[key], now1 = now, true
		}
		// the wall clock, which goes on while the machine sleeps
		now := w.now().Round(0)
		if which, at := prefs(); (which != "" || at != "") && (now1 || last.IsZero() || now.Sub(last) >= codexWarmEvery || dayStartPassed(at, last, now)) {
			last = now
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			for _, r := range w.warmNow(c, which, at) {
				if r.Err != "" {
					log.Printf("%s warm-up: %s's %s window: %s", name, r.User, strings.Join(r.Windows, ", "), r.Err)
				} else {
					log.Printf("%s warm-up: started %s's %s window", name, r.User, strings.Join(r.Windows, ", "))
				}
			}
			cancel()
			// what its own read kicked, it has just looked at
			for len(kick) > 0 {
				<-kick
			}
		}
		if tick {
			t.Reset(time.Minute)
		}
	}
}

// notStartedHooks is, by agent, what a usage read finding an account's
// window not started tells, and the windows each account has.
var notStartedHooks struct {
	sync.Mutex
	m map[string]notStartedHook
}

type notStartedHook struct {
	expect []QuotaWindow
	f      func(user string)
}

// onNotStarted has f told of each of agent's accounts a usage read finds
// a window of not started, until the func it returns is called.
func onNotStarted(agent string, expect []QuotaWindow, f func(user string)) func() {
	h := &notStartedHooks
	h.Lock()
	if h.m == nil {
		h.m = map[string]notStartedHook{}
	}
	h.m[agent] = notStartedHook{expect, f}
	h.Unlock()
	return func() {
		h.Lock()
		delete(h.m, agent)
		h.Unlock()
	}
}

// usageRead tells onNotStarted's f of each account in usage, read for
// agent, with a window not started: nothing used and no reset known, or
// left out of the read.
func usageRead(agent string, usage map[string]SubscriptionQuota) {
	h := &notStartedHooks
	h.Lock()
	hook, ok := h.m[agent]
	h.Unlock()
	if !ok {
		return
	}
	for user, q := range usage {
		if q.Error == "" && slices.ContainsFunc(withExpected(q.Windows, hook.expect), notStarted) {
			hook.f(user)
		}
	}
}

// notStarted says whether w is a window that hasn't begun as far as is
// known: nothing used and no reset.
func notStarted(w QuotaWindow) bool {
	return w.Span > 0 && !w.Aside && w.Model == "" && w.Used == 0 && w.ResetsAt == nil && w.ResetSecs <= 0
}
