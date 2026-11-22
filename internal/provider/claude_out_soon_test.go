package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// claudeOutSoon sets up #4443's case: Claude kept to Claude Code
// (switch-only), Claude Code on pro@x.com, max@x.com on with all of its
// allowance left, and /usage answering, for the account Claude Code is on,
// the share set last (set). The account Claude Code is on is read once, at
// first, and doesn't move on. It answers the spare, set, and a func that
// ages every reading by d, as the minutes between two looks do.
func claudeOutSoon(t *testing.T, first string) (spare string, set func(string), age func(d time.Duration)) {
	t.Helper()
	claudeThree(t) // pro@x.com signed in; team and max saved
	var out atomic.Value
	out.Store("")
	fakeClaudeUsage(t, &out, nil)
	claudeUsageWait = func() time.Duration { return 10 * time.Minute } // fakeClaudeUsage puts it back
	forget := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		claudeLimitHit.Store(0)
		claudeTranscripts.Lock()
		claudeTranscripts.started, claudeTranscripts.off = false, nil
		claudeTranscripts.Unlock()
		claudeOn.Lock()
		claudeOn.user, claudeOn.since = "", time.Time{}
		claudeOn.Unlock()
	}
	forget()
	t.Cleanup(forget)
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	for _, l := range Logins("claude") {
		if strings.HasPrefix(l.User, "max@x.com") {
			spare = l.User
			if err := SetLoginOn("claude", l.User, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if spare == "" {
		t.Fatal("no max@x.com")
	}
	var mu sync.Mutex
	active := first
	set = func(s string) {
		mu.Lock()
		active = s
		mu.Unlock()
	}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		used := "0" // the spare
		if dir == "" {
			used = active
		}
		return "Current session: " + used + "% used · resets " + sessionReset(), nil
	})
	age = func(d time.Duration) {
		loginUsageCache.Lock()
		loginUsageCache.m = nil // the minute's cache gone
		loginUsageCache.Unlock()
		claudeUsage.Lock()
		for k, e := range claudeUsage.m {
			e.tried, e.at = e.tried.Add(-d), e.at.Add(-d)
			claudeUsage.m[k] = e
		}
		claudeUsage.Unlock()
	}
	if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != "" {
		t.Fatalf("at %s%%: switched to %q: %v", first, to, err)
	}
	return spare, set, age
}

// The account Claude Code is on read near out (#4443: 4% left at
// 12:16:15) is read again a minute later, not at its 5 to 15 minutes'
// wait, and the look a minute later that finds it spent moves Claude Code
// on.
func TestClaudeNearOutIsReadEveryMinute(t *testing.T) {
	spare, set, age := claudeOutSoon(t, "96")
	if !claudeLookDue() {
		t.Fatal("near out, but no look due before the next full one")
	}
	set("100")
	age(claudeLookEvery)
	if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != spare {
		t.Fatalf("a minute on, spent: switched to %q: %v, want %q", to, err, spare)
	}
}

