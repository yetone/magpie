package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzhttp"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/update"
)

// `magpie web` serves the window's page to a browser, for a computer that
// can't show the app — no desktop, WSL, a server reached over SSH — with
// the gateway beside it as the app has. What is the desktop's (the panel,
// the tray, the Dock) does nothing there; a folder is named, not opened.

// webHost is the Windows a browser tab stands for.
type webHost struct{ quit func() }

func (webHost) HidePanel()                       {}
func (webHost) ShowMain(string)                  {}
func (h webHost) Quit()                          { h.quit() }
func (webHost) OpenURL(u string) error           { return openOnHost(u) }
func (webHost) Copy(string) bool                 { return false }
func (webHost) FitPanel(int, Glide)              {}
func (webHost) TintPanel([4]uint8, int) bool     { return false }
func (webHost) TintTitleBar([4]uint8, bool) bool { return false }
func (webHost) SetTextSize(int)                  {} // the browser zooms its own tab
func (webHost) OpenFolder(path string) error {
	return errors.New("in the browser magpie can't open folders: it is " + tilde(path))
}
func (webHost) ChooseFolder(string) (string, error) {
	return "", errors.New("in the browser magpie can't show a folder picker: type the folder's path")
}

// openOnHost opens an app's import link (cindy://, lody://, ...) on this
// computer, the one magpie runs on: the apps' databases it reads for "added"
// are here, so the link must open here and not in the browser that views the
// page, which may be another machine's (Jealousy_Du on X). A web page's own
// https links stay the page's to open. Tests stand in for the system.
var openOnHost = func(u string) error {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return nil // the page opens links itself
	}
	return proc.OpenBrowser(u)
}

// Web is a started `magpie web`: its address and the link that signs a
// browser in, and with MAGPIE_WEB_SIGN_IN=lazycat the box's address its
// administrators open the page at, signed in to the box (BoxLink).
type Web struct {
	Addr, Link, BoxLink string
	key                 string
	srv                 *http.Server
	quit                chan struct{}
}

// webReexec is set when the page's restart to update has put the new
// version in: Wait then runs it in this one's place.
var webReexec atomic.Bool

// webRunKey hands a run's own key to the version it restarts into, so the
// browser's cookie still opens the page (#111).
const webRunKey = "MAGPIE_WEB_RUNKEY"

