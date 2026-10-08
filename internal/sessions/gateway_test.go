package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func gatewayHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SetGatewayRecording(false, true); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayConversationManyTurnsShareFile(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Hour)
	for i := range 4200 {
		turn := GatewayTurn{Agent: "pi", Session: "s", Time: at.Add(time.Duration(i) * time.Millisecond), Output: []Part{{Role: "assistant", Kind: "text", Text: fmt.Sprint(i)}}}
		if err := SaveGatewayTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(gatewayDir(), at.UTC().Format(time.DateOnly), gatewayIdentity("pi", "s"))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("one session should use one file: %d %v", len(entries), err)
	}
	tr, err := GatewayTranscript("pi", "s")
	if err != nil || tr.Captured != gatewayReadMax || !tr.Cut || tr.Parts[len(tr.Parts)-1].Text != "4199" {
		t.Fatalf("recent turns missing: %+v %v", tr, err)
	}
}

func TestGatewayConversationClearAndReenableRejectsOldRequest(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	generation := GatewayRecordingGeneration()
	if err := SetGatewayRecording(false, true); err != nil {
		t.Fatal(err)
	}
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveGatewayTurnGeneration(GatewayTurn{Agent: "pi", Session: "s", Time: time.Now()}, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gatewayDir()); !os.IsNotExist(err) {
		t.Fatal("old request resurrected cleared content")
	}
}

