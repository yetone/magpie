package provider

// agy's account switched by itself when it runs low (#1908), and the
// check every switch of agy's sign-in makes first: nothing running on it.
//
// A switch writes the keyring entry agy and the Antigravity app read.
// One already running keeps the account it read at start, and renews that
// account's token about five minutes before it runs out, writing it back
// into the same entry: the switch is undone within the hour, and the
// account the user was moved off is spent on. So a switch, the user's or
// magpie's, is made only while no agy and no Antigravity app runs on agy's
// own sign-in; one run by the agy-cli plugin for another account (HOME in
// its own folder, SSH_CONNECTION set, so agy keeps that sign-in in a file
// there) never touches the entry and doesn't count.
//
// With "Switch by itself when an account runs low" on as well
// (Provider.AutoSwitch, off unless the user turns it on), magpie reads the
// account agy is on every loginSwitchEvery; when one of its quota pools
// (Gemini, Claude & GPT, …) is at the subscription's spent share, it
// signs agy in to the first other account whose same pools have room.
// Google's quota is per pool, each with its own windows, so an account
// past its line in one pool is not past it in another: only the pools
// that are spent need room on the next. While agy runs, the switch waits,
// looking at the processes every agyWaitEvery, and is made once they have
// ended, before the next agy starts as far as a look can tell. The
// account magpie moved off is not signed in again by itself: one used up,
// then the next.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// agyFamily and agyEnvOf are how agy's processes are listed and told
// apart; a test stands in for them.
var (
	agyFamily = proc.Family
	agyEnvOf  = proc.EnvOf
)

// agyWaitEvery is how often a switch waiting for agy to end looks again.
var agyWaitEvery = 10 * time.Second

// agyProcKind is "agy" for the agy CLI, "app" for the Antigravity app's
// main process, "" for anything else: its helpers (--type=…) and the
// language server it ships end with it.
func agyProcKind(args string) string {
	exe, rest := args, ""
	if strings.HasPrefix(exe, `"`) {
		if i := strings.Index(exe[1:], `"`); i >= 0 {
			exe, rest = exe[1:1+i], exe[2+i:]
		}
	} else if i := strings.IndexByte(exe, ' '); i >= 0 {
		exe, rest = exe[:i], exe[i:]
	}
	low := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	base := low[strings.LastIndexByte(low, '/')+1:]
	if base == "agy" || base == "agy.exe" {
		return "agy"
	}
	if strings.Contains(rest, "--type=") {
		return ""
	}
	// macOS: Antigravity.app/Contents/MacOS/…; Windows:
	// %LOCALAPPDATA%\Programs\antigravity\Antigravity.exe; Linux:
	// …/antigravity/antigravity
	if strings.Contains(low, "/antigravity.app/contents/macos/") ||
		base == "antigravity.exe" && strings.Contains(low, "/antigravity/") ||
		base == "antigravity" && strings.Contains(low, "/antigravity/") {
		return "app"
	}
	return ""
}

// AgyRunning is what runs on agy's own sign-in now: the Antigravity
// app, and agy CLIs by pid, the agy-cli plugin's own apart (Plugin: kept
// for a request served before Only switch was ticked, each ends after ten
// idle minutes). As a switch's refusal it names them for the user to
// quit; the GUI says the same in the reader's language (code
// agy_running).
type AgyRunning struct {
	App    bool
	PIDs   []int
	Plugin []int
}

func (r AgyRunning) none() bool { return !r.App && len(r.PIDs) == 0 && len(r.Plugin) == 0 }

// names is r as the user would quit it: "the Antigravity app",
// "agy (pid 123)".
func (r AgyRunning) names() []string {
	var out []string
	if r.App {
		out = append(out, "the Antigravity app")
	}
	for _, pid := range r.PIDs {
		out = append(out, fmt.Sprintf("agy (pid %d)", pid))
	}
	for _, pid := range r.Plugin {
		out = append(out, fmt.Sprintf("agy (pid %d, the Antigravity CLI plugin's, which ends by itself within ten minutes)", pid))
	}
	return out
}

func (r *AgyRunning) Error() string {
	return fmt.Sprintf("quit %s first: while it runs it keeps the account it started on, and writes it back over a switch; nothing was changed", strings.Join(r.names(), ", "))
}

// AgyUnlistedError is a switch refused because the processes couldn't be
// listed: whether agy runs is unknown.
type AgyUnlistedError struct{ Err error }

func (e *AgyUnlistedError) Error() string {
	return fmt.Sprintf("magpie couldn't tell whether agy or the Antigravity app is running (%v); nothing was changed", e.Err)
}

func (e *AgyUnlistedError) Unwrap() error { return e.Err }

// agyRunningOwn lists what runs on agy's own sign-in now (AgyRunning's
// names). A process whose environment can't be read is counted: it may
// be on the keyring. One magpie started (the agy-cli plugin's) is told by
// its environment too; on Windows, which doesn't tell it, one of
// magpie's is the plugin's, which runs agy on the keyring for a moment
// only to read its usage.
func agyRunningOwn(ctx context.Context) ([]string, error) {
	r, err := agyRunningNow(ctx)
	return r.names(), err
}

