package gui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/gateway"
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
func (webHost) OpenURL(string)                   {} // the page opens links itself
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

// Web is a started `magpie web`: its address and the link that signs a
// browser in.
type Web struct {
	Addr, Link string
	key        string
	srv        *http.Server
	quit       chan struct{}
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
// anyone else on the network reaches the settings and keys behind it.
func StartWeb(addr, version string) (*Web, error) {
	Version = version
	key, fixed, err := webKey()
	if err != nil {
		return nil, err
	}
	var keep time.Duration
	if fixed {
		keep = webCookieAge
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	w := &Web{Addr: ln.Addr().String(), key: key, quit: make(chan struct{})}
	host, port, _ := net.SplitHostPort(w.Addr)
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	var once sync.Once
	quit := func() { once.Do(func() { close(w.quit) }) }
	w.Link = "http://" + net.JoinHostPort(host, port) + "/?k=" + url.QueryEscape(key)
	w.srv = &http.Server{
		Handler:           webGuard("magpie_web_"+port, key, keep, Handler(webHost{quit}, startBackend())),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go w.srv.Serve(ln)
	updates.start()
	return w, nil
}

// Wait returns once the page's Quit is used. After a restart to update it
// runs the new version in this one's place, with the same arguments and
// key, so the page open in the browser finds it where it was.
func (w *Web) Wait() error {
	<-w.quit
	time.Sleep(200 * time.Millisecond) // the answer to the Quit gets out
	w.srv.Close()
	if !webReexec.Load() {
		return nil
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	return update.Reexec(exe, os.Args[1:], append(os.Environ(), webRunKey+"="+w.key))
}

// webKeyMin is the shortest MAGPIE_WEB_KEY taken: it is all that stands
// between the network and the keys behind the page.
const webKeyMin = 16

// webCookieAge is how long a browser keeps a fixed key's cookie (the most
// Chrome allows); a run's own key lives in a cookie that goes with the
// browser session, as the key goes with the run.
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
		if len(k) < webKeyMin {
			return "", false, fmt.Errorf("MAGPIE_WEB_KEY is %d characters; it needs at least %d", len(k), webKeyMin)
		}
		// a cookie can't hold a space, ; " \ or a comma: the browser would
		// be sent one that isn't the key, and turned away
		if strings.Trim(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~") != "" {
			return "", false, errors.New("MAGPIE_WEB_KEY takes letters, digits and - . _ ~ only (openssl rand -hex 16 makes one)")
		}
		return k, true, nil
	}
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b), false, nil
}

// webGuard lets through requests with the key: in the link's k, which it
// trades for a cookie (kept for keep, or the browser session when 0) and
// a clean address, or in that cookie.
func webGuard(cookie, key string, keep time.Duration, next http.Handler) http.Handler {
	same := func(v string) bool { return subtle.ConstantTimeCompare([]byte(v), []byte(key)) == 1 }
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Has("k") {
			if !same(q.Get("k")) {
				http.Error(rw, "this link's key is not this magpie web's: use the link it printed when it started", http.StatusUnauthorized)
				return
			}
			http.SetCookie(rw, &http.Cookie{Name: cookie, Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(keep / time.Second)})
			q.Del("k")
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(rw, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(cookie); err != nil || !same(c.Value) {
			http.Error(rw, "open the link magpie web printed when it started: it carries the key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// LANAddrs are this computer's addresses on the local network, for the
// links to print when the page is served there: MAGPIE_PUBLIC_URL's host
// when set (in a container, whose own addresses the network can't reach).
func LANAddrs() []string {
	if h := gateway.PublicHost(); h != "" {
		return []string{h}
	}
	var out []string
	as, _ := net.InterfaceAddrs()
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || !n.IP.IsPrivate() {
			continue
		}
		out = append(out, n.IP.String())
	}
	return out
}

// isWeb: the page is served to a browser by `magpie web`.
func isWeb(w Windows) bool { _, ok := w.(webHost); return ok }
