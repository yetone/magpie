package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/bridgesid"
)

// runSession is the session a run's Claude Code was named: given with
// --session-id, or resumed with --resume.
func runSession(t *testing.T, log string) string {
	t.Helper()
	args, _, _ := strings.Cut(log, "\n")
	for _, flag := range []string{"--session-id ", "--resume "} {
		if _, v, ok := strings.Cut(args, flag); ok {
			v, _, _ = strings.Cut(v, " ")
			return v
		}
	}
	t.Fatalf("the run was named no session: %s", args)
	return ""
}

// Every session a bridge run's Claude Code saves has an id bridgesid
// knows, in -p and in the interactive mode, a new run's and a replayed
// transcript's: the local-session readers leave them out, as the gateway
// counts their calls already (iwinux on X: each call listed twice, the
// second time as "Claude Code · Local session").
func TestClaudeRunSessionsAreTheBridges(t *testing.T) {
	t.Run("print", func(t *testing.T) {
		h := newScriptHarness(t, "ok", "")
		h.ask("rules", "a1")
		h.ask("other rules", "b1", "b2") // no run has it: told whole
		runs := h.runs()
		if len(runs) != 2 {
			t.Fatalf("%d runs", len(runs))
		}
		for _, log := range runs {
			if sid := runSession(t, log); !bridgesid.Is(sid) {
				t.Errorf("a -p run's session %s isn't the bridge's", sid)
			}
		}
	})
	t.Run("interactive", func(t *testing.T) {
		h := tuiScriptHarness(t, "")
		h.ask("rules", "a1")
		h.ask("other rules", "b1", "b2") // a transcript replayed
		runs := h.runs()
		if len(runs) != 2 {
			t.Fatalf("%d runs", len(runs))
		}
		resumed := false
		for _, log := range runs {
			resumed = resumed || strings.Contains(log, "--resume ")
			if sid := runSession(t, log); !bridgesid.Is(sid) {
				t.Errorf("an interactive run's session %s isn't the bridge's", sid)
			}
		}
		if !resumed {
			t.Errorf("no run resumed a replayed transcript: %v", runs)
		}
	})
}

// A branch's copy of a run's session is the bridge's too.
func TestClaudeCutSessionIsTheBridges(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	lines := `{"type":"user","uuid":"a","sessionId":"S"}` + "\n" +
		`{"type":"assistant","uuid":"b","sessionId":"S"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "S.jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := cutSession([]string{dir}, "S", "b")
	if err != nil {
		t.Fatal(err)
	}
	if !bridgesid.Is(id) {
		t.Fatalf("the copy %s isn't the bridge's", id)
	}
}