func agyRunningNow(ctx context.Context) (AgyRunning, error) {
	var r AgyRunning
	ps, err := agyFamily(ctx)
	if err != nil {
		return r, err
	}
	me := os.Getpid()
	parent := make(map[int]int, len(ps))
	for _, p := range ps {
		parent[p.PID] = p.PPID
	}
	mine := func(pid int) bool {
		for i := 0; i < 64 && pid > 1; i++ {
			if pid = parent[pid]; pid == me {
				return true
			}
		}
		return false
	}
	for _, p := range ps {
		if p.PID == me {
			continue
		}
		switch agyProcKind(p.Args) {
		case "app":
			r.App = true
		case "agy":
			env, err := agyEnvOf(ctx, p.PID, "SSH_CONNECTION")
			if err == nil && env["SSH_CONNECTION"] != "" {
				continue // its sign-in is in a file under its HOME
			}
			if !mine(p.PID) {
				r.PIDs = append(r.PIDs, p.PID)
			} else if !errors.Is(err, proc.ErrNoEnv) {
				r.Plugin = append(r.Plugin, p.PID)
			}
		}
	}
	return r, nil
}

// agyQuitFirst is a switch's refusal while something runs on agy's
// sign-in (*AgyRunning): nil when nothing does. A listing that fails is
// unknown, and refused (*AgyUnlistedError).
func agyQuitFirst(ctx context.Context) error {
	r, err := agyRunningNow(ctx)
	if err != nil {
		return &AgyUnlistedError{err}
	}
	if !r.none() {
		return &r
	}
	return nil
}

// agyPoolsPast is the pools of q, by name, with a window of their own at
// share or past at now ("" for a window of the whole account's), and
// whether q is spent outright: held, or at its usage cap.
func agyPoolsPast(q SubscriptionQuota, share float64, caps WindowCaps, now time.Time) (pools []string, all bool) {
	// a pool's window is the pool's alone, set aside by the plugin or not
	whole := q
	whole.Windows = slices.DeleteFunc(slices.Clone(q.Windows), func(w QuotaWindow) bool { return w.Pool != "" })
	if q.Held || capReached(q, caps, now) || usedPast(whole, share, now) {
		return nil, true
	}
	for _, w := range q.Windows {
		if w.Pool == "" || w.Model != "" || w.Family != "" || w.Text || w.Used < share || resetPassed(w, now) {
			continue
		}
		if !slices.Contains(pools, w.Pool) {
			pools = append(pools, w.Pool)
		}
	}
	return pools, false
}

// agyHasRoom says q has room in each of pools below share: read, not
// held, with a window of each pool's own, none at share.
func agyHasRoom(q SubscriptionQuota, pools []string, share float64, caps WindowCaps, now time.Time) bool {
	if q.Error != "" {
		return false
	}
	past, all := agyPoolsPast(q, share, caps, now)
	if all {
		return false
	}
	for _, pool := range pools {
		if slices.Contains(past, pool) || !slices.ContainsFunc(q.Windows, func(w QuotaWindow) bool { return w.Pool == pool && w.Model == "" && w.Family == "" }) {
			return false
		}
	}
	return true
}

// agyAutoSwitching says magpie switches agy's account by itself: Only
// switch agy's sign-in and Switch by itself are both on. Unread, it
// doesn't: the keyring is written only on the user's word.
func agyAutoSwitching() bool {
	p, err := Find(agyCLIID)
	return err == nil && p.IsPlugin() && p.SwitchOnly && p.AutoSwitch && !leftAlone(agyCLIID)
}

// nextAgyLogin is the agy-cli account agy should be signed in to instead
// of the one it is on (from), when that one is spent: the first other
// account that is on, signed in, and has room in every pool from is past
// its line in. ok is false when agy should stay.
func nextAgyLogin(ctx context.Context) (from, to string, ok bool) {
	if !agyAutoSwitching() {
		return "", "", false
	}
	var spares []string
	for _, l := range Logins(agyCLIID) {
		switch {
		case l.Active:
			from = l.User
		case l.On && l.Lapsed == "" && !l.Token && !l.AccessOnly:
			spares = append(spares, l.User)
		}
	}
	if from == "" || len(spares) == 0 {
		return "", "", false
	}
	share, _ := loginSwitching(agyCLIID)
	now := time.Now()
	// the one reading of agy's usage magpie makes (usageWithheld)
	ctx = agySwitchReading(ctx)
	q, known := someLoginUsage(ctx, agyCLIID, from)[from]
	if !known || q.Error != "" {
		return "", "", false
	}
	pools, all := agyPoolsPast(q, share, AccountCapsOf(agyCLIID, from), now)
	if !all && len(pools) == 0 {
		return "", "", false
	}
	// the spares only once the one agy is on is spent (someLoginUsage)
	u := someLoginUsage(ctx, agyCLIID, spares...)
	for _, user := range spares {
		if q, known := u[user]; known && agyHasRoom(q, pools, share, AccountCapsOf(agyCLIID, user), now) {
			return from, user, true
		}
	}
	return "", "", false
}

