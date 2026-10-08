package sessions

import (
	"os"
	"path/filepath"
	"strings"
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