func TestGatewayConversationLegacyAndNewTurns(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	dir := filepath.Join(gatewayDir(), at.UTC().Format(time.DateOnly), gatewayIdentity("pi", "s"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := GatewayTurn{Agent: "pi", Session: "s", Time: at, Output: []Part{{Role: "assistant", Kind: "text", Text: "old"}}}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "old.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	old.Time = at.Add(time.Second)
	old.Output[0].Text = "new"
	if err := SaveGatewayTurn(old); err != nil {
		t.Fatal(err)
	}
	tr, err := GatewayTranscript("pi", "s")
	if err != nil || tr.Captured != 2 || tr.Parts[0].Text != "old" || tr.Parts[1].Text != "new" {
		t.Fatalf("legacy content lost: %+v %v", tr, err)
	}
}

func TestGatewayConversationSaveDoesNotScanOtherSessions(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(gatewayDir(), "unreadable")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(other, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(other, 0o700)
	if err := SaveGatewayTurn(GatewayTurn{Agent: "pi", Session: "s", Time: time.Now()}); err != nil {
		t.Fatalf("save scanned unrelated storage: %v", err)
	}
}

func TestGatewayConversationConsentAndIsolation(t *testing.T) {
	gatewayHome(t)
	now := time.Now()
	u := Part{Role: "user", Kind: "text", Text: "hello"}
	a := Part{Role: "assistant", Kind: "text", Text: "world"}
	turn := GatewayTurn{Agent: "claude", Session: "../../outside", Time: now, Input: []Part{u}, Output: []Part{a}}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gatewayDir()); !os.IsNotExist(err) {
		t.Fatal("recorded without consent")
	}
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	turn.Time = now.Add(time.Millisecond)
	turn.Input = []Part{u, a, {Role: "user", Kind: "text", Text: "hello again"}}
	turn.Output = []Part{{Role: "assistant", Kind: "text", Text: "second reply"}}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, err := GatewayTranscript("claude", "../../outside")
	if err != nil || len(tr.Parts) != 4 || tr.Captured != 2 {
		t.Fatalf("replayed history: %+v %v", tr, err)
	}
	other, err := GatewayTranscript("codex", "../../outside")
	if err != nil || len(other.Parts) != 0 {
		t.Fatalf("identity leak: %+v %v", other, err)
	}
	if err := SetGatewayRecording(false, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, _ = GatewayTranscript("claude", "../../outside")
	if tr.Captured != 2 {
		t.Fatal("off deleted or added content")
	}
	if err := SetGatewayRecording(false, true); err != nil {
		t.Fatal(err)
	}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, _ = GatewayTranscript("claude", "../../outside")
	if tr.Captured != 0 || settings.Load().GatewayConversations {
		t.Fatal("clear refilled by in-flight turn")
	}
}

func TestGatewayConversationLimitsAndExpiry(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	turn := GatewayTurn{Agent: "pi", Session: "s", Time: time.Now(), Input: []Part{{Role: "user", Kind: "text", Text: strings.Repeat("a", 100_000)}}}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, err := GatewayTranscript("pi", "s")
	if err != nil || !tr.Cut || len(tr.Parts) != 1 || tr.Parts[0].Cut != 92_000 {
		t.Fatalf("limit: %+v %v", tr, err)
	}
	old := filepath.Join(gatewayDir(), time.Now().Add(-9*24*time.Hour).UTC().Format(time.DateOnly))
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "expired"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PruneGatewayConversations(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired content retained")
	}
	turn.Time = time.Now().Add(-8 * 24 * time.Hour)
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, _ = GatewayTranscript("pi", "s")
	if tr.Captured != 1 {
		t.Fatal("expired turn accepted")
	}
}

func TestGatewayConversationKeepsRepeatedNewTurns(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	u := Part{Role: "user", Kind: "text", Text: "again"}
	a := Part{Role: "assistant", Kind: "text", Text: "done"}
	for i := range 2 {
		if err := SaveGatewayTurn(GatewayTurn{Agent: "codex", Session: "s", Time: time.Now().Add(time.Duration(i) * time.Millisecond), Input: []Part{u}, Output: []Part{a}}); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := GatewayTranscript("codex", "s")
	if err != nil || len(tr.Parts) != 4 {
		t.Fatalf("incremental repeated turns erased: %+v %v", tr, err)
	}
}

func TestGatewayConversationInterruptedAppend(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	turn := GatewayTurn{Agent: "pi", Session: "s", Time: at, Output: []Part{{Role: "assistant", Kind: "text", Text: "first"}}}
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(gatewayDir(), at.UTC().Format(time.DateOnly), gatewayIdentity("pi", "s"), "turns.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"agent":"pi","session":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := GatewayTranscript("pi", "s")
	if err != nil || tr.Captured != 1 || !tr.Cut {
		t.Fatalf("partial append hid complete turns: %+v %v", tr, err)
	}
	turn.Time = at.Add(time.Second)
	turn.Output[0].Text = "second"
	if err := SaveGatewayTurn(turn); err != nil {
		t.Fatal(err)
	}
	tr, err = GatewayTranscript("pi", "s")
	if err != nil || tr.Captured != 2 || tr.Parts[1].Text != "second" {
		t.Fatalf("partial append poisoned later turns: %+v %v", tr, err)
	}
}

func TestGatewayConversationConcurrentSaveAndPrune(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := SaveGatewayTurn(GatewayTurn{Agent: "pi", Session: "s", Time: at.Add(time.Duration(i) * time.Millisecond), Output: []Part{{Role: "assistant", Kind: "text", Text: fmt.Sprint(i)}}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		if err := PruneGatewayConversations(at); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	tr, err := GatewayTranscript("pi", "s")
	if err != nil || tr.Captured != 20 {
		t.Fatalf("concurrent saves lost turns: %+v %v", tr, err)
	}
}

func BenchmarkGatewayConversationSaveWithHistory(b *testing.B) {
	for _, files := range []int{0, 4000} {
		b.Run(fmt.Sprint(files), func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			if err := SetGatewayRecording(true, false); err != nil {
				b.Fatal(err)
			}
			dir := filepath.Join(gatewayDir(), "history")
			if err := os.MkdirAll(dir, 0700); err != nil {
				b.Fatal(err)
			}
			data := make([]byte, 32<<10)
			for i := range files {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", i)), data, 0600); err != nil {
					b.Fatal(err)
				}
			}
			turn := GatewayTurn{Agent: "pi", Session: "benchmark", Time: time.Now(), Output: []Part{{Role: "assistant", Kind: "text", Text: "reply"}}}
			b.ResetTimer()
			for b.Loop() {
				if err := SaveGatewayTurn(turn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestGatewayConversationOversizedLine(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(gatewayDir(), time.Now().UTC().Format(time.DateOnly), gatewayIdentity("pi", "s"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turns.jsonl"), []byte(strings.Repeat(" ", gatewayTurnMax+1)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := GatewayTranscript("pi", "s"); err == nil {
		t.Fatal("oversized corrupt line reported as an empty transcript")
	}
}

func TestGatewayConversationQuotaRunsInBackground(t *testing.T) {
	gatewayHome(t)
	if err := SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	paths := []string{}
	for i := range 2 {
		dir := filepath.Join(gatewayDir(), at.UTC().Format(time.DateOnly), fmt.Sprint(i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "turns.jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(gatewayStoreMax/2 + 1)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		stamp := at.Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := SaveGatewayTurn(GatewayTurn{Agent: "pi", Session: "s", Time: at}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("save pruned unrelated session: %v", err)
		}
	}
	if err := PruneGatewayConversations(at); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("background cleanup retained oldest over-quota file")
	}
	if _, err := os.Stat(paths[1]); err != nil {
		t.Fatalf("background cleanup removed newer file: %v", err)
	}
	if tr, err := GatewayTranscript("pi", "s"); err != nil || tr.Captured != 1 {
		t.Fatalf("cleanup removed current turn: %+v %v", tr, err)
	}
}
