// Account discovery off (#1535, MaxHaiCom): with Settings → Privacy's
// "Subscription accounts" off, `magpie accounts discovery off`, or
// MAGPIE_NO_ACCOUNTS=1, magpie reads no agent's sign-in — no
// ~/.claude/.credentials.json, ~/.codex/auth.json, Copilot, Cursor, Kiro,
// Devin or other credential file, no Keychain or Secret Service item, and
// no agent CLI asked who is signed in — and serves no subscription. API-key
// providers work as before.
//
// The gates sit at every source (the readers below Accounts, Logins,
// usage, renewal, warm-up, session identities) and every sink that would
// write a sign-in (switching, forgetting, sign-in, imports, moves onto
// plugins), so a route that skips Accounts still reads nothing. What magpie
// kept of its own (logins.json, plugin-auth.json) is left as it is, unread
// and unwritten, and comes back when discovery is turned on again.
// Middleware plugins and Pi extensions in the plugin host are not
// subscriptions and run as before, and neither is a public plugin, one
// whose every way in is keyless (publicPlugin): it holds no account of
// the user's, so it is listed, signed in and served (lileo0668).

package provider

import (
	"errors"
	"os"

	"github.com/yetone/magpie/internal/settings"
)

var errAccountDiscoveryOff = errors.New("subscription accounts are off in magpie (Settings › Privacy, or `magpie accounts discovery on`)")

// discoveryOff is whether agents' sign-ins are left unread; see above.
func discoveryOff() bool { return settings.AccountDiscoveryOff() }

// readAccountFile is os.ReadFile for an agent's credential file, refused
// while discovery is off.
func readAccountFile(path string) ([]byte, error) {
	if discoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	return os.ReadFile(path)
}
