package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestTraceSessionHeaderReadinessWithoutHistoryReplay(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	old := time.Now().Add(-24 * time.Hour)
	codex := filepath.Join(CodexDir(), "sessions", "2026", "10", "03", "rollout-2026-10-03T00-00-00-codex-session.jsonl")
	pi := filepath.Join(PiDir(), "sessions", "work", "2026-10-03T00-00-00_pi-session.jsonl")
	for path, header := range map[string]string{codex: `{"type":"session_meta","payload":{"id":"codex-session","base_instructions":"` + strings.Repeat("x", 24<<10) + `"}}`, pi: `{"type":"session","id":"pi-session"}`} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(header+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for agent, id := range map[string]string{"codex": "codex-session", "pi": "pi-session"} {
		if !TraceSessionVisible(agent, id) {
			t.Fatalf("%s header unavailable", agent)
		}
		if TraceSessionVisible(agent, "remote-session") {
			t.Fatal("unknown session marked local")
		}
	}
	r := NewTraceReader(time.Now())
	if spans := r.Poll(true); len(spans) != 0 {
		t.Fatalf("header emitted history: %+v", spans)
	}
	if len(r.VisibleSessions()) != 2 {
		t.Fatalf("visible identities: %+v", r.VisibleSessions())
	}
	// A filename is insufficient: incomplete/wrong headers retain the gateway.
	for _, header := range []string{`{"type":"session","id":"pi-session"}`, `{"type":"message","id":"pi-session"}` + "\n", `{"type":"session","id":"pi-session","padding":"` + strings.Repeat("x", 256<<10) + `"}` + "\n", `{"type":"session","id":"different"}` + "\n"} {
		if err := os.WriteFile(pi, []byte(header), 0600); err != nil {
			t.Fatal(err)
		}
		if TraceSessionVisible("pi", "pi-session") {
			t.Fatalf("unusable header accepted (%d bytes)", len(header))
		}
	}
	if err := os.Remove(pi); err != nil {
		t.Fatal(err)
	}
	r.Poll(false)
	if len(r.VisibleSessions()) != 1 {
		t.Fatal("removed session retained")
	}
}

func TestTraceSessionVisibilityRespectsReaderWindow(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	dir := filepath.Join(PiDir(), "sessions", "work")
	os.MkdirAll(dir, 0700)
	for i := 0; i <= Limit; i++ {
		id := fmt.Sprintf("session-%d", i)
		p := filepath.Join(dir, "now_"+id+".jsonl")
		if err := os.WriteFile(p, []byte(fmt.Sprintf("{\"type\":\"session\",\"id\":%q}\n", id)), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if TraceSessionVisible("pi", "session-0") {
		t.Fatal("session outside reader window suppressed gateway")
	}
	if !TraceSessionVisible("pi", fmt.Sprintf("session-%d", Limit)) {
		t.Fatal("current session invisible")
	}
}

func TestTraceSessionIndexSharesMissesAndDoesNotQueueRequests(t *testing.T) {
	x := new(TraceSessionIndex)
	empty := x.update(nil)
	for i := 0; i < 100; i++ {
		if x.Visible("codex", fmt.Sprintf("remote-%d", i)) {
			t.Fatal("unknown session visible")
		}
	}
	if x.snapshot.Load() != empty {
		t.Fatal("each unknown session repeated discovery")
	}
	x.snapshot.Store(&traceIdentitySnapshot{at: time.Now().Add(-time.Minute), sessions: map[TraceSession]bool{{"codex", "known"}: true}})
	x.refresh.Lock()
	done := make(chan bool, 1)
	go func() { done <- x.Visible("codex", "known") }()
	select {
	case visible := <-done:
		x.refresh.Unlock()
		if !visible {
			t.Fatal("concurrent refresh discarded existing readiness")
		}
	case <-time.After(time.Second):
		x.refresh.Unlock()
		<-done
		t.Fatal("request queued behind directory discovery")
	}
}

func TestTraceClaudeChildrenCannotEvictMainStores(t *testing.T) {
	now := time.Now()
	files := []file{{agent: "codex", path: "codex", main: true, mod: now.Add(-time.Hour)}, {agent: "pi", path: "pi", main: true, mod: now.Add(-time.Hour)}}
	for i := 0; i < Limit+30; i++ {
		files = append(files, file{agent: "claude", path: fmt.Sprint(i), mod: now.Add(time.Duration(i) * time.Second)})
	}
	recent := traceRecentFiles(files)
	if len(recent) != Limit+2 {
		t.Fatalf("quota: %d", len(recent))
	}
	if recent[len(recent)-2].agent != "codex" && recent[len(recent)-1].agent != "codex" {
		t.Fatal("Claude children evicted Codex")
	}
	if recent[len(recent)-2].agent != "pi" && recent[len(recent)-1].agent != "pi" {
		t.Fatal("Claude children evicted Pi")
	}
	for i := 1; i < len(recent); i++ {
		if recent[i].mod.After(recent[i-1].mod) {
			t.Fatal("recency order lost")
		}
	}
}
