//go:build !nogui

package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/yetone/magpie/internal/provider"
)

// A click on a sign-in alert opens its provider's editor, where the
// account's Sign in again is (steven_4422 on Discord); a Linux dismissal
// still opens nothing.
func TestSignInAlertOpensItsProvider(t *testing.T) {
	for _, c := range []struct {
		goos, provider, action, want string
	}{
		{"darwin", "claude", notifications.DefaultActionIdentifier, "providers&edit=claude"},
		{"windows", "codex", notifications.DefaultActionIdentifier, "providers&edit=codex"},
		{"linux", "copilot", linuxClick, "providers&edit=copilot"},
		{"linux", "copilot", notifications.DefaultActionIdentifier, ""},
		{"darwin", "a|b&c", notifications.DefaultActionIdentifier, "providers&edit=a%7Cb%26c"},
	} {
		id := fmt.Sprintf("%s%d", signInKind(c.provider), time.Now().UnixNano())
		r := notifications.NotificationResponse{ID: id, ActionIdentifier: c.action}
		if got := clickView(c.goos, r); got != c.want {
			t.Errorf("clickView(%s, %s %s) = %q, want %q", c.goos, id, c.action, got, c.want)
		}
	}
}

// The notification names the subscription and account, and says to sign
// in again, or in how many days the sign-in ends, in every language.
func TestSignInText(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	gone := provider.SignInAlert{Provider: "claude", Name: "Claude", User: "a@example.com", Kind: "lapsed"}
	in := func(d time.Duration) provider.SignInAlert {
		at := now.Add(d)
		return provider.SignInAlert{Provider: "claude", Name: "Claude", User: "a@example.com", Kind: "ending", EndsAt: &at}
	}
	for _, c := range []struct {
		lang string
		a    provider.SignInAlert
		want string
	}{
		{"en", gone, "Signed out — click to sign in again"},
		{"en", in(2*24*time.Hour + time.Hour), "Sign-in ends in 3 days — click to sign in again"},
		{"en", in(5 * time.Hour), "Sign-in ends in 1 day — click to sign in again"},
		{"zh", gone, "登录已失效，点此重新登录"},
		{"zh", in(48 * time.Hour), "登录将在 2 天后失效，点此重新登录"},
		{"zh-TW", gone, "登入已失效，點此重新登入"},
		{"ja", in(24 * time.Hour), "サインインはあと 1 日で切れます。クリックしてサインインし直してください"},
		{"de", in(5 * time.Hour), "Die Anmeldung endet in 1 Tag – zum erneuten Anmelden klicken"},
		{"de", gone, "Abgemeldet – zum erneuten Anmelden klicken"},
	} {
		title, body := signInText(c.lang, c.a, now)
		if title != "Claude · a@example.com" || body != c.want {
			t.Errorf("%s %s: %q / %q, want %q", c.lang, c.a.Kind, title, body, c.want)
		}
	}
	if title, _ := signInText("en", provider.SignInAlert{Name: "Cursor", Kind: "lapsed"}, now); strings.Contains(title, "·") {
		t.Errorf("an account with no name titled %q", title)
	}
}
