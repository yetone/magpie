package gui

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"

	"github.com/yetone/magpie/internal/gateway"
)

// On a LazyCat (懒猫微服, lazycat.cloud) box every app is behind the box's
// own sign-in. The box's ingress (lzc-ingress) sends a browser that isn't
// signed in to the box's login page, and puts on every request it passes
// an app who sent it, in place of whatever the caller sent: the box
// account (X-HC-User-ID), its role (X-HC-User-Role, ADMIN or NORMAL) and
// what sent it (X-HC-Source, client for a person's browser). A path the
// app's manifest leaves public (public_path) is passed without the
// sign-in, and then with none of the three. Seen on lzcos 1.6.2: forged
// values, repeated and in any case, are dropped from a request that isn't
// signed in, and a forged value is replaced in one that is; another app's
// container reaches the app through the ingress alone (a connection to the
// app's container times out), unless it runs in the box's own network
// (network_mode: host), which reaches the app's port as the box does.
//
// So magpie web in a LazyCat app can take the box's sign-in for its key
// (MAGPIE_WEB_SIGN_IN=lazycat): the box's administrators open the page
// from the box's app list with no key to paste. It sets no cookie of the
// page's own, so a browser that came in that way is signed out of the page
// when it signs out of the box. The key's link still signs a browser in,
// and its cookie keeps that browser in.
//
// The setting is the package's word that the box's ingress is the only
// way to the page's port, which magpie can't check: it refuses lazycat
// only where LazyCat didn't set LAZYCAT_APP_DOMAIN, so the setting isn't
// taken somewhere else by mistake. A port the app publishes past the
// ingress (a TCP ingress, the box's LAN port forwarding) passes the
// headers as the caller wrote them, so the page's port is never published
// that way (docs/reference.md, LazyCat). The gateway's client routes on
// the page's port are judged by the gateway (onePort), with a gateway key,
// as everywhere.

// boxSignIn says why the sign-in of the box magpie web runs on doesn't let
// r in, and a few words of it for the log; "" when it does.
type boxSignIn func(r *http.Request) (why, short string)

// webBoxSignIn is the sign-in MAGPIE_WEB_SIGN_IN names besides the key:
// none when it is unset, lazyCatSignIn for lazycat. Any other value is an
// error, and so is lazycat without LAZYCAT_APP_DOMAIN, which LazyCat sets
// in each of an app's containers: where it isn't set, no box's ingress is
// in front of magpie, and anyone could send the headers lazyCatSignIn
// reads. Its being set proves no ingress either; that is the package's to
// see to.
func webBoxSignIn() (boxSignIn, error) {
	switch v := os.Getenv("MAGPIE_WEB_SIGN_IN"); v {
	case "":
		return nil, nil
	case "lazycat":
		if gateway.LazyCatURL() == "" {
			return nil, errors.New("MAGPIE_WEB_SIGN_IN=lazycat is for magpie web behind a LazyCat box's ingress, and LAZYCAT_APP_DOMAIN, which LazyCat sets in an app's containers, isn't set to the app's domain: without the ingress, anyone could send the headers it reads")
		}
		return lazyCatSignIn, nil
	default:
		return nil, fmt.Errorf("MAGPIE_WEB_SIGN_IN is %q; it takes lazycat, or nothing for the key alone", v)
	}
}

