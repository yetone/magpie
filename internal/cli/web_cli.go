package cli

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/daemon"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/stats"
)

// webAddr is where `magpie web` listens unless told: beside the gateway.
const webAddr = "127.0.0.1:3430"

// webCmd: magpie web [--addr host:port] [--lan] [--no-open] [--gateway]
// [--tls-cert <file> --tls-key <file>] —
// the app's window in a browser, for a computer that can't show the app;
// --gateway shows it in gateway mode (gui.WebGateway) unless Settings says
// otherwise; with a certificate the page and the gateway are HTTPS to
// other computers.
func webCmd(args []string) error {
	args, err := tlsFlags(args)
	if err != nil {
		return err
	}
	addr, lan, open := webAddr, false, true
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--addr" && i+1 < len(args):
			i++
			addr = args[i]
		case strings.HasPrefix(a, "--addr="):
			addr = strings.TrimPrefix(a, "--addr=")
		case a == "--lan":
			lan = true
		case a == "--no-open":
			open = false
		case a == "--gateway":
			gui.WebGateway.Store(true)
		default:
			return fmt.Errorf("magpie web: unknown %q · magpie web [--addr host:port] [--lan] [--no-open] [--gateway] [--tls-cert <file> --tls-key <file>], MAGPIE_WEB_KEY to keep one key", a)
		}
	}
	if lan && addr == webAddr {
		_, port, _ := net.SplitHostPort(webAddr)
		addr = "0.0.0.0:" + port
	}
	// the service serves the page already: its own, beside the gateway
	// it serves, is the one to open
	if a, k := daemon.Web(); a != "" && gateway.ServedBy().Running {
		link := webLink(a, k)
		fmt.Println(green.Render("●"), "the magpie service serves this page:", bold.Render(link))
		fmt.Println(muted.Render("  magpie daemon web off turns it off"))
		if open {
			openInBrowser(link)
		}
		return nil
	}
	// the app serves this page on this port already (Settings › Network
	// and sharing › Browser access): its own is the one to open
	if a, k := gui.AppPage(); a != "" && samePort(a, addr) && answers(a) {
		link := webLink(a, k)
		fmt.Println(green.Render("●"), "the magpie app serves this page:", bold.Render(link))
		fmt.Println(muted.Render("  Settings › Network and sharing › Browser access turns it off"))
		if open {
			openInBrowser(link)
		}
		return nil
	}
	// what this page's gateway starts (agents, MCP servers) gets the node a
	// version manager put on PATH only in the shell's profile, as the app's
	// does: a service or autostart has none of it (#3803)
	proc.UserPath()
	w, err := gui.StartWeb(addr, version)
	if err != nil {
		return err
	}
	if !w.ServesGateway() {
		// another magpie has the gateway (the service, magpie serve, the
		// app): this page shows its settings and accounts, read from the
		// same files, and takes the gateway up when that one stops
		o := gateway.ServedBy()
		who := "another magpie"
		if o.Version != "" {
			who += " (" + o.Version + ")"
		}
		fmt.Println(muted.Render("  the gateway on " + gateway.URL() + " is served by " + who + "; this page shows the same settings and accounts, and serves the gateway once that one stops"))
		if st, err := daemon.Get(); err == nil && st.Running {
			fmt.Println(muted.Render("  to have the service serve this page itself: magpie daemon web on"))
		}
	}
	printWeb(w)
	if open {
		openInBrowser(w.Link)
	}
	return w.Wait()
}

// samePort: a and b (host:port) name one port.
func samePort(a, b string) bool {
	_, pa, ea := net.SplitHostPort(a)
	_, pb, eb := net.SplitHostPort(b)
	return ea == nil && eb == nil && pa == pb
}