// StartWeb serves the page on addr (host:port), as the given version of
// magpie, which keeps itself up to date as the app does. Every request needs the
// key the link carries, taken once into a cookie other sites' pages can't
// send with a POST (every change is one), so neither a page elsewhere nor
// anyone else on the network reaches the settings and keys behind it. On a
// LazyCat box, MAGPIE_WEB_SIGN_IN=lazycat lets the box's administrators in
// without it, signed in to the box (webBoxSignIn).
func StartWeb(addr, version string) (*Web, error) {
	Version = version
	key, fixed, err := webKey()
	if err != nil {
		return nil, err
	}
	if !fixed {
		webRunKeys.Store(key, true)
	}
	box, err := webBoxSignIn()
	if err != nil {
		return nil, err
	}
	// where a browser turned away finds the key
	where := "the k= in the link magpie printed when it started (docker logs shows it for a container, magpie daemon web for the service)"
	if fixed {
		where += ", or MAGPIE_WEB_KEY"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// with --tls-cert, the page is HTTPS to other computers, as the
	// gateway is: it carries the settings and keys
	if ln, err = gateway.ServeTLS(ln); err != nil {
		return nil, err
	}
	w := &Web{Addr: ln.Addr().String(), key: key, quit: make(chan struct{})}
	host, port, _ := net.SplitHostPort(w.Addr)
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	var once sync.Once
	quit := func() { once.Do(func() { close(w.quit) }) }
	// on loopback the page takes plain HTTP with TLS on too, which a
	// browser here opens without a certificate for 127.0.0.1
	scheme := "http"
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		scheme = gateway.Scheme()
	}
	w.Link = scheme + "://" + net.JoinHostPort(host, port) + "/?k=" + url.QueryEscape(key)
	if box != nil {
		w.BoxLink = gateway.LazyCatURL() + "/"
	}
	w.srv = &http.Server{
		Handler:           webServed(port, key, where, box, Handler(webHost{quit}, startBackend())),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go w.srv.Serve(ln)
	// Settings' AutoRestart: magpie web runs on as the new version (#2096)
	updates.autoRestart = func() bool {
		if restartInto(true, false, "", true) {
			quit()
			return true
		}
		return false
	}
	updates.start()
	news.start()
	return w, nil
}

// ServesGateway says whether this process serves the gateway, which it
// doesn't while another magpie (the service, the app, magpie serve) has
// it: the page then shows that one's settings and accounts, read from the
// same files, and this one takes the gateway up once that one is gone.
func (w *Web) ServesGateway() bool { return served.Load() != nil }

// Wait returns once the page's Quit is used. After a restart to update it
// runs the new version in this one's place, with the same arguments and
// key, so the page open in the browser finds it where it was.
func (w *Web) Wait() error {
	<-w.quit
	time.Sleep(200 * time.Millisecond) // the answer to the Quit gets out
	w.Close()
	if !webReexec.Load() {
		return nil
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	// End and reap plugin hosts before replacing this process (#3285).
	plugin.EndHosts()
	return update.Reexec(exe, os.Args[1:], append(os.Environ(), webRunKey+"="+w.key))
}

// Close stops the web page and waits for this process's gateway to save its sessions.
func (w *Web) Close() {
	// a gateway draining (a SIGTERM, gateway.Drain) lets the page port's
	// requests in flight finish too, the /v1 ones an agent sends through it
	// included, for as long as the gateway gives its own
	pageDone := make(chan struct{})
	if gw := served.Load(); gw != nil && gw.Draining() {
		go func() {
			defer close(pageDone)
			ctx, cancel := context.WithTimeout(context.Background(), gw.Grace())
			defer cancel()
			if w.srv.Shutdown(ctx) != nil {
				w.srv.Close()
			}
		}()
	} else {
		w.srv.Close()
		close(pageDone)
	}
	// The gateway keeps idle Claude sessions as it ends. Wait for that
	// before returning or replacing this process, or the next turn has
	// to replay the whole conversation instead of resuming its session.
	stopServing()
	<-pageDone
}

// ServedGateway is the gateway this process serves, nil while another
// magpie has it.
func ServedGateway() *gateway.Server { return served.Load() }

// webKeyMin is the shortest MAGPIE_WEB_KEY taken: it is all that stands
// between the network and the keys behind the page.
const webKeyMin = 16

// webCookieAge is how long a browser keeps the sign-in cookie (the most
// Chrome allows), a run's own key's too: a cookie that went with the
// browser session turned a browser away after it was closed, or a phone
// dropped it, while magpie web ran on with the same key (jenryai on X),
// and the key was back in the start-up log. A cookie that outlives its
// run holds a key no magpie takes any more.
const webCookieAge = 400 * 24 * time.Hour

// webKey is the key the link carries: a new one each run, unless
// MAGPIE_WEB_KEY names one — for a page kept running as a service, which
// browsers then stay signed in to across restarts. fixed says which.
func webKey() (key string, fixed bool, err error) {
	if k := os.Getenv(webRunKey); k != "" {
		os.Unsetenv(webRunKey) // the run's alone, not its children's
		if len(k) == 32 && strings.Trim(k, "0123456789abcdef") == "" && os.Getenv("MAGPIE_WEB_KEY") == "" {
			return k, false, nil
		}
	}
	if k := os.Getenv("MAGPIE_WEB_KEY"); k != "" {
		if err := CheckWebKey(k); err != nil {
			return "", false, err
		}
		return k, true, nil
	}
	return NewWebKey(), false, nil
}

// NewWebKey is a new random key for the page.
func NewWebKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CheckWebKey says what is wrong with k as a MAGPIE_WEB_KEY, nil when
// nothing is.
func CheckWebKey(k string) error {
	if len(k) < webKeyMin {
		return fmt.Errorf("MAGPIE_WEB_KEY is %d characters; it needs at least %d", len(k), webKeyMin)
	}
	// a cookie can't hold a space, ; " \ or a comma: the browser would
	// be sent one that isn't the key, and turned away
	if strings.Trim(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~") != "" {
		return errors.New("MAGPIE_WEB_KEY takes letters, digits and - . _ ~ only (openssl rand -hex 16 makes one)")
	}
	return nil
}

// webGuard lets through requests with the key: in the link's k, which it
// trades for a cookie (kept for webCookieAge) and a clean address, or in
// that cookie; or, when box isn't nil, those it lets in (a LazyCat box's
// administrators, signed in to the box). A page opened without either gets
// a form to paste the key into (webSignInPath), which says why it was
// turned away and where the key is (where, "" for the start-up log's
// link), so a bookmark doesn't send anyone back to the start-up log.
func webGuard(cookie, key, where string, box boxSignIn, next http.Handler) http.Handler {
	if where == "" {
		where = "the k= in the link magpie printed when it started"
	}
	same := func(v string) bool { return subtle.ConstantTimeCompare([]byte(v), []byte(key)) == 1 }
	setCookie := func(rw http.ResponseWriter, r *http.Request) {
		// Priority=High (Chrome's): the page's host is often an address
		// other services share on other ports, and a browser keeps a
		// limited number of cookies for one host, all ports together;
		// past it Chrome drops the low and medium ones first (#2033).
		// Secure over TLS: a cookie isn't kept per port, so without it a
		// browser sends the key in the clear to any plain-HTTP port of
		// this host, where whoever sits on the network (what --tls-cert
		// is against) reads it
		rw.Header().Add("Set-Cookie", (&http.Cookie{Name: cookie, Value: key, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: int(webCookieAge / time.Second)}).String()+"; Priority=High")
	}
	signIn := func(rw http.ResponseWriter, r *http.Request, back string) {
		setCookie(rw, r)
		http.Redirect(rw, r, back, http.StatusSeeOther)
	}
	// passkeys sign in to the key as pasting it does (passkey.go)
	pk := &passkeyPage{key: key}
	_, pk.runs = webRunKeys.Load(key)
	own := ownPageCheck()
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// a plugin's page's files: a sandboxed frame's requests carry no
		// cookie, and its token, which only a signed-in /api hands out,
		// is the key (servePluginPage serves nothing without a good one)
		if strings.HasPrefix(r.URL.Path, pluginPagePrefix) {
			next.ServeHTTP(rw, r)
			return
		}
		if q := r.URL.Query(); q.Has("k") {
			q.Del("k")
			u := *r.URL
			u.RawQuery = q.Encode()
			if !same(r.URL.Query().Get("k")) {
				// a browser gets the form, as without a key: a bare line
				// of text read as a broken page (jenryai on X)
				why := "This link's key is not this magpie web's: the key changed since the link was made, or the link is another magpie's."
				if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
					webTurnedAwayAt(rw, r, u.RequestURI(), why, where, pk)
					return
				}
				http.Error(rw, why+" The key is "+where+".", http.StatusUnauthorized)
				return
			}
			signIn(rw, r, u.RequestURI())
			return
		}
		// what a phone's home screen reads to add the page as an app
		// (index.html's manifest and icons; ninja_s6114 on Discord):
		// fixed bytes of the build, which a browser may ask for without
		// the page's cookie. These paths alone, read alone.
		if webAppFiles[r.URL.Path] && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(rw, r)
			return
		}
		if r.URL.Path == webSignInPath && r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(rw, r.Body, 4096)
			back := localPath(r.PostFormValue("back"))
			if !same(r.PostFormValue("key")) {
				webTurnedAwayAt(rw, r, back, "That key is not this magpie web's.", where, pk)
				return
			}
			signIn(rw, r, back)
			return
		}
		// a passkey's sign-in: a challenge to sign, then the signed
		// answer, which sets the cookie pasting the key does. From the
		// page itself alone, as /api is (webOwnPage).
		if (r.URL.Path == passkeyOptionsPath || r.URL.Path == passkeySignInPath) && r.Method == http.MethodPost {
			if err := own(r); err != nil {
				log.Printf("magpie web: turned away %s from another page (Origin %q): %v", r.URL.Path, r.Header.Get("Origin"), err)
				http.Error(rw, "magpie's sign-in answers magpie's own page only", http.StatusForbidden)
				return
			}
			if r.URL.Path == passkeyOptionsPath {
				pk.signInOptions(rw, r)
				return
			}
			c, back, err := pk.signIn(rw, r)
			if err != nil {
				log.Printf("magpie web: a passkey sign-in from %s was turned away: %v", r.RemoteAddr, err)
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
				return
			}
			log.Printf("magpie web: %s signed in with the passkey %q", r.RemoteAddr, c.Name)
			setCookie(rw, r)
			writeJSON(rw, map[string]string{"back": back})
			return
		}
		// A browser can hold more than one cookie of this name (one set
		// for another path or domain, an old run's): any that carries
		// the key will do, not only the first sent.
		sent := r.CookiesNamed(cookie)
		for _, c := range sent {
			if same(c.Value) {
				if r.TLS != nil && r.Header.Get("Sec-Fetch-Mode") == "navigate" {
					// a cookie set before it was Secure is set again as
					// Secure, in its place, when the page is opened
					setCookie(rw, r)
				}
				// the passkeys of the key it signed in with: a browser
				// signed in with the key alone (not a box's sign-in)
				if r.URL.Path == "/api/passkeys" || strings.HasPrefix(r.URL.Path, "/api/passkeys/") {
					if err := own(r); err != nil {
						http.Error(rw, "magpie's API answers magpie's own page only", http.StatusForbidden)
						return
					}
					pk.routes().ServeHTTP(rw, r)
					return
				}
				next.ServeHTTP(rw, r)
				return
			}
		}
		why, short := "This browser isn't signed in at this address: it sent no sign-in cookie. Opening the link with the key once signs it in, at the address in the link (a name and an IP are two addresses, and a private window is a browser of its own). A browser keeps cookies per host, not per port, so another service on this host's other ports can take the sign-in away: by clearing its site data (a sign-out often does), or by filling the host's cookie limit.", "no cookie sent"
		if len(sent) > 0 {
			why, short = "This browser's sign-in cookie holds a key that isn't this magpie web's: the key changed after it signed in (MAGPIE_WEB_KEY changed, or a start without it made a new one).", "its cookie holds another key"
		}
		lead := "the key is " + where + ". "
		// on a LazyCat box the box's sign-in is the way in, with no
		// cookie of the page's own: it alone says why not
		if box != nil {
			if why, short = box(r); why == "" {
				next.ServeHTTP(rw, r)
				return
			}
			lead = ""
		}
		if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Error(rw, lead+why, http.StatusUnauthorized)
			return
		}
		log.Printf("magpie web: turned away %s from %s: %s", r.URL.Path, r.RemoteAddr, short)
		webTurnedAwayAt(rw, r, r.URL.RequestURI(), why, where, pk)
	})
}

