package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/gui"
)

// webAddr is where `magpie web` listens unless told: beside the gateway.
const webAddr = "127.0.0.1:3430"

// webCmd: magpie web [--addr host:port] [--lan] [--no-open] — the app's
// window in a browser, for a computer that can't show the app.
func webCmd(args []string) error {
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
		default:
			return fmt.Errorf("magpie web: unknown %q · magpie web [--addr host:port] [--lan] [--no-open], MAGPIE_WEB_KEY to keep one key", a)
		}
	}
	if lan && addr == webAddr {
		_, port, _ := net.SplitHostPort(webAddr)
		addr = "0.0.0.0:" + port
	}
	w, err := gui.StartWeb(addr, version)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("●"), "magpie web on", bold.Render(w.Link))
	host, _, _ := net.SplitHostPort(w.Addr)
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		_, port, _ := net.SplitHostPort(w.Addr)
		key := w.Link[strings.Index(w.Link, "/?k="):]
		for _, l := range gui.NetworkLinks(port, key) {
			fmt.Println(muted.Render("  on the network"), l)
		}
		if gateway.ContainerAddrs() {
			fmt.Println(muted.Render("  " + containerNote))
		}
		fmt.Println(amber.Render("!"), "anyone with the link can change magpie and see its keys, and the network carries it unencrypted")
	}
	carries := "this run's key (MAGPIE_WEB_KEY keeps one across runs)"
	if os.Getenv("MAGPIE_WEB_KEY") != "" {
		carries = "MAGPIE_WEB_KEY"
	}
	fmt.Println(muted.Render("  the link carries " + carries + " · gateway " + advertisedURL() + " · Ctrl-C to stop"))
	if open {
		openInBrowser(w.Link)
	}
	return w.Wait()
}