// answers: something listens on addr's port on this computer.
func answers(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// printWeb prints where a started page is, and what its link carries.
func printWeb(w *gui.Web) {
	fmt.Println(green.Render("●"), "magpie web on", bold.Render(w.Link))
	if w.BoxLink != "" {
		fmt.Println(muted.Render("  the LazyCat box's administrators, signed in to the box, open"), w.BoxLink, muted.Render("without the key"))
	}
	if l := tlsLine(); l != "" {
		fmt.Println(l)
	}
	host, port, _ := net.SplitHostPort(w.Addr)
	key := w.Link[strings.Index(w.Link, "/?k="):]
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		for _, l := range gui.NetworkLinks(port, key) {
			fmt.Println(muted.Render("  on the network"), l)
		}
		if gateway.ContainerAddrs() {
			fmt.Println(muted.Render("  " + containerNote))
		}
		if l := containerLine(); l != "" {
			fmt.Println(l)
		}
		fmt.Println(amber.Render("!"), lanWarning())
	} else if l := proxyLink(key); l != "" {
		fmt.Println(muted.Render("  through your proxy"), l)
	}
	carries := "this run's key (MAGPIE_WEB_KEY keeps one across runs)"
	if os.Getenv("MAGPIE_WEB_KEY") != "" {
		carries = "MAGPIE_WEB_KEY"
	}
	fmt.Println(muted.Render("  the link carries " + carries + " · gateway " + advertisedURL() + " · Ctrl-C to stop"))
}

// lanWarning is said wherever the page is served on the network.
func lanWarning() string {
	if gateway.TLSOn() {
		return "anyone with the link can change magpie and see its keys"
	}
	return "anyone with the link can change magpie and see its keys, and the network carries it unencrypted (--tls-cert and --tls-key serve it over https)"
}

// webLink is the link to a page served on addr with key, from this
// computer.
func webLink(addr, key string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/?k=" + url.QueryEscape(key)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	// this computer may call its loopback page over plain http; any other
	// address it has is https with TLS on, as other computers see it
	scheme := "http"
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		scheme = gateway.Scheme()
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/?k=" + url.QueryEscape(key)
}

// proxyLink is the page's link through a reverse proxy in front of a page
// served on loopback (#1479), with the key: MAGPIE_WEB_URL, the page's own
// address there (https://magpie.example.com), or else MAGPIE_PUBLIC_URL
// with no port of its own, as NetworkLinks takes it (#372). One with a
// port names a host whose ports are published, not a proxy. "" with
// neither, or a MAGPIE_WEB_URL that isn't a plain http(s) address.
func proxyLink(key string) string {
	if v := strings.TrimSpace(os.Getenv("MAGPIE_WEB_URL")); v != "" {
		if !strings.Contains(v, "://") {
			v = "https://" + v
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return ""
		}
		return strings.TrimRight(u.String(), "/") + key
	}
	u, err := url.Parse(gateway.PublicURL())
	if err != nil || u.Host == "" || u.Port() != "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/") + key
}

// serveCmd: magpie serve [--web[=host:port]] — the gateway, and with
// --web magpie web's page from the same process: what the service runs
// (`magpie daemon install --web`), so the page shows the gateway that
// serves the agents, its routing included (HuanCheng on Discord).
func serveCmd(args []string) error {
	args, err := tlsFlags(args)
	if err != nil {
		return err
	}
	web := ""
	for _, a := range args {
		switch {
		case a == "--web":
			web = webAddr
		case strings.HasPrefix(a, "--web="):
			web = strings.TrimPrefix(a, "--web=")
			if _, _, err := net.SplitHostPort(web); err != nil {
				return fmt.Errorf("magpie serve: --web=%s isn't host:port", web)
			}
		default:
			return fmt.Errorf("magpie serve: unknown %q · magpie serve [--web[=host:port]] [--tls-cert <file> --tls-key <file>]", a)
		}
	}
	proc.UserPath() // as magpie web's (#3803)
	if web == "" {
		return serve()
	}
	go stats.Run(version, "serve")
	w, err := gui.StartWeb(web, version)
	if err != nil {
		return err
	}
	public := advertisedURL()
	fmt.Println(green.Render("●"), "magpie gateway on", bold.Render(gateway.URL()))
	fmt.Println(muted.Render("  OpenAI  "), public+"/v1/chat/completions", muted.Render("·"), public+"/v1/responses")
	fmt.Println(muted.Render("  Anthropic"), public+"/v1/messages")
	if l := tlsLine(); l != "" {
		fmt.Println(l)
	}
	if !w.ServesGateway() {
		fmt.Println(amber.Render("!"), "another magpie serves the gateway there: this one serves it once that one stops")
	}
	printWeb(w)
	return w.Wait()
}