// webAppFiles are the files webGuard serves without the key: the web app
// manifest and the icons it and index.html name, so "Add to Home Screen"
// gets them whether or not the browser sends its cookie with them. Each is
// a file of the build, the same for every magpie; nothing in it is the
// user's.
var webAppFiles = map[string]bool{
	"/manifest.json":              true,
	"/icons/apple-touch-icon.png": true,
	"/icons/icon-512.png":         true,
}

// webSignInPath takes the key pasted into the form a page turned away shows.
const webSignInPath = "/_magpie/sign-in"

// passkeyOptionsPath hands the form's "Sign in with passkey" a challenge;
// passkeySignInPath takes the passkey's signed answer.
const (
	passkeyOptionsPath = "/_magpie/passkey/options"
	passkeySignInPath  = "/_magpie/passkey/sign-in"
)

// localPath is back when it is a path on this server, else "/": the form's
// return address never leads to another site, nor to the form's own.
func localPath(back string) string {
	u, err := url.Parse(back)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") || strings.HasPrefix(back, "/\\") || u.Path == webSignInPath {
		return "/"
	}
	return back
}

// webTurnedAway is the 401 page a browser opening the page without the key
// sees: why, where the key is, and a field for it that signs it in and
// comes back to back. The key itself is never on it.
func webTurnedAway(rw http.ResponseWriter, r *http.Request, back, why, where string) {
	webTurnedAwayAt(rw, r, back, why, where, nil)
}

