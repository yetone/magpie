package library

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// MasterYi on Discord: a server the router runs was ended after ten
// minutes unused, and a browser MCP asked again to allow the new
// connection each time. KeepAlive keeps it while magpie runs, and
// IdleTimeout sets its own wait; both from the CLI's idle= and the editor.
func TestServerIdleSettings(t *testing.T) {
	for _, c := range []struct {
		words []string
		keep  bool
		secs  int
		bad   bool
	}{
		{[]string{"idle=never"}, true, 0, false},
		{[]string{"keepalive=on"}, false, 0, true},
		{[]string{"idle=120"}, false, 120, false},
		{[]string{"idle=0"}, false, 0, false},
		{[]string{"idle=-5"}, false, 0, true},
		{[]string{"idle=soon"}, false, 0, true},
		{[]string{"idle=999999"}, false, 0, true},
	} {
		s := Server{Name: "pw", Transport: "stdio", Command: "npx", KeepAlive: true, IdleTimeout: 7}
		cmd, o, err := SplitServerWords(c.words)
		if err == nil {
			err = o.Apply(&s)
		}
		if c.bad {
			if err == nil {
				t.Errorf("%v: no error", c.words)
			}
			continue
		}
		if err != nil || len(cmd) > 0 {
			t.Errorf("%v: %v %v", c.words, cmd, err)
			continue
		}
		if s.KeepAlive != c.keep || s.IdleTimeout != c.secs {
			t.Errorf("%v: keepAlive %v idleTimeout %d", c.words, s.KeepAlive, s.IdleTimeout)
		}
		if err := s.check(); err != nil {
			t.Errorf("%v: %v", c.words, err)
		}
	}
	bad := Server{Name: "pw", Transport: "stdio", Command: "npx", IdleTimeout: -1}
	if bad.check() == nil {
		t.Error("a negative idleTimeout was taken")
	}
	for _, c := range []struct {
		s    Server
		want time.Duration
	}{
		{Server{}, hubIdle},
		{Server{KeepAlive: true}, 0},
		{Server{KeepAlive: true, IdleTimeout: 30}, 0},
		{Server{IdleTimeout: 30}, 30 * time.Second},
	} {
		if got := c.s.idleFor(hubIdle); got != c.want {
			t.Errorf("%+v: idleFor %v, want %v", c.s, got, c.want)
		}
	}
}

// The router's server is ended after its own idle time, never with
// KeepAlive, and after hubIdle otherwise.
func TestHubIdleFollowsServer(t *testing.T) {
	for _, c := range []struct {
		s     Server
		timer bool
	}{
		{Server{Name: "a"}, true},
		{Server{Name: "b", IdleTimeout: 3600}, true},
		{Server{Name: "c", KeepAlive: true}, false},
	} {
		b := &backend{s: &c.s, busy: 1}
		b.done()
		b.mu.Lock()
		got := b.idle != nil
		if b.idle != nil {
			b.idle.Stop()
		}
		b.mu.Unlock()
		if got != c.timer {
			t.Errorf("%s: idle timer %v, want %v", c.s.Name, got, c.timer)
		}
	}
}

// A session's server (Codex's, through magpie) is kept past runIdle with
// KeepAlive, and ended after its own idle time when it has one.
func TestRunIdleFollowsServer(t *testing.T) {
	for _, c := range []struct {
		s    Server
		idle time.Duration
	}{
		{Server{Command: "x"}, 0},
		{Server{Command: "x", KeepAlive: true}, -1},
		{Server{Command: "x", IdleTimeout: 90}, 90 * time.Second},
	} {
		r, ok := specOf(&c.s)
		if !ok || r.Idle != c.idle {
			t.Errorf("%+v: spec idle %v (%v), want %v", c.s, r.Idle, ok, c.idle)
		}
	}

	for _, c := range []struct {
		idle  time.Duration
		after time.Duration
		ends  bool
	}{
		{-1, runIdle + time.Hour, false},
		{time.Minute, 2 * time.Minute, true},
		{time.Hour, runIdle + time.Minute, false},
	} {
		srv, _, spec := runFixture(t)
		spec.Idle = c.idle
		runGiven(map[string]runSpec{"fake": spec})
		_, a := call(t, srv, "", fmt.Sprintf(runInit, "thread-a"))
		got, _ := call(t, srv, a, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`)
		if !strings.Contains(got, `"id":1`) {
			t.Fatalf("idle %v: %s", c.idle, got)
		}
		runs.Lock()
		s := runs.sessions[a]
		runs.Unlock()
		ch := s.running()
		if ch == nil {
			t.Fatalf("idle %v: no server after a call", c.idle)
		}
		runSweep(time.Now().Add(c.after))
		if c.ends {
			select {
			case <-ch.done:
			case <-time.After(30 * time.Second):
				t.Errorf("idle %v: not ended %v after", c.idle, c.after)
			}
		} else if s.running() == nil {
			t.Errorf("idle %v: ended %v after", c.idle, c.after)
		}
	}
}
