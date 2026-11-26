package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// On magpie's gateway, Antigravity's app drops the models its own Google
// sign-in gave it (zysicyj on X: "either official models or custom ones,
// can it have both?"). The notice used to send the user to magpie's
// Antigravity subscription, which is retired: its accounts serve nothing.
// What brings the account's models back beside magpie's now is
// Antigravity (local agy CLI), the user's own agy. So the notice names
// it, an old Antigravity account doesn't quiet it, and an agy-cli account
// does: its models are in the app's list then.
func TestAntigravityOwnModelsNoticeNamesAgyCLI(t *testing.T) {
	home := antigravityHome(t)
	path := filepath.Join(home, ".antigravity", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(antigravityUser), 0o600)
	a := antigravity(home)
	if err := a.Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}

	own := noticeAntigravityOwn.String()
	if !strings.Contains(own, "Antigravity (local agy CLI)") || strings.Contains(own, "Antigravity subscription") {
		t.Errorf("the notice doesn't send the user to agy-cli: %q", own)
	}

	// an account of the retired subscription, which serves nothing
	b, _ := json.Marshal([]map[string]any{{"agent": "antigravity", "user": "me@example.com", "on": true,
		"auth": map[string]any{"access_token": "a", "refresh_token": "r"}}})
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	if !strings.Contains(a.Notice(), own) {
		t.Errorf("an old Antigravity account quieted the notice: %q", a.Notice())
	}

	// agy-cli on and signed in: the account's models are in the list
	const spec = "@magpie-community/opencode-agy-cli-auth@latest"
	writeFile(t, filepath.Join(settings.Dir(), "plugins.json"), `{"plugins":[{"spec":"`+spec+`"}]}`)
	writeFile(t, plugin.AuthPath(), `{"agy-cli":{"type":"success","key":"agy:0123","metadata":{"email":"me@example.com"}}}`)
	plugin.UseCached([]plugin.Provider{{ID: "agy-cli", Spec: spec, Name: "Antigravity (local agy CLI)",
		Models: []plugin.Model{{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"}}}})
	t.Cleanup(plugin.Settle)
	if n := len(provider.Logins("agy-cli")); n != 1 {
		t.Fatalf("the agy-cli account isn't read: %d", n)
	}
	if got := a.Notice(); got != noticeAntigravity.String() {
		t.Errorf("notice with agy-cli signed in: %q", got)
	}
}