// webTurnedAwayAt is webTurnedAway with "Sign in with passkey" under the
// field when pk's key has a passkey: the browser shows it where it can
// use one (a secure context at a name, not an IP address), and says what
// it needs where it can't.
func webTurnedAwayAt(rw http.ResponseWriter, r *http.Request, back, why, where string, pk *passkeyPage) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusUnauthorized)
	passkey := ""
	if pk != nil && pk.hasAny() {
		passkey = webPasskeyPart
	}
	fmt.Fprintf(rw, webTurnedAwayPage, webLogo(), html.EscapeString(why), webSignInPath, html.EscapeString(where), html.EscapeString(back), passkey)
}

// webPasskeyPart is the sign-in page's passkey button, hidden until its
// script finds the browser can use a passkey here, and the line saying
// what it needs where it can't. The form's back is where it returns to.
const webPasskeyPart = `<div class="pk"><button type="button" id="pk" class="alt" hidden>Sign in with passkey</button><p id="pkno" class="note" hidden></p><p id="pkerr" class="note err" hidden></p></div>
<script>(function(){
var b=document.getElementById('pk'),no=document.getElementById('pkno'),er=document.getElementById('pkerr'),h=location.hostname;
if(/^[0-9.]+$/.test(h)||h.indexOf(':')>=0||h.charAt(0)==='['||!window.isSecureContext){no.textContent='Passkeys need https at a name: open this page through tailscale serve, a proxy with TLS, or MAGPIE_TLS_CERT.';no.hidden=false;return}
if(!window.PublicKeyCredential||!navigator.credentials)return;
function enc(a){a=new Uint8Array(a);var s='';for(var i=0;i<a.length;i++)s+=String.fromCharCode(a[i]);return btoa(s).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')}
function dec(s){s=atob(s.replace(/-/g,'+').replace(/_/g,'/'));var a=new Uint8Array(s.length);for(var i=0;i<s.length;i++)a[i]=s.charCodeAt(i);return a}
function say(m){er.textContent=m;er.hidden=!m}
b.hidden=false;
b.onclick=async function(){
say('');b.disabled=true;
try{
var r=await fetch('` + passkeyOptionsPath + `',{method:'POST'}),o=await r.json();
if(!r.ok)throw new Error(o.error||r.statusText);
var c=await navigator.credentials.get({publicKey:{challenge:dec(o.challenge),rpId:h,timeout:o.timeout,userVerification:o.userVerification}});
if(!c)return;
var back=document.querySelector('input[name=back]').value;
r=await fetch('` + passkeySignInPath + `',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:c.id,clientDataJSON:enc(c.response.clientDataJSON),authenticatorData:enc(c.response.authenticatorData),signature:enc(c.response.signature),back:back})});
o=await r.json();
if(!r.ok)throw new Error(o.error||r.statusText);
location.replace(o.back||'/');
}catch(e){if(e&&e.name!=='NotAllowedError'&&e.name!=='AbortError')say(e.message||String(e))}
finally{b.disabled=false}
};
})();</script>`

