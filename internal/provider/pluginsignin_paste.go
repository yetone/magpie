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

	"github.com/yetone/magpie/internal/plugin"
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
// Command Code's Studio posts its key. Their sign-in takes a key made on
// the page given here and pasted, through the plugin's API key way, as the
// built-in's did (startCommandCodeSignIn's PasteKey, #2468).
var pluginPostsCallback = map[string]string{"commandcode-plan": cmdKeysURL}

// pastedPluginWait is how long a pasted address is given to finish the
// sign-in before magpie says it didn't.
var pastedPluginWait = 30 * time.Second

// loopbackPorts are the ports on this machine a sign-in page sends the
// browser back to, as its address names them: a redirect address on
// localhost (redirect_uri, Trae's auth_callback_url, Command Code's
// callback) or a bare port (Zed's native_app_port). A page that is a login
// first, with the sign-in page as where it goes next (CodeArts'
// international station puts its authorize address, port and all, in
// service=), is read through to that page.
func loopbackPorts(page string) []string {
	var ports []string
	readPorts(page, 2, func(p string) {
		if !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	})
	return ports
}

// readPorts gives add each port page names, and reads the web addresses
// in its query the same way, depth pages deep.
func readPorts(page string, depth int, add func(string)) {
	u, err := url.Parse(page)
	if err != nil {
		return
	}
	for k, vs := range u.Query() {
		for _, v := range vs {
			r, err := url.Parse(v)
			if err == nil && r.Scheme == "http" && isLoopback(r.Hostname()) {
				p := r.Port()
				if p == "" {
					p = "80"
				}
				add(p)
				continue
			}
			if strings.Contains(strings.ToLower(k), "port") && isPort(v) {
				add(v)
				continue
			}
			if err == nil && depth > 1 && (r.Scheme == "https" || r.Scheme == "http") && r.Host != "" {
				readPorts(v, depth-1, add)
			}
		}
	}
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
	return pluginPostsCallback[id] == "" && len(loopbackPorts(page)) > 0
}

type pluginKeyWay struct {
	id     string
	method int
}

// pluginKeyMethod is id's "api" method, for a plugin whose page posts its
// callback, and the page its keys are made on; ok is false for another.
func pluginKeyMethod(id string) (method int, keys string, ok bool) {
	keys = pluginPostsCallback[id]
	if keys == "" {
		return 0, "", false
	}
	for _, pp := range plugin.Cached() {
		if pp.ID != id {
			continue
		}
		for i, m := range pp.Methods {
			if m.Type == "api" {
				return i, keys, true
			}
		}
	}
	return 0, "", false
}

// pastedPluginKey finishes a plugin's browser sign-in with a key pasted
// instead, through the plugin's own API key way: the page's post couldn't
// reach a magpie on a server or in Docker.
func (s *signInFlow) pastedPluginKey(raw string) error {
	key := strings.TrimSpace(raw)
	switch {
	case key == "":
		return errors.New("paste an API key from the keys page")
	case strings.Contains(key, "://"):
		return errors.New("that's an address: this page sends its key in the background, so make a key on the keys page and paste that")
	case strings.ContainsAny(key, " \t\r\n"):
		return errors.New("that doesn't look like an API key")
	}
	if !s.claim() {
		return errors.New("this sign-in is over or already finishing; start it again if it doesn't end")
	}
	ctx, cancel := context.WithTimeout(s.via(context.Background()), time.Minute)
	defer cancel()
	saved, err := plugin.APIKey(ctx, s.pluginKey.id, s.pluginKey.method, nil, key, plugin.NewAccount)
	if err != nil {
		s.mu.Lock()
		s.claimed = false // the browser's own may still come, or a right key
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	s.pluginDone(saved, nil)
	if stop != nil {
		stop() // the page's own post is waited for no more
	}
	return nil
}
