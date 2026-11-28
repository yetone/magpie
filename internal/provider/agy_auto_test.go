package provider

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// agyRunning has agyRunningOwn see ps, with env as each one's
// SSH_CONNECTION (absent: none set; envErr: unreadable), never this
// machine's processes.
func agyRunning(t *testing.T, ps ...proc.Kin) (env map[int]string, envErr map[int]error) {
	t.Helper()
	env, envErr = map[int]string{}, map[int]error{}
	oldF, oldE := agyFamily, agyEnvOf
	agyFamily = func(context.Context) ([]proc.Kin, error) { return ps, nil }
	agyEnvOf = func(_ context.Context, pid int, _ ...string) (map[string]string, error) {
		if err := envErr[pid]; err != nil {
			return nil, err
		}
		return map[string]string{"SSH_CONNECTION": env[pid]}, nil
	}
	t.Cleanup(func() { agyFamily, agyEnvOf = oldF, oldE })
	return env, envErr
}

func TestAgyProcKind(t *testing.T) {
	for args, want := range map[string]string{
		"/Users/u/.local/bin/agy acp --agent x": "agy",
		"agy":                                   "agy",
		`"C:\Users\u\AppData\Local\agy\agy.exe" acp`:                                          "agy",
		"/Applications/Antigravity.app/Contents/MacOS/Electron":                               "app",
		"/Applications/Antigravity.app/Contents/MacOS/Antigravity --type=renderer":            "",
		"/Applications/Antigravity.app/Contents/Frameworks/Antigravity Helper.app/Contents/x": "",
		`"C:\Users\u\AppData\Local\Programs\antigravity\Antigravity.exe"`:                     "app",
		`"C:\Users\u\AppData\Local\Programs\antigravity\Antigravity.exe" --type=gpu-process`:  "",
		"/usr/share/antigravity/antigravity --no-sandbox":                                     "app",
		"/usr/bin/agyctl":     "",
		"/bin/zsh -c agy acp": "",
		"/Applications/Antigravity.app/Contents/Resources/bin/language_server_macos_arm --x": "",
	} {
		if got := agyProcKind(args); got != want {
			t.Errorf("agyProcKind(%q) = %q, want %q", args, got, want)
		}
	}
}

// What runs on agy's own sign-in is the app's main process and every agy
// that isn't the plugin's for another account: SSH_CONNECTION set says
// that one keeps its sign-in in a file. One whose environment is unknown
// counts, unless it is magpie's own on a system that tells no
// environment.
func TestAgyRunningOwn(t *testing.T) {
	me := os.Getpid()
	env, envErr := agyRunning(t,
		proc.Kin{PID: me, PPID: 1, Args: "/x/magpie serve"},
		proc.Kin{PID: 10, PPID: 1, Args: "/Applications/Antigravity.app/Contents/MacOS/Electron"},
		proc.Kin{PID: 11, PPID: 10, Args: "/Applications/Antigravity.app/Contents/MacOS/Electron --type=renderer"},
		proc.Kin{PID: 20, PPID: 1, Args: "/u/bin/agy chat"},            // the user's
		proc.Kin{PID: 30, PPID: 31, Args: "/u/bin/agy acp"},            // the plugin's, another account
		proc.Kin{PID: 31, PPID: me, Args: "/u/bun host.js"},            //
		proc.Kin{PID: 40, PPID: 31, Args: "/u/bin/agy acp"},            // the plugin's, unknown env (Windows)
		proc.Kin{PID: 50, PPID: 1, Args: "/u/bin/agy acp"},             // someone's, unknown env
		proc.Kin{PID: 60, PPID: 31, Args: "/u/bin/agy usage"},          // the plugin's, on the keyring
		proc.Kin{PID: 70, PPID: 1, Args: "/usr/bin/vim agy_switch.go"}, //
	)
	env[30] = "127.0.0.1 0 127.0.0.1 0"
	envErr[40] = proc.ErrNoEnv
	envErr[50] = proc.ErrNoEnv
	got, err := agyRunningOwn(context.Background())
	want := "the Antigravity app|agy (pid 20)|agy (pid 50)|agy (pid 60, the Antigravity CLI plugin's, which ends by itself within ten minutes)"
	if err != nil || strings.Join(got, "|") != want {
		t.Fatalf("agyRunningOwn = %q, %v\nwant %q", got, err, want)
	}
}

