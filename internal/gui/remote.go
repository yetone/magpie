package gui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The app opens another magpie's page in a window of its own: a magpie on
// a server (`magpie daemon install --web`, `magpie web`, or an app with
// Settings › Network and sharing › Browser access on), whose routing and
// usage the user reads from the desktop (xuanwo on X). The window is that
// magpie's own web page, signed in with its own key as a browser is
// (webGuard there): nothing is served here, and the page in the window
// talks to that magpie alone. The address and key live in
// remote-magpie.json beside settings.json, this computer's alone, never
// in settings.json: a sync or a backup never carries the key elsewhere.

// remoteFile is where the other magpie's address and key are kept, 0600.
func remoteFile() string { return filepath.Join(settings.Dir(), "remote-magpie.json") }

// remoteConf is remote-magpie.json: URL is the page's address without the
// key, Key the key its link carries.
type remoteConf struct {
	URL string `json:"url,omitempty"`
	Key string `json:"key,omitempty"`
}

// loadRemote reads remote-magpie.json; none is an empty conf.
func loadRemote() (remoteConf, error) {
	var c remoteConf
	b, err := os.ReadFile(remoteFile())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return remoteConf{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return remoteConf{}, fmt.Errorf("%s: %w", remoteFile(), err)
	}
	// an address saved with its scheme twice reads as one
	if c.URL != "" {
		c.URL = provider.WithScheme(c.URL, "https")
	}
	return c, nil
}

// saveRemote writes remote-magpie.json readable by its owner alone: the
// key opens every setting and key the other magpie has.
func saveRemote(c remoteConf) error {
	p := remoteFile()
	if c == (remoteConf{}) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := edit.WriteAtomic(p, append(b, '\n')); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// remoteAddr reads what was typed as the other magpie's address: the link
// it printed (with ?k=, which is taken out as the key) or the bare
// address, https:// when no scheme is named. Plain http is taken only
// where the key doesn't cross the internet in the clear (plainHTTP);
// tailnet says the way there is Tailscale's, which encrypts it.
func remoteAddr(raw string) (addr, key string, tailnet bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false, errors.New("type the other magpie's address")
	}
	raw = provider.WithScheme(raw, "https")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", "", false, fmt.Errorf("%q isn't an address such as https://magpie.example.com", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false, fmt.Errorf("the address is http:// or https://, not %s://", u.Scheme)
	}
	if u.User != nil {
		return "", "", false, errors.New("the address can't carry a user name or password: the key signs in")
	}
	if u.Scheme == "http" {
		if tailnet, err = plainHTTP(u.Hostname()); err != nil {
			return "", "", false, err
		}
	}
	q := u.Query()
	key = q.Get("k")
	q.Del("k")
	u.RawQuery = q.Encode()
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), key, tailnet, nil
}

// tailnetNets are Tailscale's addresses: 100.64/10 and fd7a:115c:a1e0::/48.
var tailnetNets = []*net.IPNet{
	{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)},
	{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)},
}

func onTailnet(ip net.IP) bool {
	for _, n := range tailnetNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteLookup resolves a name as the window will; a var for tests.
var remoteLookup = func(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// remoteOS is the system whose webview opens the window; a var for tests.
var remoteOS = runtime.GOOS

// plainHTTP judges a page on host reached over plain http: nil where the
// key stays off the internet, tailnet when Tailscale carries it (its
// WireGuard encrypts the way, @baysonfox on X). Allowed: localhost, a
// .local or one-word name, a private, loopback or link-local address, a
// Tailscale address, and a .ts.net name that resolves to Tailscale
// addresses alone, as MagicDNS answers on the tailnet; a .ts.net name
// that resolves elsewhere (Funnel's public relays) or not at all is on
// the internet. A Mac's webview (App Transport Security) refuses http to
// any other name and to a 100.64/10 or public address, whatever this
// check says, so there those are refused here; Info.plist excepts ts.net.
// Elsewhere a public address still passes (#7488).
func plainHTTP(host string) (tailnet bool, err error) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	internet := fmt.Errorf("%s is a name on the internet: open it over https://, or the key crosses the network unencrypted", host)
	if h == "localhost" || strings.HasSuffix(h, ".local") || !strings.Contains(h, ".") && net.ParseIP(h) == nil {
		return false, nil
	}
	if ip := net.ParseIP(h); ip != nil {
		switch {
		case onTailnet(ip) && ip.To4() != nil && remoteOS == "darwin":
			return false, fmt.Errorf("%s is a Tailscale address, which a Mac opens over https:// only: type the machine's .ts.net name, or open it over https://", host)
		case onTailnet(ip):
			return true, nil
		case ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast():
			return false, nil
		case remoteOS == "darwin":
			return false, internet
		}
		return false, nil
	}
	if !strings.HasSuffix(h, ".ts.net") {
		return false, internet
	}
	ips, lerr := remoteLookup(h)
	if lerr != nil || len(ips) == 0 {
		return false, fmt.Errorf("%s isn't reached through Tailscale from this computer: turn Tailscale on, or open it over https://", host)
	}
	for _, ip := range ips {
		if !onTailnet(ip) {
			return false, fmt.Errorf("%s isn't reached through Tailscale from this computer: turn Tailscale on, or open it over https://", host)
		}
	}
	return true, nil
}

// remoteLink is the address with the key, which the page trades for its
// cookie and takes out of the address bar (webGuard).
func remoteLink(c remoteConf) string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("k", c.Key)
	u.RawQuery = q.Encode()
	return u.String()
}

