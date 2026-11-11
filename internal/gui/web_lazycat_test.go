package gui

import (
	"bufio"
	"html"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// lazyCatDomain is the app's domain a box gives magpie (LAZYCAT_APP_DOMAIN).
const lazyCatDomain = "magpie.box.heiyu.space"

// lazyCatPeer is where every request the box's ingress passes the app
// comes from: the app's own proxy container.
const lazyCatPeer = "172.28.2.130:41234"

// lazyCatSignedIn are the headers the box's ingress put on a request from
// a browser signed in to the box as its administrator, to a probe app on
// a box (lzcos 1.6.2+fix.4): the values it sent, but for the account's
// name, the device's ids, the sign-in's time and the client's address.
var lazyCatSignedIn = []string{
	"X-HC-User-ID", "admin", "X-HC-User-Role", "ADMIN", "X-HC-Source", "client",
	"X-HC-Device-ID", "device-1", "X-HC-Device-PeerID", "peer-1", "X-HC-Device-Version", "v0.27.0-168-g2741455f", "X-HC-Login-Time", "1762765000",
	"X-Forwarded-By", "lzc-ingress", "X-Forwarded-Proto", "https", "X-Forwarded-Host", lazyCatDomain,
	"X-Forwarded-For", "fc03:1:2:3:4:5:6:0, 172.28.2.129",
}

// lazyCatPassed are the headers it put on a request on a public path from
// a caller not signed in to the box, which sent X-HC-* of its own: the
// account, role and source were taken off, the groups and the original
// host passed on as they came.
var lazyCatPassed = []string{
	"X-HC-User-Groups", `[{"name":"admin"}]`, "X-HC-Original-Host", "forged.example",
	"X-Forwarded-By", "lzc-ingress", "X-Forwarded-Proto", "https", "X-Forwarded-Host", lazyCatDomain,
	"X-Forwarded-For", "fc03:1:2:3:4:5:6:0, 172.28.2.129",
}

// over is hdr with more set over it.
func over(hdr []string, more ...string) []string { return slices.Concat(hdr, more) }

// without is hdr with no name.
func without(hdr []string, name string) []string {
	var out []string
	for i := 0; i+1 < len(hdr); i += 2 {
		if !strings.EqualFold(hdr[i], name) {
			out = append(out, hdr[i], hdr[i+1])
		}
	}
	return out
}

// lazyCatPage and lazyCatAPI are the Fetch Metadata a browser sends with
// the page it opens at an address typed, and with the page's own API
// requests.
var (
	lazyCatPage = []string{"Accept", "text/html,application/xhtml+xml", "Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}
	lazyCatAPI  = []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}
)

// lazyCatCall is a GET from the box's ingress with the headers hdr sets,
// and again added to them: a name hdr sets, sent once more.
func lazyCatCall(t *testing.T, h http.Handler, target string, hdr []string, again ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	r.RemoteAddr, r.Host = lazyCatPeer, lazyCatDomain
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	for i := 0; i+1 < len(again); i += 2 {
		r.Header.Add(again[i], again[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// lazyCatBox is magpie web's page as StartWeb serves it in a LazyCat app
// with MAGPIE_WEB_SIGN_IN=lazycat; reached says the page answered.
func lazyCatBox(t *testing.T, reached *bool) http.Handler {
	t.Helper()
	onePortHome(t)
	t.Setenv("LAZYCAT_APP_DOMAIN", lazyCatDomain)
	t.Setenv("MAGPIE_WEB_SIGN_IN", "lazycat")
	box, err := webBoxSignIn()
	if err != nil || box == nil {
		t.Fatalf("MAGPIE_WEB_SIGN_IN=lazycat: %v", err)
	}
	return webServed("3430", onePortWebKey, 0, box, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		*reached = true
		rw.WriteHeader(http.StatusTeapot)
	}))
}

// A LazyCat box's administrator, signed in to the box, opens the page and
// its API with no key: none asked for and no cookie set, so signing out of
// the box signs that browser out of the page. The page opens at an address
// typed, from the box's app list or login page (another page of the box's
// site) and from a link on another site; the API answers the page's own
// requests, and the page's script loads. A link to the API from another
// page is still webOwnPage's to refuse.
func TestLazyCatSignInOpensThePage(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	open := func(name, method, target string, hdr ...string) {
		t.Helper()
		reached = false
		w := onePortCall(t, h, method, target, lazyCatPeer, lazyCatDomain, "", hdr...)
		if w.Code != http.StatusTeapot || !reached {
			t.Errorf("%s: %d, reached %v: %s", name, w.Code, reached, w.Body)
		}
		if sc := w.Header().Values("Set-Cookie"); len(sc) > 0 {
			t.Errorf("%s: set %v", name, sc)
		}
	}
	navigate := func(site string) []string {
		return []string{"Accept", "text/html", "Sec-Fetch-Site", site, "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}
	}
	open("the page", "GET", "/", over(lazyCatSignedIn, lazyCatPage...)...)
	open("the page, from the box's app list", "GET", "/?view=providers", over(lazyCatSignedIn, navigate("same-site")...)...)
	open("the page, from a link on another site", "GET", "/", over(lazyCatSignedIn, navigate("cross-site")...)...)
	open("the page's head, from a link on another site", "HEAD", "/", over(lazyCatSignedIn, navigate("cross-site")...)...)
	open("its API, read", "GET", "/api/settings", over(lazyCatSignedIn, lazyCatAPI...)...)
	open("its API, written", "POST", "/api/settings", over(lazyCatSignedIn, over(lazyCatAPI, "Origin", "https://"+lazyCatDomain)...)...)
	open("its script", "GET", "/boot.js", over(lazyCatSignedIn, "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script")...)

	refused := func(name, method, target string, hdr ...string) {
		t.Helper()
		reached = false
		if w := onePortCall(t, h, method, target, lazyCatPeer, lazyCatDomain, "", hdr...); w.Code != http.StatusForbidden || reached {
			t.Errorf("%s: %d, reached %v: %s", name, w.Code, reached, w.Body)
		}
	}
	refused("a link to the API in the box's app list", "GET", "/api/settings", over(lazyCatSignedIn, navigate("same-site")...)...)
	refused("a link to the API on another site", "GET", "/api/state", over(lazyCatSignedIn, navigate("cross-site")...)...)
}

// Only the box's administrators get in by the box's sign-in, from a
// browser, and the box's reason is what anyone else is told, on the page a
// browser gets and in the API's 401: a request on a public path the box
// passed without its sign-in (with groups and a host of the caller's own,
// which it passes on), an account that isn't an administrator (in an
// administrators' group too: the groups are never read), one with no role
// or the role in another case, one another app or the box itself sent, or
// that names no sender or another, and one that names the account, its
// role or its sender twice, in one header or two (a second line in lower
// case is the same name): the ingress names each once, in place of the
// caller's. The key's link and the page's cookie still sign a browser in.
func TestLazyCatSignInTurnsAway(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	turnedAway := func(name string, w *httptest.ResponseRecorder, says string, page bool) {
		t.Helper()
		body := w.Body.String()
		told := strings.Contains(body, says) && !strings.Contains(body, "open the link")
		if page {
			told = strings.Contains(html.UnescapeString(body), says) && strings.Contains(body, `action="`+webSignInPath+`"`) && !strings.Contains(body, onePortWebKey)
		}
		if w.Code != http.StatusUnauthorized || reached || !told {
			t.Errorf("%s: %d, reached %v: %s", name, w.Code, reached, body)
		}
	}
	for _, c := range []struct {
		name string
		hdr  []string
		says string
	}{
		{"past the box's sign-in", lazyCatPassed, "sent no signed-in account"},
		{"an account that isn't an administrator", over(lazyCatSignedIn, "X-HC-User-ID", "guest", "X-HC-User-Role", "NORMAL"), `"guest" isn't one of the box's administrators`},
		{"in an administrators' group", over(lazyCatSignedIn, "X-HC-User-Role", "NORMAL", "X-HC-User-Groups", `[{"name":"admin"}]`), "isn't one of the box's administrators"},
		{"with no role", without(lazyCatSignedIn, "X-HC-User-Role"), `its role is ""`},
		{"with the role in another case", over(lazyCatSignedIn, "X-HC-User-Role", "admin"), `its role is "admin"`},
		{"with two roles in one header", over(lazyCatSignedIn, "X-HC-User-Role", "ADMIN, NORMAL"), `its role is "ADMIN, NORMAL"`},
		{"sent by another app", over(lazyCatSignedIn, "X-HC-Source", "app:cloud.lazycat.app.other"), `"app:cloud.lazycat.app.other" sent this request`},
		{"sent by the box itself", over(lazyCatSignedIn, "X-HC-Source", "system"), `"system" sent this request`},
		{"sent by a sender the box doesn't name", over(lazyCatSignedIn, "X-HC-Source", "browser"), `"browser" sent this request`},
		{"with the sender in another case", over(lazyCatSignedIn, "X-HC-Source", "CLIENT"), `"CLIENT" sent this request`},
		{"with two senders in one header", over(lazyCatSignedIn, "X-HC-Source", "client, app:x"), `"client, app:x" sent this request`},
		{"with no sender", without(lazyCatSignedIn, "X-HC-Source"), "didn't say what sent this request"},
	} {
		reached = false
		turnedAway(c.name+", the page", onePortCall(t, h, "GET", "/?view=usage", lazyCatPeer, lazyCatDomain, "", over(c.hdr, lazyCatPage...)...), c.says, true)
		reached = false
		turnedAway(c.name+", the API", onePortCall(t, h, "GET", "/api/state", lazyCatPeer, lazyCatDomain, "", over(c.hdr, lazyCatAPI...)...), c.says, false)
	}
	for _, c := range []struct {
		name  string
		again []string
		says  string
	}{
		{"the role twice", []string{"X-HC-User-Role", "NORMAL"}, "names X-HC-User-Role 2 times"},
		{"the role twice, the same", []string{"X-HC-User-Role", "ADMIN"}, "names X-HC-User-Role 2 times"},
		{"the sender twice", []string{"X-HC-Source", "system"}, "names X-HC-Source 2 times"},
		{"the account twice", []string{"X-HC-User-ID", "guest"}, "names X-HC-User-ID 2 times"},
	} {
		reached = false
		turnedAway(c.name+", the page", lazyCatCall(t, h, "/", over(lazyCatSignedIn, lazyCatPage...), c.again...), c.says, true)
		reached = false
		turnedAway(c.name+", the API", lazyCatCall(t, h, "/api/state", over(lazyCatSignedIn, lazyCatAPI...), c.again...), c.says, false)
	}
	raw := "GET /api/state HTTP/1.1\r\nHost: " + lazyCatDomain + "\r\nX-HC-User-ID: admin\r\nX-HC-User-Role: ADMIN\r\nx-hc-user-role: NORMAL\r\nX-HC-Source: client\r\nSec-Fetch-Site: same-origin\r\n\r\n"
	r, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	r.RemoteAddr = lazyCatPeer
	w := httptest.NewRecorder()
	reached = false
	h.ServeHTTP(w, r)
	turnedAway("the role twice, the second line in lower case", w, "names X-HC-User-Role 2 times", false)

	reached = false
	if w := onePortCall(t, h, "GET", "/api/state", lazyCatPeer, lazyCatDomain, onePortWebKey, over(lazyCatPassed, lazyCatAPI...)...); w.Code != http.StatusTeapot || !reached {
		t.Errorf("with the page's cookie: %d", w.Code)
	}
	if w := onePortCall(t, h, "GET", "/?k="+onePortWebKey, lazyCatPeer, lazyCatDomain, "", lazyCatPassed...); w.Code != http.StatusSeeOther {
		t.Errorf("the key's link: %d", w.Code)
	}
}

// The box's sign-in is a cookie of the box's, which a browser may send
// with a request another app's page on the box makes: the box's
// administrator, signed in, is turned away when another page fetches the
// API, writes to it, loads the page's script, puts the page in a frame,
// an object or an embed, fetches the page without opening it or sends a
// form to it, and when the browser doesn't say where a request came from
// (no Fetch Metadata, or a navigation that names no site or destination)
// or says what no browser does.
func TestLazyCatSignInAsksWhereFrom(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	for _, c := range []struct {
		name, method, target string
		hdr                  []string
		says                 string
		page                 bool
	}{
		{"a browser that says nothing, the page", "GET", "/", []string{"Accept", "text/html"}, "didn't say where the request came from", true},
		{"a browser that says nothing, the API", "GET", "/api/state", nil, "didn't say where the request came from", false},
		{"a browser that says nothing, a write", "POST", "/api/settings", []string{"Origin", "https://" + lazyCatDomain}, "didn't say where the request came from", false},
		{"another app's page on the box, a fetch", "GET", "/api/state", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}, "Another page asked for this (Sec-Fetch-Site same-site)", false},
		{"another app's page on the box, a write", "POST", "/api/settings", []string{"Origin", "https://other.box.heiyu.space", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}, "Another page asked for this (Sec-Fetch-Site same-site)", false},
		{"another app's page on the box, a frame", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"}, "Another page asked for this (Sec-Fetch-Site same-site)", true},
		{"another app's page on the box, a frame of a frameset", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "frame"}, "Another page asked for this (Sec-Fetch-Site same-site)", true},
		{"another site's page, an object", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "object"}, "Another page asked for this (Sec-Fetch-Site cross-site)", true},
		{"another site's page, an embed", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "embed"}, "Another page asked for this (Sec-Fetch-Site cross-site)", true},
		{"another site's page, a navigation that names no destination", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate"}, "Another page asked for this (Sec-Fetch-Site cross-site)", true},
		{"another site's page, the page fetched, not opened", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "document"}, "Another page asked for this (Sec-Fetch-Site cross-site)", true},
		{"another app's page on the box, the page's script", "GET", "/boot.js", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}, "Another page asked for this (Sec-Fetch-Site same-site)", false},
		{"another site's page, the page's script", "GET", "/boot.js", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}, "Another page asked for this (Sec-Fetch-Site cross-site)", false},
		{"another site's form, sent", "POST", "/api/settings", []string{"Accept", "text/html", "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, "Another page asked for this (Sec-Fetch-Site cross-site)", false},
		{"another site's form, sent to the page", "POST", "/", []string{"Accept", "text/html", "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, "Another page asked for this (Sec-Fetch-Site cross-site)", false},
		{"a navigation that names no site", "GET", "/", []string{"Accept", "text/html", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, "didn't say where the request came from", true},
		{"a browser that says what none does", "GET", "/api/state", []string{"Sec-Fetch-Site", "same-orign"}, `came from "same-orign"`, false},
	} {
		reached = false
		w := onePortCall(t, h, c.method, c.target, lazyCatPeer, lazyCatDomain, "", over(lazyCatSignedIn, c.hdr...)...)
		body := w.Body.String()
		told := strings.Contains(body, c.says)
		if c.page {
			told = strings.Contains(html.UnescapeString(body), c.says) && strings.Contains(body, `action="`+webSignInPath+`"`)
		}
		if w.Code != http.StatusUnauthorized || reached || !told {
			t.Errorf("%s: %d, reached %v: %s", c.name, w.Code, reached, body)
		}
	}
}

// The box's sign-in is asked for with every request and never kept: an
// administrator's next request without the box's account (signed out of
// the box), with another role or from another sender is turned away, and
// the one after it, signed in again, let in.
func TestLazyCatSignInIsAskedEachTime(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	for i, c := range []struct {
		hdr []string
		in  bool
	}{
		{lazyCatSignedIn, true},
		{lazyCatPassed, false},
		{lazyCatSignedIn, true},
		{over(lazyCatSignedIn, "X-HC-User-Role", "NORMAL"), false},
		{lazyCatSignedIn, true},
		{over(lazyCatSignedIn, "X-HC-Source", "system"), false},
		{lazyCatSignedIn, true},
	} {
		reached = false
		w := onePortCall(t, h, "GET", "/api/state", lazyCatPeer, lazyCatDomain, "", over(c.hdr, lazyCatAPI...)...)
		if in := w.Code == http.StatusTeapot && reached; in != c.in {
			t.Errorf("request %d: %d, reached %v, want it let in: %v", i+1, w.Code, reached, c.in)
		}
	}
}

// The box's sign-in is no gateway key: on the page's port the client
// routes are the gateway's to judge, as from another machine, with a key,
// for a request whose sign-in and Fetch Metadata open the page: the
// page's own request, an address typed, a link on another app's page or
// on another site.
func TestLazyCatSignInIsNoGatewayKey(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	prev := served.Swap(gateway.New())
	t.Cleanup(func() { served.Store(prev) })
	key := onePortKey(t)
	navigate := func(site string) []string {
		return []string{"Accept", "text/html", "Sec-Fetch-Site", site, "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}
	}
	for _, from := range []struct {
		name, page string
		hdr        []string
	}{
		{"the page's own request", "/api/state", lazyCatAPI},
		{"an address typed", "/", lazyCatPage},
		{"a link on another app's page", "/", navigate("same-site")},
		{"a link on another site", "/", navigate("cross-site")},
	} {
		signedIn := over(lazyCatSignedIn, from.hdr...)
		reached = false
		if w := onePortCall(t, h, "GET", from.page, lazyCatPeer, lazyCatDomain, "", signedIn...); w.Code != http.StatusTeapot || !reached {
			t.Fatalf("%s, %s: %d, reached %v, want the page to open: %s", from.name, from.page, w.Code, reached, w.Body)
		}
		for _, c := range []struct {
			name, method, target string
			want                 int
			hdr                  []string
		}{
			{"models, with a gateway key", "GET", "/v1/models", http.StatusOK, over(signedIn, "Authorization", "Bearer "+key)},
			{"models", "GET", "/v1/models", http.StatusUnauthorized, signedIn},
			{"models, its head", "HEAD", "/v1/models", http.StatusUnauthorized, signedIn},
			{"chat completions", "POST", "/v1/chat/completions", http.StatusUnauthorized, signedIn},
			{"an MCP server", "POST", "/mcp/github", http.StatusUnauthorized, signedIn},
			{"the Codex backend", "POST", gateway.CodexPath + "/responses", http.StatusUnauthorized, signedIn},
		} {
			reached = false
			w := onePortCall(t, h, c.method, c.target, lazyCatPeer, lazyCatDomain, "", c.hdr...)
			// a 401 turns 429 once the failed-key bucket is spent
			if w.Code != c.want && !(c.want == http.StatusUnauthorized && w.Code == http.StatusTooManyRequests) || reached {
				t.Errorf("%s, %s: %d, want %d (page reached %v): %s", from.name, c.name, w.Code, c.want, reached, w.Body)
			}
		}
	}
}

// MAGPIE_WEB_SIGN_IN takes lazycat in a LazyCat app, or nothing for the
// key alone. lazycat without the app's domain (nothing in front of magpie
// to set the box's headers) or with one that is no domain, and any other
// value, are errors StartWeb returns before it serves anything. Without
// it the box's headers open nothing.
func TestWebBoxSignInChecksItsValue(t *testing.T) {
	onePortHome(t)
	for _, c := range []struct {
		sign, domain string
		ok           bool
	}{
		{"", "", true},
		{"", lazyCatDomain, true},
		{"lazycat", lazyCatDomain, true},
		{"lazycat", "", false},
		{"lazycat", "not a domain", false},
		{"LazyCat", lazyCatDomain, false},
		{"1", lazyCatDomain, false},
		{"key", lazyCatDomain, false},
	} {
		t.Setenv("MAGPIE_WEB_SIGN_IN", c.sign)
		t.Setenv("LAZYCAT_APP_DOMAIN", c.domain)
		box, err := webBoxSignIn()
		if (err == nil) != c.ok || (box != nil) != (c.ok && c.sign != "") {
			t.Errorf("MAGPIE_WEB_SIGN_IN=%q, LAZYCAT_APP_DOMAIN=%q: %v, %v", c.sign, c.domain, box != nil, err)
		}
	}

	t.Setenv("MAGPIE_WEB_SIGN_IN", "lazycat")
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	was := Version
	t.Cleanup(func() { Version = was })
	if w, err := StartWeb("127.0.0.1:0", "test"); err == nil || !strings.Contains(err.Error(), "LAZYCAT_APP_DOMAIN") {
		if w != nil {
			w.srv.Close()
		}
		t.Fatalf("StartWeb with lazycat outside LazyCat: %v", err)
	}

	t.Setenv("MAGPIE_WEB_SIGN_IN", "")
	t.Setenv("LAZYCAT_APP_DOMAIN", lazyCatDomain)
	box, _ := webBoxSignIn()
	var reached bool
	h := webServed("3430", onePortWebKey, 0, box, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		reached = true
		rw.WriteHeader(http.StatusTeapot)
	}))
	if w := onePortCall(t, h, "GET", "/", lazyCatPeer, lazyCatDomain, "", over(lazyCatSignedIn, "Accept", "text/html")...); w.Code != http.StatusUnauthorized || reached {
		t.Errorf("the box's headers without MAGPIE_WEB_SIGN_IN: %d, reached %v", w.Code, reached)
	}
}

// The box's headers are the caller's own from anywhere but the box's
// network: an administrator's headers, forged, from a public address (the
// app's port published past the ingress), or from a peer that isn't an
// address, open nothing, with Fetch Metadata that suits.
func TestLazyCatSignInIgnoresAPublicPeer(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	for _, peer := range []string{"203.0.113.9:50000", "[2001:db8::1]:50000", "8.8.8.8:1", "not-an-address", ""} {
		reached = false
		w := onePortCall(t, h, "GET", "/api/state", peer, lazyCatDomain, "", over(lazyCatSignedIn, lazyCatAPI...)...)
		if w.Code != http.StatusUnauthorized || reached {
			t.Errorf("peer %q: %d, reached %v", peer, w.Code, reached)
		}
		w = onePortCall(t, h, "GET", "/", peer, lazyCatDomain, "", over(lazyCatSignedIn, lazyCatPage...)...)
		if w.Code != http.StatusUnauthorized || reached {
			t.Errorf("peer %q, the page: %d, reached %v", peer, w.Code, reached)
		}
	}
	for _, peer := range []string{"172.28.2.130:1", "10.0.0.2:1", "127.0.0.1:1", "[fd00::2]:1"} {
		reached = false
		if w := onePortCall(t, h, "GET", "/api/state", peer, lazyCatDomain, "", over(lazyCatSignedIn, lazyCatAPI...)...); w.Code != http.StatusTeapot || !reached {
			t.Errorf("peer %q: %d, reached %v", peer, w.Code, reached)
		}
	}
}

// A sandboxed frame's request (Origin: null) from the box's network, with
// the administrator's headers, is another page's request: it opens nothing.
func TestLazyCatSignInTurnsAwayOriginNull(t *testing.T) {
	var reached bool
	h := lazyCatBox(t, &reached)
	for _, fetch := range [][]string{
		{"Origin", "null", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"},
		{"Origin", "null", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"},
		{"Origin", "null"},
	} {
		reached = false
		w := onePortCall(t, h, "POST", "/api/state", lazyCatPeer, lazyCatDomain, "", over(lazyCatSignedIn, fetch...)...)
		if w.Code != http.StatusUnauthorized || reached {
			t.Errorf("%v: %d, reached %v", fetch, w.Code, reached)
		}
	}
}
