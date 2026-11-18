// Package ccswitchlink hands CC Switch's own ccswitch:// links back to it.
//
// magpie v0.1.3981 to v0.1.4454 claimed ccswitch:// on every system
// (889758605), and 03314b25a stopped that. A claim an older magpie made
// can still be there: macOS's Launch Services remembering the old
// Info.plist, HKCU\Software\Classes\ccswitch on Windows, and
// magpie-ccswitch.desktop as the desktop's default on Linux. A link that
// arrives that way is opened with CC Switch when it is installed, and the
// claim is given back to it, so the next link goes there directly
// (asombrado_a on X: with both apps installed, a ccswitch:// import opened
// magpie).
//
// What is known of CC Switch comes from its own source and releases
// (farion1231/cc-switch, v4.0.35): bundle id com.ccswitch.desktop with
// CFBundleURLSchemes ccswitch; the per-user MSI installs
// %LOCALAPPDATA%\Programs\CC Switch\cc-switch.exe and writes
// HKCU\Software\Classes\ccswitch to run it with "%1"; on Linux the package
// has "CC Switch.desktop" (MimeType=x-scheme-handler/ccswitch, Exec with no
// %u) and the app writes ~/.local/share/applications/cc-switch-handler.desktop
// (Exec=… %u) and makes it the default on its first start.
package ccswitchlink

import (
	"errors"
	"strings"
)

// Scheme is CC Switch's URL scheme.
const Scheme = "ccswitch"

// ErrNotInstalled is Forward's answer when no CC Switch is found.
var ErrNotInstalled = errors.New("CC Switch isn't installed; magpie opens magpie:// links only")

// Is reports whether link is a ccswitch:// link.
func Is(link string) bool {
	return strings.HasPrefix(strings.ToLower(link), Scheme+":")
}

// Forward opens link with CC Switch and gives back the claim on the scheme
// an older magpie made. ErrNotInstalled when there is no CC Switch: the
// link is then left alone, and so is the claim.
func Forward(link string) error {
	if !Is(link) {
		return errors.New("not a ccswitch:// link")
	}
	return forward(link)
}