// Claude Code refused for its limit ("You've hit your 5-hour limit",
// written to its session) while the reading magpie has says room: the
// next look reads /usage at once, whatever its wait, and moves on.
func TestClaudeRefusalInSessionReadsAtOnce(t *testing.T) {
	spare, set, age := claudeOutSoon(t, "50")
	session := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-work", "s1.jsonl")
	writeSession(t, session, `{"type":"user","message":{"role":"user","content":"go"},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	if claudeLookDue() {
		t.Fatal("a look due at 50% with no refusal")
	}
	set("100")
	age(2 * time.Minute)
	if claudeLookDue() {
		t.Fatal("a look due with no refusal written")
	}
	writeSession(t, session, claudeRefusalLine(time.Now()))
	if !claudeLookDue() {
		t.Fatal("Claude Code was refused, and no look is due")
	}
	if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != spare {
		t.Fatalf("after the refusal: switched to %q: %v, want %q", to, err, spare)
	}
}

// claudeUnread has the account Claude Code is on answer /usage with err
// from now on, as the owner's did on every look (11-22: "signal: killed",
// the run killed at its deadline), and ages the readings by d.
func claudeUnread(t *testing.T, err string, age func(time.Duration), d time.Duration) {
	t.Helper()
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir == "" {
			return "", errors.New(err)
		}
		return "Current session: 0% used · resets " + sessionReset(), nil
	})
	age(d)
}

// The owner's case (11-22): every /usage run of the account Claude Code
// is on killed at its deadline, its reading "signal: killed", and Claude
// Code stopped mid-task for its limit while every other account had room.
// The failed reading kept Claude Code on it for good; the refusal Claude
// Code wrote to its session moves it on.
func TestClaudeUnreadRefusedMovesOn(t *testing.T) {
	spare, _, age := claudeOutSoon(t, "50")
	session := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-work", "s1.jsonl")
	writeSession(t, session, `{"type":"user","message":{"role":"user","content":"go"},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	claudeLookDue() // the minute's look finds Claude Code on pro@x.com
	claudeUnread(t, "signal: killed", age, claudeLookEvery)
	if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != "" {
		t.Fatalf("unread with no refusal: switched to %q: %v", to, err)
	}
	writeSession(t, session, claudeRefusalLine(time.Now()))
	if !claudeLookDue() {
		t.Fatal("Claude Code was refused, and no look is due")
	}
	if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != spare {
		t.Fatalf("unread and refused: switched to %q: %v, want %q", to, err, spare)
	}
}

// Claude Code's own word that the account is out, as /usage or a run
// answers it ("You've hit your weekly limit", the owner's warm-up line of
// 11-22 whole), moves Claude Code on with no refusal since it is on the
// account; a refusal of the account itself (signed out) doesn't, nor a
// failed read with only a refusal from before Claude Code was on it.
func TestClaudeLimitSaidMovesOn(t *testing.T) {
	for _, c := range []struct {
		err   string
		moves bool
	}{
		{"Claude Code: You've hit your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)", true},
		{"Claude Code's /usage told no allowance: You've hit your session limit · resets 3:30am (UTC)", true},
		{"Claude Code: You've hit your Fable limit for this week. Switch models?", true},
		{"Claude Code: You've hit your limit · resets 5pm (UTC)", true},
		{"Claude Code's /usage told no allowance: HTTP 401 Unauthorized", false},
		{"signal: killed", false},
	} {
		t.Run(c.err, func(t *testing.T) {
			spare, _, age := claudeOutSoon(t, "50")
			session := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-work", "s1.jsonl")
			writeSession(t, session, `{"type":"user","message":{"role":"user","content":"go"},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
			claudeLookDue()
			// refused before Claude Code was on the account: another's
			writeSession(t, session, claudeRefusalLine(time.Now().Add(-time.Hour)))
			claudeLookDue()
			// past the reading's wait, Claude Code used since: /usage is run
			claudeUnread(t, c.err, age, 11*time.Minute)
			want := ""
			if c.moves {
				want = spare
			}
			if to, err := SwitchWhenSpent(context.Background(), "claude"); err != nil || to != want {
				t.Fatalf("switched to %q: %v, want %q", to, err, want)
			}
		})
	}
}

// What the scan of Claude Code's sessions counts: its own rate_limit
// message, written since the scan before, whole; not one from before
// magpie looked, a half-written line, nor the words in a tool's output.
func TestScanClaudeLimits(t *testing.T) {
	claudeHome(t)
	reset := func() {
		claudeLimitHit.Store(0)
		claudeTranscripts.Lock()
		claudeTranscripts.started, claudeTranscripts.off = false, nil
		claudeTranscripts.Unlock()
	}
	reset()
	t.Cleanup(reset)
	projects := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects")
	old := filepath.Join(projects, "-a", "old.jsonl")
	at := time.Now().Add(-time.Hour)
	writeSession(t, old, claudeRefusalLine(at))
	scanClaudeLimits()
	if claudeLimitHitAfter(time.Time{}) {
		t.Fatal("a refusal from before the first scan counted")
	}
	// a tool's output naming it, and a refusal half written
	writeSession(t, old, `{"type":"user","message":{"content":[{"type":"tool_result","content":"\"error\":\"rate_limit\""}]},"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	now := time.Now().Add(-time.Second)
	line := claudeRefusalLine(now)
	appendRaw(t, old, line[:40])
	scanClaudeLimits()
	if claudeLimitHitAfter(time.Time{}) {
		t.Fatal("a tool's output or a half-written line counted")
	}
	appendRaw(t, old, line[40:]+"\n")
	scanClaudeLimits()
	if !claudeLimitHitAfter(now.Add(-time.Millisecond)) || claudeLimitHitAfter(now) {
		t.Fatalf("the refusal finished since: hit at %v, want %v", time.Unix(0, claudeLimitHit.Load()), now)
	}
	// a session begun since the last scan is read from its start
	claudeLimitHit.Store(0)
	writeSession(t, filepath.Join(projects, "-b", "new.jsonl"), claudeRefusalLine(time.Now()))
	scanClaudeLimits()
	if !claudeLimitHitAfter(now) {
		t.Fatal("a refusal in a new session not counted")
	}
}

// claudeRefusalLine is what Claude Code writes to its session when
// Anthropic refuses the account for its limit, as a real session has it.
func claudeRefusalLine(at time.Time) string {
	return fmt.Sprintf(`{"parentUuid":"p","isSidechain":false,"type":"assistant","uuid":"u","timestamp":%q,"message":{"id":"m","container":null,"model":"<synthetic>","role":"assistant","stop_reason":"stop_sequence","stop_sequence":"","type":"message","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"You've hit your 5-hour limit · resets 5pm (UTC)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`,
		at.UTC().Format(time.RFC3339Nano))
}

// writeSession appends line, ended, to the session at path.
func writeSession(t *testing.T, path, line string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, path, line+"\n")
}

func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// The gateway's loop looks at Claude's sign-in between its five-minute
// looks while the account Claude Code is on is near out, and moves it on
// once a reading says it is spent: the account read at 96% at the first
// look, spent a minute on, is moved off before the next full look.
func TestKeepOnAnAccountWithRoomLooksAtClaudeNearOut(t *testing.T) {
	spare, set, age := claudeOutSoon(t, "96")
	oldFirst, oldEvery, oldLooked := firstSignInLook, claudeLookEvery, signInsLooked
	firstSignInLook, claudeLookEvery = time.Millisecond, 10*time.Millisecond
	fulls := make(chan struct{}, 1)
	signInsLooked = func(full bool) {
		if full {
			select {
			case fulls <- struct{}{}:
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepOnAnAccountWithRoom(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		firstSignInLook, claudeLookEvery, signInsLooked = oldFirst, oldEvery, oldLooked
	})
	// the full look done at 96%, the next is five minutes off
	select {
	case <-fulls:
	case <-time.After(time.Minute):
		t.Fatal("no full look")
	}
	if on := Logins("claude"); slices.ContainsFunc(on, func(l Login) bool { return l.Active && l.User == spare }) {
		t.Fatal("moved on at 96%")
	}
	set("100")
	age(claudeNearOutWait)
	on := func() string {
		for _, l := range Logins("claude") {
			if l.Active {
				return l.User
			}
		}
		return ""
	}
	deadline := time.Now().Add(time.Minute)
	for on() != spare {
		if time.Now().After(deadline) {
			t.Fatalf("Claude Code still on %q, want %q", on(), spare)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