// webTurnedAwayPage is the page webTurnedAway fills in: the logo, why,
// where the key is, the form's action and its return address. It is served
// before the browser is signed in, so it reads none of the page's files
// (webGuard serves them only signed in): its styles are its own, app.css's
// tokens copied (--bg, --card, --line, --fg, --fg-2, --accent and their
// dark values), light or dark as the system is (jenryai on X: a server's
// page turned away read as unstyled).
const webTurnedAwayPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>magpie web</title>
<style>
:root{--bg:#f4f4f6;--card:#fff;--line:#e3e3e8;--fg:#1c1c21;--fg-2:#4b4b53;--accent:#4f46e5;--accent-fg:#fff;--ring:rgba(79,70,229,.22);--edge:0 0 0 1px rgba(0,0,0,.05),0 1px 2px rgba(0,0,0,.04);color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#1a1a1e;--card:#232327;--line:#333339;--fg:#ededf1;--fg-2:#b9b9c1;--accent:#a5b4fc;--accent-fg:#14142a;--ring:rgba(165,180,252,.25);--edge:inset 0 0 0 1px rgba(255,255,255,.06)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px 16px;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI",system-ui,sans-serif;-webkit-font-smoothing:antialiased}
main{width:100%%;max-width:400px}
.brand{display:flex;align-items:center;gap:6px;font-weight:600;letter-spacing:.01em;margin:0 0 14px 2px}
.brand svg{display:block;overflow:visible}.brand .parts{display:none}
.card{background:var(--card);border-radius:12px;box-shadow:var(--edge);padding:20px}
h1{font-size:17px;font-weight:600;margin:0 0 6px}
.why{margin:0 0 16px;color:var(--fg-2);font-size:13px}
label{display:block;font-size:13px;margin:0 0 6px}
.row{display:flex;gap:8px}
input{flex:1;min-width:0;font:inherit;color:var(--fg);background:var(--card);border:1px solid var(--line);border-radius:7px;padding:7px 10px;outline:none;transition:border-color .15s ease,box-shadow .15s ease}
input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--ring)}
button{font:inherit;font-weight:500;color:var(--accent-fg);background:var(--accent);border:0;border-radius:7px;padding:7px 14px;cursor:pointer;transition:transform .12s cubic-bezier(.23,1,.32,1)}
button:active{transform:scale(.97)}
button.alt{display:block;width:100%%;margin-top:12px;color:var(--fg);background:transparent;box-shadow:inset 0 0 0 1px var(--line)}
button:disabled{opacity:.6;cursor:default}
.note{margin:12px 0 0;color:var(--fg-2);font-size:12px}
.note.err{color:#d93025}
@media (prefers-reduced-motion:reduce){button{transition:none}button:active{transform:none}}
</style></head>
<body><main><div class="brand">%s<span>magpie</span></div>
<div class="card"><h1>Sign in</h1><p class="why">%s</p>
<form method="post" action="%s"><label for="key">Paste the key, %s:</label><input type="hidden" name="back" value="%s"><div class="row"><input type="password" id="key" name="key" autocomplete="current-password" autofocus required><button type="submit">Sign in</button></div></form>%s</div></main></body></html>`

// webLogo is the brand's bird as index.html draws it, read once from the
// build's own index.html so the two never differ; "" when it can't be found.
var webLogo = sync.OnceValue(func() string {
	b, err := fs.ReadFile(assets, "assets/index.html")
	if err != nil {
		return ""
	}
	s := string(b)
	i := strings.Index(s, `<span class="logo"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], "<svg")
	k := strings.Index(s[i:], "</svg>")
	if j < 0 || k < j {
		return ""
	}
	return s[i+j : i+k+len("</svg>")]
})

