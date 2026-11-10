package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// The app serves its window's page to a browser too, when Settings ›
// Network and sharing › Browser access says so (jiakun_zhao on X: the page
// managed through a Cloudflare tunnel took `magpie web` kept running in a
// terminal beside the app). It is magpie web's page, served from the app's
// own process, beside the gateway the app serves: the same key in the
// link, traded for the same cookie (webGuard), the same allow list, and
// the gateway's client routes on its port judged as from another machine
// (onePort). There is no way to serve it without the key.
//
// "local" listens on 127.0.0.1 alone, for a tunnel or an SSH forward to
// reach (cloudflared's service is http://127.0.0.1:<port>); "lan" on every
// address, for other computers on the network. Its port and key live in
// web-page.json beside settings.json, this computer's alone, never in
// settings.json: a sync or a backup never carries the key to another
// computer, nor turns the page on there.

// appPageFile is where the app's page's choice and key are kept, 0600.
func appPageFile() string { return filepath.Join(settings.Dir(), "web-page.json") }

// appPageDefaultPort is magpie web's own port: a browser signed in to one
// is signed in to the other where they share the key.
const appPageDefaultPort = 3430

// appPageConf is web-page.json: Mode "" (off), "local" or "lan"; Port 0
// for appPageDefaultPort; Key the link's.
type appPageConf struct {
	Mode string `json:"mode,omitempty"`
	Port int    `json:"port,omitempty"`
	Key  string `json:"key,omitempty"`
}

func (c appPageConf) port() int {
	if c.Port == 0 {
		return appPageDefaultPort
	}
	return c.Port
}

// addr is where the page listens, "" when it is off.
func (c appPageConf) addr() string {
	switch c.Mode {
	case "local":
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port()))
	case "lan":
		return net.JoinHostPort("0.0.0.0", strconv.Itoa(c.port()))
	}
	return ""
}

// loadAppPage reads web-page.json. A file that can't be read or parsed is
// off, and says why: nothing is served on a choice not known.
func loadAppPage() (appPageConf, error) {
	var c appPageConf
	b, err := os.ReadFile(appPageFile())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return appPageConf{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return appPageConf{}, fmt.Errorf("%s: %w", appPageFile(), err)
	}
	if c.Mode != "" && c.Mode != "local" && c.Mode != "lan" {
		return appPageConf{}, fmt.Errorf("%s: mode is local, lan or off, not %q", appPageFile(), c.Mode)
	}
	return c, nil
}

// saveAppPage writes web-page.json readable by its owner alone: it holds
// the key that opens every setting and key magpie has.
func saveAppPage(c appPageConf) error {
	p := appPageFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// made 0600 before anything is in it, so WriteAtomic keeps that mode
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := edit.WriteAtomic(p, append(b, '\n')); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// AppPage is where the app serves its page and its key, "" when it doesn't
// say it does: `magpie web` points to it rather than starting a second.
func AppPage() (addr, key string) {
	c, err := loadAppPage()
	if err != nil || c.addr() == "" || CheckWebKey(c.Key) != nil {
		return "", ""
	}
	return c.addr(), c.Key
}

// appWebHost is the Windows a browser on the app's page stands for: a
// browser's (webHost's) in all but quitting, which quits the app, and the
// window the app has up, which a restart to update brings back.
type appWebHost struct {
	webHost
	app Windows
}

func (a appWebHost) MainShown() bool { return mainShown(a.app) }

// appPage is the app's page served to a browser: listening where
// web-page.json says, or trying again every appPageRetry while another
// program (magpie web, the magpie before a restart to update) has the port.
type appPage struct {
	mu   sync.Mutex
	mk   func() http.Handler // the page behind the key; nil until the app starts
	page http.Handler
	srv  *http.Server
	addr string                       // where it listens, "" when it doesn't
	cur  atomic.Pointer[http.Handler] // webServed with the key now
	key  string
	err  string // why it isn't served where asked
	wake chan struct{}
}

var appPageServer = &appPage{wake: make(chan struct{}, 1)}

// appPageRetry is how often a busy port is tried again.
var appPageRetry = 4 * time.Second

// startAppPage has the app serve its page as web-page.json says, now and
// whenever Settings changes it. app is the app's own windows; gw the
// gateway it serves (nil while another magpie has it).
func startAppPage(app Windows, gw *gateway.Server) {
	p := appPageServer
	p.mu.Lock()
	p.mk = func() http.Handler { return Handler(appWebHost{webHost{quit: app.Quit}, app}, gw) }
	p.mu.Unlock()
	go p.run()
}

func (p *appPage) run() {
	for {
		p.apply()
		var retry <-chan time.Time
		if p.waiting() {
			retry = time.After(appPageRetry)
		}
		select {
		case <-p.wake:
		case <-retry:
		}
	}
}

// nudge has the loop look again, so a port found busy now is tried again.
func (p *appPage) nudge() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// waiting: the page is asked for and not served.
func (p *appPage) waiting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err != "" && p.mk != nil
}