// agyPending is the switch a running agy holds back: from the account
// it is on to the one it goes to, zero for none.
var agyPending struct {
	sync.Mutex
	from, to string
	said     bool       // its wait was logged
	running  AgyRunning // what it waits for
}

// AgyWait is a switch magpie makes by itself that a running agy or
// Antigravity app holds back: from the account agy is on, which is spent,
// to the next with room, once what runs has quit. The editor and agy's
// Agents rows say it, so a switch that hasn't come is not a mystery.
type AgyWait struct {
	From   string `json:"from"`
	To     string `json:"to"`
	App    bool   `json:"app,omitempty"`
	PIDs   []int  `json:"pids,omitempty"`
	Plugin []int  `json:"plugin,omitempty"`
}

// AgyWaiting is the switch waiting now, nil for none, or once Switch
// automatically is off.
func AgyWaiting() *AgyWait {
	agyPending.Lock()
	w := AgyWait{From: agyPending.from, To: agyPending.to, App: agyPending.running.App, PIDs: slices.Clone(agyPending.running.PIDs), Plugin: slices.Clone(agyPending.running.Plugin)}
	agyPending.Unlock()
	if w.To == "" || !agyAutoSwitching() {
		return nil
	}
	return &w
}

// SwitchAgyWhenSpent signs agy in to the next agy-cli account with room
// when the one it is on is spent (nextAgyLogin), once nothing runs on
// agy's sign-in, and answers the account it signed agy in to, "" when it
// stayed. waiting says a switch is due and held back by a running agy or
// Antigravity app.
func SwitchAgyWhenSpent(ctx context.Context) (to string, waiting bool, err error) {
	from, to, ok := nextAgyLogin(ctx)
	if !ok {
		agyPending.Lock()
		agyPending.from, agyPending.to, agyPending.said, agyPending.running = "", "", false, AgyRunning{}
		agyPending.Unlock()
		return "", false, nil
	}
	return switchAgyWhenFree(ctx, from, to)
}

// switchAgyWhenFree switches agy from from to to, unless something runs
// on agy's sign-in: then it is kept as pending, for the next look.
func switchAgyWhenFree(ctx context.Context, from, to string) (string, bool, error) {
	err := switchLoginBy(agyCLIID, to, true)
	var busy *AgyRunning
	if errors.As(err, &busy) {
		agyPending.Lock()
		defer agyPending.Unlock()
		if !agyPending.said || !strings.EqualFold(agyPending.to, to) {
			log.Printf("agy: %s has used its allowance; it is signed in to %s once %s has quit", from, to, strings.Join(busy.names(), ", "))
		}
		agyPending.from, agyPending.to, agyPending.said, agyPending.running = from, to, true, *busy
		return "", true, nil
	}
	agyPending.Lock()
	agyPending.from, agyPending.to, agyPending.said, agyPending.running = "", "", false, AgyRunning{}
	agyPending.Unlock()
	if err != nil {
		return "", false, err
	}
	share, _ := loginSwitching(agyCLIID)
	log.Printf("agy: %s has used %g%% or more of its allowance; signed it in to %s", from, share, to)
	return to, false, nil
}

// KeepAgyOnAnAccountWithRoom is the look SwitchAgyWhenSpent makes, every
// loginSwitchEvery, and every agyWaitEvery while a switch waits for agy
// to end: then only the processes are listed, and the account agy is on
// is read again at the usual pace.
func KeepAgyOnAnAccountWithRoom(ctx context.Context) {
	t := time.NewTimer(firstSignInLook)
	defer t.Stop()
	var full time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		agyPending.Lock()
		pending := agyPending.to != ""
		agyPending.Unlock()
		c, cancel := context.WithTimeout(ctx, time.Minute)
		waiting := false
		var err error
		if due := !time.Now().Before(full); due || pending {
			// between full looks, only once agy has ended: then the look
			// is made again, the user's own switch since included
			if running, lerr := agyRunningNow(c); !due && (lerr != nil || !running.none()) {
				waiting = true
				if lerr == nil {
					agyPending.Lock()
					agyPending.running = running
					agyPending.Unlock()
				}
			} else {
				if due {
					full = time.Now().Add(loginSwitchEvery)
				}
				_, waiting, err = SwitchAgyWhenSpent(c)
			}
		}
		cancel()
		if err != nil {
			log.Printf("agy: switching to an account with room: %v", err)
		}
		next := time.Until(full)
		if waiting {
			next = min(next, agyWaitEvery)
		}
		t.Reset(max(next, time.Second))
	}
}
