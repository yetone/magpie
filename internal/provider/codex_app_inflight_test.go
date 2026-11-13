package provider

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// codexRollout writes a Codex session file under home's sessions: a turn
// started at at, then out bytes of a tool's output, as Codex 0.164 writes
// them (payloads cut short), and its end when done.
func codexRollout(t *testing.T, home string, at time.Time, out int, done bool) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := func(at time.Time) string { return `{"timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `",` }
	var b strings.Builder
	b.WriteString(stamp(at.Add(-time.Hour)) + `"type":"event_msg","payload":{"type":"task_started","turn_id":"0"}}` + "\n")
	b.WriteString(stamp(at.Add(-time.Hour)) + `"type":"event_msg","payload":{"type":"task_complete","turn_id":"0"}}` + "\n")
	b.WriteString(stamp(at) + `"type":"event_msg","payload":{"type":"task_started","turn_id":"1","model_context_window":258400}}` + "\n")
	line := stamp(at) + `"type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":"` + strings.Repeat("x", 4000) + `"}}` + "\n"
	for b.Len() < out {
		b.WriteString(line)
	}
	if done {
		b.WriteString(stamp(at) + `"type":"event_msg","payload":{"type":"task_complete","turn_id":"1"}}` + "\n")
	}
	p := filepath.Join(dir, "rollout-2026-11-13T10-00-00-norethcy.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A turn that has written more than the 512 KB sessionTurnOpen read of
// its session file's end is still running: its start is read further
// back. Only the tail was read, and such a turn (a tool's long output, an
// image, an hour of work: the owner's own run 22 MB) counted as none, so
// the Codex app's app-server was ended under it at the first gap of
// codexAppIdle between two of its requests (Norethcy on X).
func TestCodexTurnOpenPastTheTail(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	since := now.Add(-time.Minute)
	for _, size := range []int{sessionTurnTail + 1, 3*sessionTurnTail + 12345, 22 << 20} {
		codexRollout(t, home, now, size, false)
		if !codexTurnOpen(home, now, time.Time{}) {
			t.Fatalf("a turn %d bytes long, running: not open", size)
		}
		if !codexTurnOpen(home, now, since) {
			t.Fatalf("a turn %d bytes long started after the app-servers: not open", size)
		}
		if codexTurnOpen(home, now, now.Add(time.Hour)) {
			t.Fatalf("a turn %d bytes long started before the app-servers: open", size)
		}
		codexRollout(t, home, now, size, true)
		if codexTurnOpen(home, now, time.Time{}) {
			t.Fatalf("a turn %d bytes long, ended: open", size)
		}
	}
}

// A turn's start or end that straddles two of sessionTurnOpen's reads is
// found whole.
func TestCodexTurnOpenMarkAcrossReads(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := filepath.Join(dir, "rollout-straddle.jsonl")
	for _, mark := range []string{"task_started", "task_complete", "turn_aborted"} {
		head := `{"timestamp":"` + now.UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"0"}}` + "\n"
		line := `{"timestamp":"` + now.UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"` + mark + `","turn_id":"1"}}` + "\n"
		at := strings.Index(line, `"type":"`+mark+`"`)
		for cut := 1; cut < len(`"type":"`+mark+`"`); cut++ {
			// the mark begins cut bytes before the last read's start
			tail := sessionTurnTail + cut - (len(line) - at)
			body := head + line + strings.Repeat(" ", tail)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, want := codexTurnOpen(home, now, time.Time{}), mark == "task_started"; got != want {
				t.Fatalf("%s %d bytes into the read before: open %v, want %v", mark, cut, got, want)
			}
		}
	}
}

// Norethcy on X (v0.1.4045): the Codex app signed in to ChatGPT, sent
// through a routing group. The account ran out mid-conversation, the
// group's fallback answered, and magpie restarted the app's app-server
// while that turn went on, which cut it off. The turn's session file had
// grown past what was read of it, and a tool ran between two of its
// requests: magpie took the gap for the turn's end. And a turn that went
// on through magpie after the account ran out is served, not stuck on
// it: a tool running over codexTurnStuckAfter in it doesn't let the
// app-server be ended under it. The app-server is restarted once the
// turn ends.
func TestCodexAppServerWaitsForATurnTheFallbackServes(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	home := codexHome()
	start := time.Now()
	oldTurn, oldStart := codexTurnRunning, codexServerStart
	t.Cleanup(func() { codexTurnRunning, codexServerStart = oldTurn, oldStart })
	codexTurnRunning = func(home string, now, since time.Time) bool { return codexTurnOpen(home, now, since) }
	// the app's app-server started before the turn
	codexServerStart = func(int) (time.Time, error) { return start.Add(-time.Hour), nil }

	s.running = []int{60}
	step := func(at time.Duration, inFlight int, last time.Duration) []int {
		return codexAppStep(context.Background(), start.Add(at), func() (int, time.Time) {
			return inFlight, start.Add(last)
		})
	}
	codexRollout(t, home, start, 1<<10, false)
	if got := step(0, 1, -time.Minute); got != nil {
		t.Fatalf("on the account with room: ended %v", got)
	}
	// the account runs out: the group's fallback streams the reply, and
	// magpie becomes Codex's provider for the account
	s.on = true
	if got := step(3*time.Second, 1, -time.Minute); got != nil {
		t.Fatalf("the fallback's reply streaming: ended %v", got)
	}
	// the reply has ended, and a tool runs; the turn has written 600 KB
	codexRollout(t, home, start, 600<<10, false)
	if got := step(30*time.Second, 0, 15*time.Second); got != nil {
		t.Fatalf("a tool running in a turn past 512 KB, 15s after the fallback's reply: ended %v", got)
	}
	// the tool runs on, past codexTurnStuckAfter, and writes nothing
	s.written = false
	if got := step(30*time.Second+codexTurnStuckAfter+time.Second, 0, 15*time.Second); got != nil {
		t.Fatalf("a tool running over %s in a turn the fallback served: ended %v", codexTurnStuckAfter, got)
	}
	// the turn ends
	codexRollout(t, home, start, 600<<10, true)
	if got := step(5*time.Minute, 0, 4*time.Minute); !slices.Equal(got, []int{60}) {
		t.Fatalf("the turn ended: ended %v, want [60]", got)
	}
}

// A request Codex begins while the step looked for an open turn and moved
// the open chats (CodexBeforeAppEnd) holds the end to the next look: the
// in-flight count read before them is out of date by then.
func TestCodexAppServerEndLooksAtRequestsAgain(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	old := CodexBeforeAppEnd
	t.Cleanup(func() { CodexBeforeAppEnd = old })
	now := time.Now()
	s.running = []int{60}
	inFlight := 0
	busy := func() (int, time.Time) { return inFlight, now.Add(-time.Minute) }
	codexAppStep(context.Background(), now, busy)
	CodexBeforeAppEnd = func(string) { inFlight = 1 } // a request begins meanwhile
	s.route = "elsewhere"
	if got := codexAppStep(context.Background(), now, busy); got != nil {
		t.Fatalf("a request began before the end: ended %v", got)
	}
	CodexBeforeAppEnd = nil
	inFlight = 0
	if got := codexAppStep(context.Background(), now.Add(time.Minute), func() (int, time.Time) { return 0, now }); !slices.Equal(got, []int{60}) {
		t.Fatalf("idle again: ended %v, want [60]", got)
	}
}

// When no app-server is listed in place of the one ended, a Codex request
// through magpie since says the app has one all the same (a listing that
// missed it): the app is never quit and opened under that request.
func TestCodexAppNotReopenedWhileItSends(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	reopened := 0
	CodexAppReopen = func(context.Context) error { reopened++; return nil }
	start := time.Now()
	s.running, s.noNext = []int{60}, true
	idle := func() (int, time.Time) { return 0, start.Add(-time.Minute) }
	codexAppStep(context.Background(), start, idle)
	s.route = "elsewhere"
	if got := codexAppStep(context.Background(), start, idle); !slices.Equal(got, []int{60}) {
		t.Fatalf("ended %v, want [60]", got)
	}
	sending := func() (int, time.Time) { return 1, start.Add(time.Second) }
	codexAppStep(context.Background(), start.Add(codexAppBackWait), sending)
	codexAppStep(context.Background(), start.Add(2*codexAppBackWait), sending)
	if reopened != 0 {
		t.Fatalf("the app reopened %d times while it sent through magpie", reopened)
	}
}

// A magpie that took the gateway over from another (an update) saw none
// of the requests the one before still serves: its start counts as a
// Codex request ended then, so its first look doesn't end the app's
// app-server under a reply still streaming from the old one.
func TestCodexAppStartCountsAsARequest(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	old := codexAppEvery
	t.Cleanup(func() { codexAppEvery = old })
	codexAppEvery = time.Millisecond
	s.running, s.on = []int{60}, true
	ended := make(chan int, 1)
	endCodexAppServer = func(pid int) error {
		select {
		case ended <- pid:
		default:
		}
		return nil
	}
	looked := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepCodexAppOnItsProvider(ctx, func() (int, time.Time) {
			select {
			case looked <- struct{}{}:
			default:
			}
			return 0, time.Time{} // none served by this magpie
		})
	}()
	defer func() { cancel(); <-done }()
	// five looks at the requests, all within codexAppIdle of the start
	for n := 0; n < 5; {
		select {
		case <-looked:
			n++
		case pid := <-ended:
			t.Fatalf("ended %d within codexAppIdle of magpie's start", pid)
		}
	}
}