// NetworkLinks are the links to the page, served on port, that other
// machines open; key is the link's "/?k=…". MAGPIE_PUBLIC_URL with a port
// of its own names the host the container's ports are published on, so
// the page is at that host on its own port; one without (a reverse proxy's
// https://magpie.example.com, #372) is where the page is, path and all.
func NetworkLinks(port, key string) []string {
	if u, err := url.Parse(gateway.PublicURL()); err == nil && u.Host != "" && u.Port() == "" {
		return []string{strings.TrimRight(u.String(), "/") + key}
	}
	var out []string
	for _, a := range LANAddrs() {
		out = append(out, gateway.Scheme()+"://"+net.JoinHostPort(a, port)+key)
	}
	return out
}

// LANAddrs are this computer's addresses on the local network, for the
// links to print when the page is served there: MAGPIE_PUBLIC_URL's host
// when set (in a container, whose own addresses the network can't reach),
// else the gateway's own (gateway.LANHosts), so Settings lists one
// computer's addresses the same way twice.
func LANAddrs() []string {
	if h := gateway.PublicHost(); h != "" {
		return []string{h}
	}
	return gateway.LANHosts()
}

// webServed is all `magpie web` on port serves: the gateway's client
// routes, judged by the gateway (onePort) and never by box, and the page
// and its API behind the allow list and the key, or box's sign-in.
func webServed(port, key, where string, box boxSignIn, page http.Handler) http.Handler {
	return closesWhenDraining(onePort(gateway.AllowGuard(gateway.ClientCertGuard(webHandler(port, key, where, box, page)))))
}

