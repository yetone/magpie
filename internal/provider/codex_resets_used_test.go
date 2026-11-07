package provider

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// foobra12 on Discord: "magpie会自动使用codex重置卡吗 我发现刚才好像不小心
// 自动给我使用了一张". A reset magpie spent by itself was said only in the
// gateway's log, so a reset gone from the card read the same whether magpie
// used it, the user did, or it ran out. Each one magpie spends is kept, with
// why, and the account's reading carries them, newest first: one spent as
// it was about to run out, one by hand, and one as the week was used up.
func TestResetUsesKept(t *testing.T) {
	signIn(t)
	rememberLogins(true) // the account kept, as the Usage page lists it
	old, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = old, oldUntil })
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	var weekUsed atomic.Int32
	weekUsed.Store(35)
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 10, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": weekUsed.Load(), "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 3}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 3, "credits": []any{
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	const who = "me@example.com"

	if got := ResetsUsed(who); len(got) != 0 {
		t.Fatalf("none used yet, got %+v", got)
	}
	if err := SetCodexAutoReset(who, true); err != nil {
		t.Fatal(err)
	}
	// about to run out, the account used: spent by itself
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 {
		t.Fatalf("spent %d, want the one about to run out", consumed.Load())
	}
	// by hand, from the Usage page or the CLI
	if out, err := UseCodexReset(t.Context(), who); err != nil || out.Code != "reset" {
		t.Fatalf("by hand: %+v %v", out, err)
	}
	// the week used up, no other account
	weekUsed.Store(100)
	if out, err := AutoUseCodexReset(t.Context(), who); err != nil || out.Code != "reset" {
		t.Fatalf("week used up: %+v %v", out, err)
	}
	got := ResetsUsed(who)
	want := []string{ResetUsedWeek, ResetUsedByHand, ResetUsedExpiring}
	if len(got) != len(want) {
		t.Fatalf("kept %+v, want %v", got, want)
	}
	for i, u := range got {
		if u.Why != want[i] || u.User != who || u.At.IsZero() || u.Windows != 2 {
			t.Fatalf("use %d: %+v, want why %q", i, u, want[i])
		}
	}
	// the account's card says so: its reading carries them
	q := LoginUsage(t.Context(), "codex")[who]
	if len(q.ResetsUsed) != 3 || q.ResetsUsed[0].Why != ResetUsedWeek {
		t.Fatalf("the card's reading has %+v", q.ResetsUsed)
	}
	// another account's are its own
	if other := ResetsUsed("other@example.com"); len(other) != 0 {
		t.Fatalf("another account got %+v", other)
	}
	// kept on disk: a restart still says it
	if _, err := os.Stat(filepath.Join(settings.Dir(), "codex-resets-used.json")); err != nil {
		t.Fatal(err)
	}
}

// A record write that fails is seen, not taken for kept (#5556): the
// reset is spent on the server either way, so a lost record must never
// spend a second credit — the spend still succeeds, exactly once, and
// the record's loss is named in the log. The record's path made a
// directory is the failure stood in: reading it and replacing it both
// fail, as a full disk or a denied path would.
func TestResetUseWriteFailureSeen(t *testing.T) {
	signIn(t)
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
				map[string]any{"id": "c1", "status": "available"},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	p := resetsUsedPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Mkdir(p, 0o755); err != nil { // the record's path, a directory
		t.Fatal(err)
	}
	oldLog := log.Writer()
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	out, err := UseCodexResetCard(t.Context(), "me@example.com", "")
	if err != nil || out.Code != "reset" {
		t.Fatalf("the reset itself: %+v %v", out, err)
	}
	if consumed.Load() != 1 {
		t.Fatalf("consumed %d times, want once: a lost record is never a second spend", consumed.Load())
	}
	if !strings.Contains(logged.String(), "codex-resets-used.json") {
		t.Fatalf("the record's loss went unnamed; log:\n%s", logged.String())
	}
	// kept, said so: a note whose write fails is an error, not silence
	if err := noteResetUsed("me@example.com", ResetUsedByHand, ResetOutcome{Code: "reset", Windows: 2}); err == nil {
		t.Fatal("a failed write of the record went unnamed")
	}
}

// A use that didn't start anything again isn't one: nothing kept.
func TestResetUseNotKeptWhenNothingSpent(t *testing.T) {
	signIn(t)
	noteResetUsed("me@example.com", ResetUsedByHand, ResetOutcome{Code: "nothing_to_reset"})
	noteResetUsed("me@example.com", ResetUsedByHand, ResetOutcome{Code: "no_credit"})
	if got := ResetsUsed("me@example.com"); len(got) != 0 {
		t.Fatalf("kept %+v", got)
	}
}

// A file that doesn't parse is copied aside before a new use is written,
// never taken for none and written over; a use older than resetsUsedKeep
// is let go.
func TestResetUsesUnreadableKeptAside(t *testing.T) {
	signIn(t)
	p := resetsUsedPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResetsUsed("me@example.com"); got != nil {
		t.Fatalf("unreadable read as %+v", got)
	}
	noteResetUsed("me@example.com", ResetUsedByHand, ResetOutcome{Code: "reset", Windows: 1})
	aside, _ := filepath.Glob(p + ".bad-*")
	if len(aside) != 1 {
		t.Fatalf("copied aside: %v", aside)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != "{not json" {
		t.Fatalf("aside holds %q", b)
	}
	if got := ResetsUsed("me@example.com"); len(got) != 1 {
		t.Fatalf("after a use: %+v", got)
	}
	// an old one goes on the next write
	old := []ResetUse{{User: "me@example.com", At: time.Now().Add(-resetsUsedKeep - time.Hour), Why: ResetUsedWeek}}
	b, _ := json.Marshal(old)
	os.WriteFile(p, b, 0o600)
	noteResetUsed("me@example.com", ResetUsedExpiring, ResetOutcome{Code: "reset", Windows: 1})
	if got := ResetsUsed("me@example.com"); len(got) != 1 || got[0].Why != ResetUsedExpiring {
		t.Fatalf("kept %+v", got)
	}
}
