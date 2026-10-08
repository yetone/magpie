package provider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// A plugin's browser sign-in ("auto") that the plugin finishes at a port of
// its own on this machine — Devin's, Kiro's, Trae's and Zed's do — can't
// finish when the browser is on another computer: magpie on a server or in
// Docker, opened from the web (Chicring). The page the browser ends on
// won't load there, but its address is pasted back, and magpie opens it on
// this machine, where the plugin is listening, as a built-in's pasted
// address goes to its own callback (pastedCallback).

// pluginPostsCallback are the plugins (by OpenCode's id) whose page posts
// to their port from script, which leaves the browser no address to paste:
// Command Code's Studio posts its key. Their sign-in offers the plugin's
// API key way instead.
var pluginPostsCallback = map[string]bool{"commandcode-plan": true}

// pastedPluginWait is how long a pasted address is given to finish the
// sign-in before magpie says it didn't.
var pastedPluginWait = 30 * time.Second

// loopbackPorts are the ports on this machine a sign-in page sends the
// browser back to, as its address names them: a redirect address on
// localhost (redirect_uri, Trae's auth_callback_url, Command Code's
// callback) or a bare port (Zed's native_app_port).
func loopbackPorts(page string) []string {
	u, err := url.Parse(page)
	if err != nil {
		return nil
	}
	var ports []string
	add := func(p string) {
		if p != "" && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	for k, vs := range u.Query() {
		for _, v := range vs {
			if r, err := url.Parse(v); err == nil && r.Scheme == "http" && isLoopback(r.Hostname()) {
				p := r.Port()
				if p == "" {
					p = "80"
				}
				add(p)
				continue
			}
			if strings.Contains(strings.ToLower(k), "port") && isPort(v) {
				add(v)
			}
		}
	}
	return ports
}

func isPort(v string) bool {
	if v == "" || len(v) > 5 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// openPastedCallback opens a pasted address on this machine, if it is one
// of ports: the plugin's port answers it as it would the browser. next is
// the page a sign-in goes on to (Kiro's AWS sign-in, which comes back to
// the same port), when that is what it answered.
func openPastedCallback(ctx context.Context, ports []string, raw string) (next string, status int, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", 0, errors.New("paste the whole address the browser ended on, starting with http://")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if !isLoopback(u.Hostname()) || !slices.Contains(ports, port) {
		return "", 0, errors.New("that address isn't from this sign-in: paste the one its browser tab ended on")
	}
	// the plugin listens on 127.0.0.1, which "localhost" may not resolve to
	// in a container; never through a proxy
	target := "http://" + net.JoinHostPort("127.0.0.1", port) + u.RequestURI()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", 0, err
	}
	c := http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, errors.New("the sign-in isn't waiting for that address any more; start it again")
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 && strings.HasPrefix(loc, "https://") {
		if slices.ContainsFunc(loopbackPorts(loc), func(p string) bool { return slices.Contains(ports, p) }) {
			next = loc
		}
	}
	return next, resp.StatusCode, nil
}

// pluginCallback finishes a plugin's browser sign-in with the address its
// browser ended on.
func (s *signInFlow) pluginCallback(raw string) error {
	s.mu.Lock()
	ports, waiting := s.pluginPorts, s.st.State == "waiting"
	s.mu.Unlock()
	if !waiting {
		return errors.New("this sign-in is over; start it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pastedPluginWait)
	defer cancel()
	next, status, err := openPastedCallback(ctx, ports, raw)
	if err != nil {
		return err
	}
	if next != "" {
		s.mu.Lock()
		s.st.URL = next
		s.mu.Unlock()
		return nil
	}
	if status == http.StatusNotFound {
		return errors.New("that address isn't from this sign-in: paste the one its browser tab ended on")
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.New("that address didn't finish the sign-in: paste the whole address its browser tab ended on, or start it again")
	}
	switch st := s.status(); st.State {
	case "done":
		return nil
	case "failed":
		return errors.New(st.Error)
	}
	return errors.New("the sign-in was canceled")
}

// PastePluginCallback is pluginCallback for the command line, which runs
// a plugin's sign-in itself: page is the address the sign-in opened.
func PastePluginCallback(ctx context.Context, page, raw string) (next string, err error) {
	ports := loopbackPorts(page)
	if len(ports) == 0 {
		return "", errors.New("this sign-in can't be finished from a pasted address")
	}
	next, status, err := openPastedCallback(ctx, ports, raw)
	if err == nil && next == "" && status == http.StatusNotFound {
		err = errors.New("that address isn't from this sign-in: paste the one its browser tab ended on")
	}
	return next, err
}

// PluginPastesCallback says whether a plugin's sign-in that opened page
// can be finished from the address its browser ends on.
func PluginPastesCallback(id, page string) bool {
	return !pluginPostsCallback[id] && len(loopbackPorts(page)) > 0
}
