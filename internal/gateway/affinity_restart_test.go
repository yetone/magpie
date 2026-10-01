package gateway

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A conversation stays with the key or account that answered it across a
// restart of magpie, an update's included, while the vendor still has it
// cached there (vincentzhang on Discord: restarted, the session went to
// the first account; updated and restarted, back to the second, and the
// prompt cache was read by neither).
func TestAffinitySurvivesRestart(t *testing.T) {
	fresh(t)
	v := &keyed{fail: map[string]int{"ka": 429}}
	serveOn(t, "a", "ka", []string{"m"}, v, "kb")
	s := New()
	turn1 := `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`
	if code, body := postAs(t, s, "sess-1", turn1); code != 200 || !strings.Contains(body, "from kb") {
		t.Fatalf("first turn: %d %s", code, body)
	}
	fi, err := os.Stat(sticksPath())
	if err != nil {
		t.Fatalf("affinity not kept on disk: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("affinity file mode %v, want 0600", fi.Mode().Perm())
	}

	// a restart: what magpie had in memory is gone, ka is no longer resting
	sticks.Lock()
	sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
	sticks.Unlock()
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	v.fail = nil
	s = New()

	turn2 := `{"model":"a/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"from kb"},{"role":"user","content":"and then?"}]}`
	if code, body := postAs(t, s, "sess-1", turn2); code != 200 || !strings.Contains(body, "from kb") {
		t.Fatalf("after the restart the conversation left kb: %d %s (tried %v)", code, body, v.tried)
	}
	if a := s.trace.routes[len(s.trace.routes)-1].Affinity; a == nil || !a.Kept || a.Why != "cache" {
		t.Fatalf("affinity after restart: %+v", a)
	}
	// a conversation never answered still goes to whoever routing puts first
	if code, body := postAs(t, s, "sess-2", `{"model":"a/m","messages":[{"role":"user","content":"other"}]}`); code != 200 || !strings.Contains(body, "from ka") {
		t.Fatalf("a new conversation: %d %s", code, body)
	}
}

// What is kept on disk is bounded: only the latest conversations, and none
// older than a conversation is remembered.
func TestAffinityFileBounded(t *testing.T) {
	fresh(t)
	now := time.Now()
	sticks.Lock()
	for i := range sticksKept + 50 {
		sticks.m[strings.Repeat("k", 1+i%7)+string(rune('a'+i%26))+time.Duration(i).String()] = stick{rest: "a", who: "a", at: now.Add(-time.Duration(i) * time.Second)}
	}
	sticks.m["old"] = stick{rest: "a", who: "a", at: now.Add(-stickKeep - time.Minute)}
	sticks.Unlock()
	saveSticks()
	sticks.Lock()
	sticks.m, sticks.from, sticks.mod = map[string]stick{}, "", time.Time{}
	_, old := stickOf("old")
	n := len(sticks.m)
	sticks.Unlock()
	if old || n != sticksKept {
		t.Fatalf("read back %d (old kept %v), want the latest %d", n, old, sticksKept)
	}
}