// apply brings what is served to what web-page.json says.
func (p *appPage) apply() {
	c, err := loadAppPage()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mk == nil {
		return
	}
	want := c.addr()
	if err != nil {
		want, p.err = "", err.Error()
	}
	if p.srv != nil && p.addr != want {
		p.srv.Close()
		p.srv, p.addr = nil, ""
	}
	if want == "" {
		if err == nil {
			p.err = ""
		}
		return
	}
	if CheckWebKey(c.Key) != nil {
		p.err = "the page has no key yet"
		return
	}
	if p.page == nil {
		p.page = p.mk()
	}
	if p.key != c.Key || p.cur.Load() == nil {
		h := webServed(strconv.Itoa(c.port()), c.Key, webCookieAge, nil, p.page)
		p.cur.Store(&h)
		p.key = c.Key
	}
	if p.srv != nil {
		return
	}
	ln, err := net.Listen("tcp", want)
	if err != nil {
		p.err = busySays(c.port(), err)
		return
	}
	// with --tls-cert the page is HTTPS to other computers, as magpie web's
	if ln, err = gateway.ServeTLS(ln); err != nil {
		p.err = err.Error()
		return
	}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { (*p.cur.Load()).ServeHTTP(rw, r) }),
		ReadHeaderTimeout: 30 * time.Second,
	}
	p.addr, p.err = want, ""
	go p.srv.Serve(ln)
	log.Printf("magpie: the page is served to browsers on %s", ln.Addr())
}

// busySays is why the page couldn't listen on port: most often another
// program has it (magpie web, or the magpie before a restart to update).
func busySays(port int, err error) string {
	return fmt.Sprintf("port %d can't be taken (%v): magpie tries again every few seconds", port, err)
}

// appPageJSON is GET /api/web-page: the choice, and what came of it.
type appPageJSON struct {
	Mode    string   `json:"mode"`
	Port    int      `json:"port"`
	Serving bool     `json:"serving"`
	Link    string   `json:"link,omitempty"`    // this computer's, with the key
	Network []string `json:"network,omitempty"` // other computers', with the key ("lan")
	Error   string   `json:"error,omitempty"`
	// TLS: other computers get it over HTTPS (--tls-cert)
	TLS bool `json:"tls,omitempty"`
}

func (p *appPage) state() appPageJSON {
	c, err := loadAppPage()
	p.mu.Lock()
	defer p.mu.Unlock()
	s := appPageJSON{Mode: c.Mode, Port: c.port(), Serving: p.srv != nil && p.addr == c.addr(), Error: p.err, TLS: gateway.TLSOn()}
	if err != nil {
		s.Error = err.Error()
	}
	if c.Mode == "" || CheckWebKey(c.Key) != nil {
		return s
	}
	k := "/?k=" + url.QueryEscape(c.Key)
	s.Link = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port())) + k
	if c.Mode == "lan" {
		s.Network = NetworkLinks(strconv.Itoa(c.port()), k)
	}
	return s
}

// appPageRoutes: GET /api/web-page says what is served; POST takes
// {"mode": "" | "local" | "lan", "port": n, "newKey": true}, each part
// optional, and answers the same. The app's own windows' alone: a browser
// on the page can't turn it off under itself, nor read the key it came in
// without.
func appPageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/web-page", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, appPageServer.state())
	})
	mux.HandleFunc("POST /api/web-page", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode   *string `json:"mode"`
			Port   *int    `json:"port"`
			NewKey bool    `json:"newKey"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		c, err := loadAppPage()
		if err != nil {
			// a file that can't be read is written anew only when the
			// whole choice is given
			if in.Mode == nil {
				fail(rw, err)
				return
			}
			c = appPageConf{}
		}
		if in.Mode != nil {
			switch *in.Mode {
			case "", "local", "lan":
				c.Mode = *in.Mode
			default:
				fail(rw, fmt.Errorf("browser access is off, local or lan, not %q", *in.Mode))
				return
			}
		}
		if in.Port != nil {
			if err := appPagePortOK(*in.Port); err != nil {
				fail(rw, err)
				return
			}
			c.Port = *in.Port
			if c.Port == appPageDefaultPort {
				c.Port = 0
			}
		}
		if in.NewKey || CheckWebKey(c.Key) != nil {
			c.Key = NewWebKey()
		}
		if err := saveAppPage(c); err != nil {
			fail(rw, err)
			return
		}
		appPageServer.apply()
		appPageServer.nudge()
		writeJSON(rw, appPageServer.state())
	})
}

// appPagePortOK says what is wrong with port for the page: it is a port of
// its own, 1024 up, not the gateway's.
func appPagePortOK(port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("a port is a number from 1024 to 65535")
	}
	if u, err := url.Parse(gateway.URL()); err == nil && u.Port() == strconv.Itoa(port) {
		return fmt.Errorf("port %d is the gateway's: pick another one", port)
	}
	return nil
}