// remoteProbe asks the other magpie whether it takes the key, as the
// window's first load would, so a wrong address or key is said in Settings
// rather than as a window with an error page. It reads nothing behind the
// key: webGuard answers a link with the key with a redirect and a cookie,
// and one without it with 401.
var remoteProbe = func(c remoteConf) error {
	cl := &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, remoteLink(c), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/html")
	resp, err := cl.Do(req)
	if err != nil {
		var cert *tls.CertificateVerificationError
		if errors.As(err, &cert) {
			return fmt.Errorf("%s's certificate isn't trusted by this computer: %v", req.URL.Host, cert.Err)
		}
		return fmt.Errorf("%s doesn't answer: %v", req.URL.Host, errors.Unwrap(err))
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusSeeOther && remoteSignedIn(resp):
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("that magpie turned the key away: use the key in the link it printed (MAGPIE_WEB_KEY, or the k= in it)")
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("that magpie doesn't let this computer in: add this computer's address to its allowed addresses")
	}
	return fmt.Errorf("%s answered %s, not as a magpie page: check the address and port (magpie web's is 3430)", req.URL.Host, resp.Status)
}

// remoteSignedIn: the answer sets magpie web's cookie.
func remoteSignedIn(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "magpie_web_") {
			return true
		}
	}
	return false
}

// remoteOpener is a host that can open the other magpie's window: the
// app's, not a browser's.
type remoteOpener interface {
	OpenRemote(link, title string)
}

// remoteJSON is GET /api/remote: the address kept, and whether a key is.
// The key itself never goes back to the page.
type remoteJSON struct {
	URL    string `json:"url"`
	HasKey bool   `json:"hasKey"`
	// Tailnet: the address just opened is plain http over Tailscale.
	Tailnet bool `json:"tailnet,omitempty"`
}

// remoteRoutes: GET /api/remote says what is kept; POST takes {"url",
// "key"}, checks that magpie takes the key, keeps both and opens its
// window (a key left out keeps the one kept for the same address);
// POST /api/remote/forget forgets them. The app's own windows' alone.
func remoteRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/remote", func(rw http.ResponseWriter, r *http.Request) {
		c, err := loadRemote()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, remoteJSON{URL: c.URL, HasKey: c.Key != ""})
	})
	mux.HandleFunc("POST /api/remote", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			URL string `json:"url"`
			Key string `json:"key"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 8192)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		addr, key, tailnet, err := remoteAddr(in.URL)
		if err != nil {
			fail(rw, err)
			return
		}
		if k := strings.TrimSpace(in.Key); k != "" {
			key = k
		}
		if key == "" {
			if old, err := loadRemote(); err == nil && old.URL == addr {
				key = old.Key
			}
		}
		if key == "" {
			fail(rw, errors.New("type that magpie's key: the k= in the link it printed, or its MAGPIE_WEB_KEY"))
			return
		}
		if err := CheckWebKey(key); err != nil {
			fail(rw, errors.New(strings.Replace(err.Error(), "MAGPIE_WEB_KEY", "the key", 1)))
			return
		}
		o, ok := w.(remoteOpener)
		if !ok {
			fail(rw, errors.New("only the app opens another magpie in a window"))
			return
		}
		c := remoteConf{URL: addr, Key: key}
		if err := remoteProbe(c); err != nil {
			fail(rw, err)
			return
		}
		if err := saveRemote(c); err != nil {
			fail(rw, err)
			return
		}
		u, _ := url.Parse(addr)
		o.OpenRemote(remoteLink(c), "magpie · "+u.Host)
		writeJSON(rw, remoteJSON{URL: c.URL, HasKey: true, Tailnet: tailnet})
	})
	mux.HandleFunc("POST /api/remote/forget", func(rw http.ResponseWriter, r *http.Request) {
		if err := saveRemote(remoteConf{}); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, remoteJSON{})
	})
}