// closesWhenDraining has each reply the page port gives while this
// process's gateway drains (a SIGTERM) close its connection, as the
// gateway's own do: an agent that sends /v1 here and kept its connection
// alive then dials the Service's next pod for its next request, rather
// than sending it on a connection the drain's end closes.
func closesWhenDraining(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gw := served.Load(); gw != nil && gw.Draining() {
			w.Header().Set("Connection", "close")
		}
		h.ServeHTTP(w, r)
	})
}

// webHandler is what `magpie web` on port serves: the page and its API
// behind the key, or box's sign-in, compressed for the browser.
func webHandler(port, key, where string, box boxSignIn, page http.Handler) http.Handler {
	return webCompressed(webGuard("magpie_web_"+port, key, where, box, webOwnPage(page)))
}

// webOwnPage turns away a request for /api that another page sent with
// the page's cookie. The cookie is SameSite=Lax, so a browser sends it
// with any request one site sends another, and a site is the registrable
// domain without the port: a page on another port of 127.0.0.1 or of the
// LAN address, or on a sibling name behind the same reverse proxy, could
// POST a stdio MCP server to /api/library/servers/save as text/plain in
// no-cors mode and start it with /api/library/mcp/check, which is code run
// as the user. Only the page itself may write: a write (any method but
// GET, HEAD and OPTIONS) is judged by net/http's CrossOriginProtection,
// which takes the browser's Sec-Fetch-Site (same-origin, or none for what
// the user did) and, from a browser too old to send it, an Origin that is
// absent or names the request's own Host or MAGPIE_PUBLIC_URL. A read is
// turned away too when the browser says another origin sent it, so that
// no GET route that does something (an import read once, a sign-in
// started) is any page's to call, whether or not it answers.
//
// The desktop window's /api is served in the process, over no port, and
// has no cookie for another page to borrow, so this is for the browser's
// page alone.
func webOwnPage(next http.Handler) http.Handler {
	own := ownPageCheck()
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(rw, r)
			return
		}
		if err := own(r); err != nil {
			log.Printf("magpie web: turned away %s %s from another page (Origin %q): %v", r.Method, r.URL.Path, r.Header.Get("Origin"), err)
			http.Error(rw, "magpie's API answers magpie's own page only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// ownPageCheck is webOwnPage's judge: nil for a request the page itself
// sent, else why not.
func ownPageCheck() func(*http.Request) error {
	cop := http.NewCrossOriginProtection()
	if u, err := url.Parse(gateway.PublicURL()); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		// behind a proxy that passes on a Host of its own, the page's
		// address is the one the user named
		if err := cop.AddTrustedOrigin(u.Scheme + "://" + u.Host); err != nil {
			log.Printf("magpie web: MAGPIE_PUBLIC_URL %s is no origin: %v", u, err)
		}
	}
	return func(r *http.Request) error {
		if err := cop.Check(r); err != nil {
			return err
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			return errors.New("Sec-Fetch-Site " + site)
		}
		return nil
	}
}

// webCompressed gzips what `magpie web` sends a browser that takes it: the
// page's scripts, styles and SVG icons, and the API's JSON. They went out
// as they are, about 4 MB for a first load before any data, and the
// routing trace's JSON grows with every account a request could go to; a
// browser over a 3 Mbps link to a server waited for all of it (akic404 on
// Discord). They shrink to about a quarter, the JSON to a twentieth.
// Pictures already compressed go as they are.
//
// The app's own window gets the page from inside the process, with no
// network between, so only `magpie web` compresses.
//
// A gzipped file's ETag says so ("…-gzip"), as a validator of other bytes;
// asked for again with it, the file is still a 304. A few bytes of padding
// keyed to the content keep a response's compressed size from telling
// exactly what it holds (BREACH): the page carries keys.
func webCompressed(next http.Handler) http.Handler {
	wrap, err := gzhttp.NewWrapper(
		gzhttp.ContentTypeFilter(compressible),
		gzhttp.SuffixETag(gzipETag),
		gzhttp.RandomJitter(32, 0, false),
	)
	if err != nil {
		return next
	}
	gz := wrap(next)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if tag := r.Header.Get("If-None-Match"); strings.Contains(tag, gzipETag+`"`) {
			r.Header.Set("If-None-Match", strings.ReplaceAll(tag, gzipETag+`"`, `"`))
		}
		gz.ServeHTTP(rw, r)
	})
}

