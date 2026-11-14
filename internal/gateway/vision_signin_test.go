package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Image recognition left to magpie doesn't pick a describer whose every
// account has to be signed in again (@freefcw on X: images went to Claude
// Haiku, its sign-in expired, and the request failed): a key's model that
// sees goes first, and the Claude account is picked again once it is
// signed in. With nothing else that sees it is still named, so agents are
// still told images are described.
func TestAutoVisionPassesOverASignedOutAccount(t *testing.T) {
	claudeMadeFirst(t, false)
	loginFile := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var logins []map[string]any
	raw, _ := os.ReadFile(loginFile)
	if err := json.Unmarshal(raw, &logins); err != nil {
		t.Fatal(err)
	}
	for _, l := range logins {
		l["on"] = false // Claude Code's own account alone
	}
	if err := os.WriteFile(loginFile, mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	if err := provider.Save(provider.Provider{ID: "claude", Models: []string{"claude-haiku-5-5"}}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := provider.SetModelImage("claude/claude-haiku-5-5", &yes); err != nil {
		t.Fatal(err)
	}

	first := AutoVision()
	if !strings.HasPrefix(first, "claude/") {
		t.Fatalf("AutoVision = %q, want the Claude account's while it is signed in", first)
	}
	if _, ready := autoVision(); !ready {
		t.Fatal("the Claude account's describer not ready while signed in")
	}

	// Anthropic refuses the sign-in: the account is marked to sign in again
	claudeRefuses(t, filepath.Join(t.TempDir(), "calls"))
	s := New()
	t.Cleanup(s.subscription.abortAll)
	askClaudeModel(s, "first")
	if rec := askClaudeModel(s, "second"); rec.Code == 200 || !strings.Contains(rec.Body.String(), "sign in again") {
		t.Fatalf("the account isn't signed out: %d %s", rec.Code, rec.Body.String())
	}
	if m := AutoVision(); m != first {
		t.Fatalf("AutoVision = %q with nothing else that sees, want %q still named", m, first)
	}
	if _, ready := autoVision(); ready {
		t.Fatal("a describer whose account has to be signed in again said to be ready")
	}

	if err := provider.Save(provider.Provider{ID: "keyed", Name: "keyed", Chat: "http://keyed.invalid/v1", Key: "k", Models: []string{"eye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("keyed", "http://keyed.invalid/v1", []catalog.Model{{ID: "eye", Images: true, ImageInput: imageInputBool(true)}}); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "keyed/eye" {
		t.Fatalf("AutoVision = %q, want the key's over the signed-out Claude account", m)
	}
	if _, ready := autoVision(); !ready {
		t.Fatal("the key's describer not ready")
	}
}