// A switch, the user's own as well, is refused while agy or the app runs
// on agy's sign-in: it would keep its account and write it back. Nothing
// is written; and an unreadable process list refuses too.
func TestAgySwitchRefusedWhileAgyRuns(t *testing.T) {
	w := agyWorld(t, true)
	agyRunning(t, proc.Kin{PID: 20, PPID: 1, Args: "/u/bin/agy chat"})
	err := SwitchLogin(agyCLIID, "b@x.com")
	if err == nil || !strings.Contains(err.Error(), "quit agy (pid 20) first") {
		t.Fatalf("SwitchLogin = %v", err)
	}
	if len(w.kr.writes) != 0 || agyFirst(t) != "a@x.com" {
		t.Fatalf("written %d times, first %q", len(w.kr.writes), agyFirst(t))
	}
	agyFamily = func(context.Context) ([]proc.Kin, error) { return nil, errors.New("ps: not found") }
	if err := SwitchLogin(agyCLIID, "b@x.com"); err == nil || !strings.Contains(err.Error(), "couldn't tell") {
		t.Fatalf("SwitchLogin with no process list = %v", err)
	}
	if len(w.kr.writes) != 0 {
		t.Fatal("the keyring was written")
	}
	agyRunning(t)
	if err := SwitchLogin(agyCLIID, "b@x.com"); err != nil || agyFirst(t) != "b@x.com" {
		t.Fatalf("SwitchLogin with nothing running = %v, first %q", err, agyFirst(t))
	}
}

func agyPoolWindow(pool string, used float64, resets time.Time) QuotaWindow {
	return QuotaWindow{Name: pool, Pool: pool, Aside: true, Used: used, ResetsAt: &resets}
}

// agyUsage stands in for the agy-cli accounts' readings, counting what
// each read asked for.
func agyUsage(t *testing.T, by map[string]SubscriptionQuota) *[]string {
	t.Helper()
	var asked []string
	LoginUsageVia(func(_ context.Context, agent string) map[string]SubscriptionQuota {
		if agent != agyCLIID {
			return nil
		}
		asked = append(asked, "read")
		return by
	})
	t.Cleanup(func() { LoginUsageVia(nil) })
	return &asked
}

// With Switch by itself on, agy is signed in to the next account when a
// pool of the one it is on is at the spent share, and the next has room
// in that pool; another pool's room doesn't count, nor does the next
// account's room when the one agy is on isn't spent. Off, the default,
// nothing is switched.
func TestAgySwitchesByItselfWhenSpent(t *testing.T) {
	soon := time.Now().Add(3 * time.Hour)
	cases := []struct {
		name string
		auto bool
		a, b SubscriptionQuota
		to   string
	}{
		{"off by default", false,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, soon)}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 5, soon)}}, ""},
		{"a's Gemini spent, b has room", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 99, soon), agyPoolWindow("Claude", 2, soon)}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 5, soon), agyPoolWindow("Claude", 99, soon)}}, "b@x.com"},
		{"a's Gemini spent, b's too", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, soon)}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 98, soon), agyPoolWindow("Claude", 0, soon)}}, ""},
		{"a's Gemini window has reset", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, time.Now().Add(-time.Minute))}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}}, ""},
		{"a has room", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 60, soon)}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}}, ""},
		{"a held, b has room", true,
			SubscriptionQuota{Held: true},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}}, "b@x.com"},
		{"b unread", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, soon)}},
			SubscriptionQuota{Error: "agy didn't answer"}, ""},
		{"a's Gemini spent, b's Claude pool not set aside", true,
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, soon)}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon), {Name: "Claude", Pool: "Claude", Used: 100, ResetsAt: &soon}}}, "b@x.com"},
		{"a's whole account spent", true,
			SubscriptionQuota{Windows: []QuotaWindow{{Name: "Daily", Used: 100, ResetsAt: &soon}}},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}}, "b@x.com"},
		{"a unread", true,
			SubscriptionQuota{Error: "agy didn't answer"},
			SubscriptionQuota{Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := agyWorld(t, true)
			if err := SetAutoSwitch(agyCLIID, c.auto); err != nil {
				t.Fatal(err)
			}
			agyUsage(t, map[string]SubscriptionQuota{"a@x.com": c.a, "b@x.com": c.b})
			to, waiting, err := SwitchAgyWhenSpent(context.Background())
			if err != nil || waiting || to != c.to {
				t.Fatalf("SwitchAgyWhenSpent = %q, %v, %v; want %q", to, waiting, err, c.to)
			}
			want := "a@x.com"
			if c.to != "" {
				want = c.to
			}
			if got := agyFirst(t); got != want {
				t.Fatalf("agy is on %q", got)
			}
			if c.to == "" && len(w.kr.writes) != 0 {
				t.Fatal("the keyring was written")
			}
		})
	}
}