// gzipETag ends the ETag of a gzipped response.
const gzipETag = "-gzip"

// compressible: text, scripts, JSON and SVG; not PNG, WebP, ICO or JPEG,
// and not an event stream, which gzip holds back until a block fills (a
// plugin page's service streams a chat's tokens, servePageService).
func compressible(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct == "text/event-stream" {
		return false
	}
	return strings.HasPrefix(ct, "text/") || strings.HasSuffix(ct, "javascript") ||
		strings.HasSuffix(ct, "json") || ct == "image/svg+xml"
}

// browserTab is a Windows that stands for a browser tab: magpie web's
// (webHost) or one on the page the app serves (appWebHost).
type browserTab interface{ browserTab() }

func (webHost) browserTab() {}

// isWeb: the page is served to a browser, by `magpie web` or by the app.
func isWeb(w Windows) bool { _, ok := w.(browserTab); return ok }

// ownWeb: the page is `magpie web`'s own (its gateway mode, its restart
// to update), not the app's served to a browser.
func ownWeb(w Windows) bool { _, ok := w.(webHost); return ok }

// onePort hands the gateway's client routes (gateway.ClientPath: /v1/…,
// /v1beta/…, /mcp/…) on the page's port to the gateway, so the page and the
// agents share one address and a reverse proxy needs one location for both
// (zgcwkj on Discord). Each such request is judged by the gateway's own
// guards (Server.Front), not the page's key, and always as one from
// another machine (gateway.FromPagePort): it needs a gateway key and
// magpie shared, from loopback too. This port is the one put behind a
// proxy or a forward, and a raw TCP forward or a proxy that adds no
// forwarding header arrives from loopback looking like an agent here
// (reported privately by Coffemon). Agents here use the gateway's own
// port. Everything else (the page, its /api/*) stays behind the page's
// allow list and key.
func onePort(page http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !gateway.ClientPath(r.URL.Path) {
			page.ServeHTTP(rw, r)
			return
		}
		r = gateway.FromPagePort(r)
		if gw := served.Load(); gw != nil {
			gw.Front().ServeHTTP(rw, r)
			return
		}
		toGateway.ServeHTTP(rw, r)
	})
}

// toGateway passes a client route on to the gateway another magpie serves
// here (the service, the app), as it came: its Host, so a rebound page's
// name is still refused, and its forwarding headers, so a request a proxy
// passed on is still one from another machine. It carries the page
// port's mark (gateway.PagePortHeader, set by onePort), so the gateway
// judges it as from another machine wherever it came from; and it always
// carries an X-Forwarded-For (the peer's address added), so a gateway of
// an older magpie, which doesn't know the mark, still does.
var toGateway = &httputil.ReverseProxy{
	Rewrite: func(pr *httputil.ProxyRequest) {
		u, _ := url.Parse(gateway.URL())
		pr.SetURL(u)
		pr.Out.Host = pr.In.Host
		// Rewrite starts from a request with these taken off
		for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			if v := pr.In.Header.Values(h); len(v) > 0 {
				pr.Out.Header[h] = append([]string(nil), v...)
			}
		}
		if !loopbackPeer(pr.In.RemoteAddr) {
			pr.SetXForwarded()
		} else if pr.Out.Header.Get("X-Forwarded-For") == "" {
			h, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
			pr.Out.Header.Set("X-Forwarded-For", h)
		}
	},
	FlushInterval: -1, // streams as they come
	ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
		log.Printf("magpie web: %s %s: the gateway at %s: %v", r.Method, r.URL.Path, gateway.URL(), err)
		http.Error(rw, "magpie's gateway at "+gateway.URL()+" doesn't answer: "+err.Error(), http.StatusBadGateway)
	},
}

// loopbackPeer: addr (a RemoteAddr) is on this computer's loopback.
func loopbackPeer(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(h)
	return err == nil && ip != nil && ip.IsLoopback()
}
