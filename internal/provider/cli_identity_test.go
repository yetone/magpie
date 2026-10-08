package provider

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLIIdentityKeptAcrossStarts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var asked atomic.Int32
	answer := func() (string, string, bool, error) { asked.Add(1); return "me@example.com", "Pro", true, nil }
	exe := func() string { return "/bin/sh" }

	// never asked before: the first look waits for the CLI, and keeps it
	first := &cliIdentity{name: "x", exe: exe, ask: answer}
	if u, p, ok := first.get(); !ok || u != "me@example.com" || p != "Pro" || asked.Load() != 1 {
		t.Fatalf("first: %q %q %v, asked %d", u, p, ok, asked.Load())
	}
	if _, err := os.Stat(identityPath()); err != nil {
		t.Fatal(err)
	}

	// a magpie started again serves the kept one at once, however long the
	// CLI takes, and asks it behind that
	release := make(chan struct{})
	slow := func() (string, string, bool, error) { <-release; return "other@example.com", "Ultra", true, nil }
	again := &cliIdentity{name: "x", exe: exe, ask: slow}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if u, p, ok := again.get(); !ok || u != "me@example.com" || p != "Pro" {
			t.Errorf("kept: %q %q %v", u, p, ok)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a start with a kept answer waited for the CLI")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if u, _, _ := again.get(); u == "other@example.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer behind the kept one never came")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if k := readIdentities()["x"]; k.User != "other@example.com" || k.Plan != "Ultra" {
		t.Fatalf("kept after the refresh: %+v", k)
	}

	// after a sign-in or out, the next look waits for the CLI again
	again.forget()
	again.ask = func() (string, string, bool, error) { return "", "", false, nil }
	if _, _, ok := again.get(); ok {
		t.Fatal("forget served the old answer")
	}
	if k := readIdentities()["x"]; k.OK {
		t.Fatalf("signed out, still kept: %+v", k)
	}
}

func TestCLIIdentityKeptIgnoredWithoutTheCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&cliIdentity{name: "x", exe: func() string { return "/bin/sh" }, ask: func() (string, string, bool, error) { return "me@example.com", "", true, nil }}).get()
	gone := &cliIdentity{name: "x", exe: func() string { return "" }, ask: func() (string, string, bool, error) { return "", "", false, nil }}
	if u, _, ok := gone.get(); ok || u != "" {
		t.Fatalf("a removed CLI still served %q", u)
	}
}

// a CLI never answered before that takes long holds a look only so long:
// nobody is signed in until it answers, and then its answer is served
func TestCLIIdentityFirstAskBounded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := firstAsk
	firstAsk = 100 * time.Millisecond
	t.Cleanup(func() { firstAsk = old })
	release := make(chan struct{})
	c := &cliIdentity{name: "x", exe: func() string { return "/bin/sh" }, ask: func() (string, string, bool, error) { <-release; return "me@example.com", "Pro", true, nil }}
	start := time.Now()
	if _, _, ok := c.get(); ok {
		t.Fatal("an unanswered CLI served someone")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the first look waited %v", d)
	}
	// a request looks again: that one doesn't wait
	start = time.Now()
	c.get()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("a second look waited %v", d)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if u, _, ok := c.get(); ok && u == "me@example.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer never came")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if k := readIdentities()["x"]; k.User != "me@example.com" {
		t.Fatalf("not kept: %+v", k)
	}
}

// a CLI saying nobody is signed in is kept too: the next start serves that
// rather than waiting for it again
func TestCLIIdentitySignedOutKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&cliIdentity{name: "x", exe: func() string { return "/bin/sh" }, ask: func() (string, string, bool, error) { return "", "", false, nil }}).get()
	if _, found := readIdentities()["x"]; !found {
		t.Fatal("a signed-out answer wasn't kept")
	}
}

// a CLI that keeps failing to answer, or answers only after a long while,
// is asked again after a minute, then two, four… not every minute: a
// cursor-agent calling itself ran ten seconds and hundreds of processes
// each time, and with no token kept answered nobody all the same (#1278).
// A quick answer has it asked every minute again.
func TestCLIIdentityFailingAskedLessOften(t *testing.T) {
	old := slowAsk
	slowAsk = 50 * time.Millisecond
	t.Cleanup(func() { slowAsk = old })
	for _, bad := range []struct {
		name string
		ask  func() (string, string, bool, error)
	}{
		{"fails", func() (string, string, bool, error) { return "", "", false, errors.New("signal: killed") }},
		{"slow", func() (string, string, bool, error) { time.Sleep(60 * time.Millisecond); return "", "", false, nil }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var asked atomic.Int32
			var good atomic.Bool
			c := &cliIdentity{name: "x", exe: func() string { return "/bin/sh" }, ask: func() (string, string, bool, error) {
				asked.Add(1)
				if good.Load() {
					return "me@example.com", "", true, nil
				}
				return bad.ask()
			}}
			// look as if the last ask was ago, and wait for any ask it starts
			look := func(ago time.Duration) int32 {
				c.Lock()
				c.at = time.Now().Add(-ago)
				c.Unlock()
				c.get()
				c.Lock()
				done, busy := c.done, c.refreshing
				c.Unlock()
				if busy {
					<-done
				}
				return asked.Load()
			}
			if c.get(); asked.Load() != 1 { // never answered: asked
				t.Fatalf("first look asked %d times", asked.Load())
			}
			if n := look(61 * time.Second); n != 2 { // a minute after the first
				t.Fatalf("a minute after the first: asked %d times", n)
			}
			if n := look(61 * time.Second); n != 2 {
				t.Fatal("asked again a minute after the second")
			}
			if n := look(121 * time.Second); n != 3 {
				t.Fatalf("two minutes after the second: asked %d times", n)
			}
			for i := 0; i < 10; i++ { // never longer than slowestAsk
				if n := look(slowestAsk + time.Second); n != int32(4+i) {
					t.Fatalf("after %v: asked %d times, want %d", slowestAsk, n, 4+i)
				}
			}
			good.Store(true)
			if n := look(slowestAsk + time.Second); n != 14 {
				t.Fatalf("asked %d times", n)
			}
			if n := look(61 * time.Second); n != 15 {
				t.Fatal("after a quick answer, not asked again a minute later")
			}
			if u, _, ok := c.get(); !ok || u != "me@example.com" {
				t.Fatalf("served %q %v", u, ok)
			}
		})
	}
}
