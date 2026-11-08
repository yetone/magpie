package gui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// signInNotice is the id a sign-in alert is notified under, followed by
// its provider's id and "|", so a click opens that provider's accounts.
const signInNotice = "magpie-signin-"

// signInKind is the start of the id a sign-in alert for provider id is
// notified under (sendAs puts the time after it).
func signInKind(id string) string { return signInNotice + url.QueryEscape(id) + "|" }

// signInView is the page a click on a sign-in alert opens, rest being its
// id after signInNotice: the provider's editor, where the account's Sign
// in again is.
func signInView(rest string) string {
	p, _, _ := strings.Cut(rest, "|")
	if p, err := url.QueryUnescape(p); err == nil && p != "" {
		return "providers&edit=" + url.QueryEscape(p)
	}
	return "providers"
}

// signInDays is how many days are left until at, rounded up, as Claude
// Code counts them in its own warning.
func signInDays(at, now time.Time) int {
	d := int((at.Sub(now) + 24*time.Hour - 1) / (24 * time.Hour))
	return max(d, 1)
}

// signInText is the notification of a sign-in alert in the language (en,
// zh, zh-TW, ja or de): the subscription and account, and that it has to
// be signed in again, or in how many days its sign-in ends.
func signInText(lang string, a provider.SignInAlert, now time.Time) (title, body string) {
	title = a.Name
	if a.User != "" {
		title += " · " + a.User
	}
	n := 0
	if a.Kind == "ending" && a.EndsAt != nil {
		n = signInDays(*a.EndsAt, now)
	}
	switch lang {
	case "zh":
		if n > 0 {
			return title, fmt.Sprintf("登录将在 %d 天后失效，点此重新登录", n)
		}
		return title, "登录已失效，点此重新登录"
	case "zh-TW":
		if n > 0 {
			return title, fmt.Sprintf("登入將在 %d 天後失效，點此重新登入", n)
		}
		return title, "登入已失效，點此重新登入"
	case "ja":
		if n > 0 {
			return title, fmt.Sprintf("サインインはあと %d 日で切れます。クリックしてサインインし直してください", n)
		}
		return title, "サインインが切れました。クリックしてサインインし直してください"
	case "de":
		if n == 1 {
			return title, "Die Anmeldung endet in 1 Tag – zum erneuten Anmelden klicken"
		}
		if n > 0 {
			return title, fmt.Sprintf("Die Anmeldung endet in %d Tagen – zum erneuten Anmelden klicken", n)
		}
		return title, "Abgemeldet – zum erneuten Anmelden klicken"
	}
	if n == 1 {
		return title, "Sign-in ends in 1 day — click to sign in again"
	}
	if n > 0 {
		return title, fmt.Sprintf("Sign-in ends in %d days — click to sign in again", n)
	}
	return title, "Signed out — click to sign in again"
}