// A switch due while agy runs waits, writing nothing, and is made once
// it has quit.
func TestAgyAutoSwitchWaitsForAgy(t *testing.T) {
	w := agyWorld(t, true)
	if err := SetAutoSwitch(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Hour)
	agyUsage(t, map[string]SubscriptionQuota{
		"a@x.com": {Windows: []QuotaWindow{agyPoolWindow("Gemini", 100, soon)}},
		"b@x.com": {Windows: []QuotaWindow{agyPoolWindow("Gemini", 0, soon)}},
	})
	agyRunning(t, proc.Kin{PID: 10, PPID: 1, Args: "/Applications/Antigravity.app/Contents/MacOS/Electron"})
	to, waiting, err := SwitchAgyWhenSpent(context.Background())
	if err != nil || !waiting || to != "" || len(w.kr.writes) != 0 {
		t.Fatalf("SwitchAgyWhenSpent with the app open = %q, %v, %v; %d writes", to, waiting, err, len(w.kr.writes))
	}
	// the editor says what it waits for, and what now runs: the app was
	// quit and agy started
	if got := AgyWaiting(); got == nil || got.From != "a@x.com" || got.To != "b@x.com" || !got.App || len(got.PIDs) != 0 {
		t.Fatalf("AgyWaiting with the app open = %+v", got)
	}
	agyRunning(t, proc.Kin{PID: 20, PPID: 1, Args: "/u/bin/agy chat"})
	if _, waiting, _ := SwitchAgyWhenSpent(context.Background()); !waiting {
		t.Fatal("not waiting for agy")
	}
	if got := AgyWaiting(); got == nil || got.App || !slices.Equal(got.PIDs, []int{20}) {
		t.Fatalf("AgyWaiting with agy running = %+v", got)
	}
	if err := SetAutoSwitch(agyCLIID, false); err != nil || AgyWaiting() != nil {
		t.Fatalf("AgyWaiting with Switch automatically off = %+v (%v)", AgyWaiting(), err)
	}
	if err := SetAutoSwitch(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	agyRunning(t)
	to, waiting, err = SwitchAgyWhenSpent(context.Background())
	if err != nil || waiting || to != "b@x.com" || agyFirst(t) != "b@x.com" {
		t.Fatalf("SwitchAgyWhenSpent once it quit = %q, %v, %v; first %q", to, waiting, err, agyFirst(t))
	}
	if got := AgyWaiting(); got != nil {
		t.Fatalf("AgyWaiting after the switch = %+v", got)
	}
}

// Switch by itself is agy-cli's, and only with Only switch agy's sign-in.
func TestSetAutoSwitchNeedsSwitchOnly(t *testing.T) {
	agyWorld(t, true)
	if err := SetSwitchOnly(agyCLIID, false); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoSwitch(agyCLIID, true); err == nil || !strings.Contains(err.Error(), "first") {
		t.Fatalf("SetAutoSwitch without switch-only = %v", err)
	}
	if err := SetSwitchOnly(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoSwitch(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(agyCLIID); !p.AutoSwitch {
		t.Fatal("AutoSwitch not kept")
	}
	if !agyAutoSwitching() {
		t.Fatal("agyAutoSwitching is false")
	}
	// unticking switch-only turns it off; ticking again leaves it off
	if err := SetSwitchOnly(agyCLIID, false); err != nil {
		t.Fatal(err)
	}
	if err := SetSwitchOnly(agyCLIID, true); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(agyCLIID); p.AutoSwitch || agyAutoSwitching() {
		t.Fatal("AutoSwitch came back with switch-only")
	}
}