// lazyCatSignIn lets in a request the box's ingress passed from one of the
// box's administrators in a browser: a box account whose role is ADMIN,
// sent by a person's browser (X-HC-Source client), each named once, for a
// request the page itself or a browser opening it asked for (boxFetched).
// Another account is turned away, and so is a request another app or the
// box itself sent (app:…, system), one that names no sender, and one that
// names any of the three twice, which the ingress doesn't: the page holds
// magpie's keys and sign-ins, which are the administrators'. Nothing else
// the box sends is read; X-HC-User-Groups least of all, which the ingress
// passed on as the caller wrote it (lzcos 1.6.2).
func lazyCatSignIn(r *http.Request) (why, short string) {
	// The ingress reaches the app over the box's own network. LazyCat's
	// docs don't give its address, so this can't name it; but a peer on a
	// public address is never it, whatever headers it sends.
	if !boxPeer(r.RemoteAddr) {
		return "This request didn't come from the box's network, so the headers it carries (X-HC-*) are the caller's own, not the box's ingress's.", "peer " + r.RemoteAddr + " is not on the box's network"
	}
	for _, k := range []string{"X-HC-User-ID", "X-HC-User-Role", "X-HC-Source"} {
		if n := len(r.Header.Values(k)); n > 1 {
			return fmt.Sprintf("This request names %s %d times, and the box's ingress names it once, in place of what the caller sent: it didn't come through the box's sign-in alone.", k, n), fmt.Sprintf("%s sent %d times", k, n)
		}
	}
	user, role, from := r.Header.Get("X-HC-User-ID"), r.Header.Get("X-HC-User-Role"), r.Header.Get("X-HC-Source")
	switch {
	case user == "":
		return "The LazyCat box sent no signed-in account with this request, so it came past the box's sign-in: through a port the app publishes (a TCP ingress, the box's LAN port forwarding), or a path its manifest leaves public (public_path). Open magpie at " + gateway.LazyCatURL() + ", signed in to the box.", "no box account sent"
	case role != "ADMIN":
		return fmt.Sprintf("The box account %q isn't one of the box's administrators (its role is %q), and magpie's settings and keys are theirs. An administrator can give it a gateway key for its agents.", user, role), fmt.Sprintf("box account %q is %q", user, role)
	case from == "":
		return "The box didn't say what sent this request (X-HC-Source): magpie's settings open in a person's browser (client) alone.", "no X-HC-Source sent"
	case from != "client":
		return fmt.Sprintf("The box says %q sent this request, not a person's browser (client): magpie's settings open in a browser alone.", from), fmt.Sprintf("sent by %q", from)
	}
	return boxFetched(r)
}

// boxFetched says why the box's sign-in can't let r in for where the
// browser says r came from (its Fetch Metadata), "" when it can: a request
// of the page's own (Sec-Fetch-Site same-origin), one the user made (none:
// an address typed, a bookmark), or a browser opening the page in a tab of
// its own from another page, to read it (a navigation to a document, GET or
// HEAD: the box's app list, its login page sending the browser back, a
// link). The box's sign-in is a cookie of the box's, whose terms magpie
// doesn't set, and the box's apps are one site: a browser may send it with
// a request another app's page makes, for a script, a frame or a fetch, so
// those are turned away, and so is any request from a browser that doesn't
// say where it came from. The page's own cookie, which holds the key, is
// SameSite=Lax; webOwnPage keeps /api to the page's origin for both.
func boxFetched(r *http.Request) (why, short string) {
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "same-origin", "none":
		return "", ""
	case "same-site", "cross-site":
		if r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			return "", ""
		}
		return "Another page asked for this (Sec-Fetch-Site " + site + "), and the box's sign-in opens magpie in a tab of its own: never in another page's frame, or for its scripts and requests.", "asked by another page (" + site + ")"
	case "":
		return "This browser didn't say where the request came from (Sec-Fetch-Site), and the box's sign-in is taken from a browser that does, so that another page can't use it. Open magpie in a current browser, or with the link that carries the key, in the app's log.", "no Sec-Fetch-Site sent"
	default:
		return fmt.Sprintf("This browser says the request came from %q (Sec-Fetch-Site), which the box's sign-in doesn't take.", site), fmt.Sprintf("Sec-Fetch-Site %q", site)
	}
}

// boxPeer says addr, a request's peer, is on a private, loopback or
// link-local network, where a box's ingress is, and not a public address. An
// address it can't read is not.
func boxPeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}
