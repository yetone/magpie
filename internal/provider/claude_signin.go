package provider

// Adding a Claude account is Claude Code's own `claude auth login`, run by
// magpie in a config directory of its own: the sign-in page, the tokens and
// who signed in are all Claude Code's, and magpie asks Anthropic nothing.
// magpie takes the link Claude Code would open (it is the browser Claude
// Code is given), hands it to its window, and keeps the account Claude Code
// signed in to once it is done.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/wslrun"
)

// claudeCLISignIn is a `claude auth login` magpie is running.
type claudeCLISignIn struct {
	mu   sync.Mutex
	in   io.WriteCloser // where the code its page shows is typed
	port string         // where it waits for the browser, from the link it opened
}

// openedURLEnv names the file the browser Claude Code is given (magpie
// itself, TookOpenedURL) writes the page it was asked to open to.
const openedURLEnv = "MAGPIE_OPENED_URL"

// claudeURLOpener is the browser `claude auth login` is given; a var so
// tests can give another.
var claudeURLOpener = func() string {
	exe, _ := os.Executable()
	return exe
}

// TookOpenedURL is magpie run as the browser of a `claude auth login` it
// started: the page is written down for magpie's window to open, and
// nothing else runs.
func TookOpenedURL(args []string) bool {
	f := os.Getenv(openedURLEnv)
	if f == "" || len(args) != 1 || !strings.HasPrefix(args[0], "https://") {
		return false
	}
	_ = os.WriteFile(f, []byte(args[0]), 0o600)
	return true
}

// claudeSignInEnv is env for a Claude Code that signs in to config
// directory dir: none of the wiring that would point it elsewhere.
func claudeSignInEnv(env []string, dir string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range withoutClaudeWiring(env) {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "BROWSER", openedURLEnv:
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+dir)
}

// claudeCLI is the Claude Code a sign-in runs: this machine's, or, on
// Windows without one, the one installed in a running WSL distro, which
// signs in to the config directory magpie gives it all the same (wslrun).
type claudeCLI struct {
	path string
	wsl  *wslrun.Tool
}

func findClaudeCLI() (claudeCLI, bool) {
	if p := claudeExecutable(); p != "" {
		return claudeCLI{path: p}, true
	}
	if t, ok := wslrun.Find("claude"); ok {
		return claudeCLI{wsl: &t}, true
	}
	return claudeCLI{}, false
}

// ClaudeInWSL is the WSL distro the gateway runs Claude Code in, when there
// is none on Windows itself and one was found there; "" otherwise. It never
// waits on WSL: until a look has been made, one starts and "" comes back.
func ClaudeInWSL() string {
	if !wslrun.On || claudeExecutable() != "" {
		return ""
	}
	if t, ok := wslrun.Known("claude"); ok {
		return t.Distro
	}
	if lookingInWSL.CompareAndSwap(false, true) {
		go func() {
			defer lookingInWSL.Store(false)
			wslrun.Find("claude")
		}()
	}
	return ""
}

var lookingInWSL atomic.Bool

func (c claudeCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	if c.wsl != nil {
		return c.wsl.Command(ctx, args...)
	}
	return proc.CommandContext(ctx, c.path, args...)
}

func (c claudeCLI) probe(ctx context.Context, args ...string) *exec.Cmd {
	if c.wsl != nil {
		return c.wsl.Probe(ctx, args...)
	}
	return proc.ProbeContext(ctx, c.path, args...)
}

// env is env for a sign-in's Claude Code: for one in WSL, what of it goes
// in, its config directory and browser told by their paths there.
func (c claudeCLI) env(env []string) []string {
	if c.wsl == nil {
		return env
	}
	return c.wsl.Env(env, "CLAUDE_CONFIG_DIR/p", "BROWSER/p", openedURLEnv+"/p")
}

// proxied is env told the proxy choice names (netproxy.With), a SOCKS
// one bridged: Claude Code is built with Bun, which takes only http:// and
// https:// proxies (#2651). One in a WSL distro reaches the bridge only
// when the distro shares Windows' loopback.
func (c claudeCLI) proxied(choice string, env []string) ([]string, error) {
	return netproxy.EnvForBunWith(choice, env, c.wsl == nil || c.wsl.Host == "")
}

// startClaudeSignIn runs `claude auth login` and gives the window the page
// to open: the one Claude Code would have opened, which comes back to it on
// this machine, or, when it opened none, the one it prints, whose page
// shows a code to paste.
func startClaudeSignIn(s *signInFlow) error {
	cli, ok := findClaudeCLI()
	if !ok {
		return errors.New("install Claude Code first, the `claude` command line (the Claude desktop app is not it): magpie signs in to Claude through it; " + claudecode.InstallHint)
	}
	if err := os.MkdirAll(claudeDirsRoot(), 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(claudeDirsRoot(), "signin-")
	if err != nil {
		return err
	}
	cleanup := func() {
		clearClaudeDir(dir)
		_ = os.RemoveAll(dir)
	}
	opened := filepath.Join(dir, ".opened-url")
	env := append(claudeSignInEnv(os.Environ(), dir), "BROWSER="+claudeURLOpener(), openedURLEnv+"="+opened)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := cli.command(ctx, "auth", "login", "--claudeai")
	cmd.Dir = dir
	// through the proxy the account's requests will go through (#1663)
	env, err = cli.proxied(s.proxy, env)
	if err != nil {
		cancel()
		cleanup()
		return err
	}
	cmd.Env = cli.env(env)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		cleanup()
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		cleanup()
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		cleanup()
		return err
	}
	cl := &claudeCLISignIn{in: in}
	s.mu.Lock()
	s.stop, s.claude = cancel, cl
	s.mu.Unlock()

	printed := make(chan string, 1)
	exited := make(chan string, 1)
	var said lastSaid
	claudeLogins.Add(1)
	go func() {
		defer claudeLogins.Done()
		sc := bufio.NewScanner(out)
		var tail []string
		for sc.Scan() {
			line := strings.TrimSpace(ansi.ReplaceAllString(sc.Text(), ""))
			if u := cursorLoginURL.FindString(line); u != "" {
				select {
				case printed <- u:
				default:
				}
				continue
			}
			if line = strings.TrimSpace(strings.TrimPrefix(line, "Paste code here if prompted >")); line != "" {
				tail = append(tail, line)
				said.set(line)
			}
		}
		err := cmd.Wait()
		cancel()
		forgetAccountCaches()
		msg := "claude auth login didn't finish"
		if n := len(tail); n > 0 {
			msg = tail[n-1]
		}
		if old := claudeTooOld(cli, tail); old != "" {
			msg = old
		}
		if err == nil {
			l, lerr := claudeSignedIn(cli, dir, s.proxy)
			if lerr == nil {
				var using bool
				if lerr = s.keep(func() (err error) { using, err = addLogin(l); return err }); lerr == nil {
					cleanup()
					s.finish(SignInState{State: "done", User: l.User, Plan: l.Plan, Using: using})
					return
				}
			}
			msg = lerr.Error()
		}
		cleanup()
		exited <- msg
		s.finish(SignInState{State: "failed", Error: msg})
	}()

	var link string
	grace := time.After(linkWait)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(opened); err == nil && len(b) > 0 {
			u := strings.TrimSpace(string(b))
			cl.mu.Lock()
			cl.port = claudeCallbackPort(u)
			cl.mu.Unlock()
			s.mu.Lock()
			s.st.URL, s.st.PasteCallback, s.st.PasteCode = u, true, false
			s.mu.Unlock()
			return nil
		}
		select {
		case u := <-printed:
			// the page it opens, if it opens one, is on its way
			link = u
			grace = time.After(3 * time.Second)
		case msg := <-exited:
			return errors.New(msg)
		case <-grace:
			if link == "" {
				cancel()
				return claudeNoLink(cli, said.get())
			}
			s.mu.Lock()
			s.st.URL, s.st.PasteCode = link, true
			s.mu.Unlock()
			return nil
		case <-tick.C:
		}
	}
}

// claudeLogins counts the runs of `claude auth login` still going on,
// to the end of what each does after the CLI exits (the account added, its
// folder removed). A sign-in that gave up has killed its run, but the run
// may still be letting go; tests wait for it before the next one starts.
var claudeLogins sync.WaitGroup

// lastSaid is the last line a login command printed, which says why
// there's no link.
type lastSaid struct {
	mu   sync.Mutex
	line string
}

func (l *lastSaid) set(s string) { l.mu.Lock(); l.line = s; l.mu.Unlock() }

func (l *lastSaid) get() string { l.mu.Lock(); defer l.mu.Unlock(); return l.line }

// claudeNoLink is the error of a `claude auth login` that gave no link in
// linkWait: what it last printed, or, when it printed nothing at all, the
// program that was run, which may be a script that never starts Claude
// Code (deluxe on Discord: a mise wrapper running itself), and how to see
// what it does.
func claudeNoLink(cli claudeCLI, last string) error {
	if last != "" {
		return errors.New("claude auth login gave no link to open: " + last)
	}
	if cli.path == "" {
		return fmt.Errorf("claude auth login gave no link to open and printed nothing in %s", linkWait)
	}
	return fmt.Errorf("claude auth login gave no link to open: %s printed nothing in %s; run `%s auth login` in a terminal to see what it does", cli.path, linkWait, cli.path)
}

// claudeCallbackPort is the port a Claude sign-in page sends the browser
// back to on this machine, "" when it sends it elsewhere.
func claudeCallbackPort(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	r, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil || !isLoopback(r.Hostname()) {
		return ""
	}
	return r.Port()
}

func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// claudeSignedIn is the account `claude auth login` signed in to in config
// directory dir, as magpie keeps it: the sign-in Claude Code kept, and who
// it says that is, asked through the sign-in's proxy.
func claudeSignedIn(cli claudeCLI, dir, proxy string) (savedLogin, error) {
	c, ok := readClaudeDir(dir)
	if !ok {
		return savedLogin{}, errors.New("Claude Code signed in, but kept no sign-in magpie can read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := cli.probe(ctx, "auth", "status", "--json")
	cmd.Dir = dir
	env, err := cli.proxied(proxy, claudeSignInEnv(os.Environ(), dir))
	if err != nil {
		return savedLogin{}, err
	}
	cmd.Env = cli.env(env)
	b, _ := cmd.Output()
	var status struct {
		Email            string `json:"email"`
		OrgName          string `json:"orgName"`
		SubscriptionType string `json:"subscriptionType"`
	}
	_ = json.Unmarshal(b, &status)
	acct := map[string]any{}
	if pb, err := os.ReadFile(filepath.Join(dir, ".claude.json")); err == nil {
		var profile struct {
			OAuthAccount map[string]any `json:"oauthAccount"`
		}
		if json.Unmarshal(pb, &profile) == nil && profile.OAuthAccount != nil {
			acct = profile.OAuthAccount
		}
	}
	if _, ok := acct["emailAddress"].(string); !ok && status.Email != "" {
		acct["emailAddress"] = status.Email
	}
	if _, ok := acct["organizationName"].(string); !ok && status.OrgName != "" {
		acct["organizationName"] = status.OrgName
	}
	if c.OAuth.SubscriptionType == "" {
		c.OAuth.SubscriptionType = status.SubscriptionType
	}
	return claudeLogin(c, acct)
}

// claudePaste finishes a Claude sign-in from what its page left: the code
// Claude's page shows, typed into `claude auth login` as one types it, or
// the address a browser that couldn't reach this machine ended on, handed
// to the `claude auth login` waiting for it.
func (s *signInFlow) claudePaste(raw string) error {
	raw = strings.TrimSpace(raw)
	s.mu.Lock()
	st, cl := s.st, s.claude
	s.mu.Unlock()
	if st.State != "waiting" {
		return errors.New("this sign-in is over; start it again")
	}
	cl.mu.Lock()
	port, in := cl.port, cl.in
	cl.mu.Unlock()
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if port == "" || u.Port() != port || !isLoopback(u.Hostname()) || u.Query().Get("code") == "" {
			return errors.New("that address isn't from this sign-in: paste the one its browser tab ended on")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:"+port+u.RequestURI(), nil)
		if err != nil {
			return err
		}
		c := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := c.Do(req)
		if err != nil {
			return errors.New("Claude Code didn't take that address: " + err.Error())
		}
		res.Body.Close()
	} else if code, state, ok := strings.Cut(raw, "#"); ok && code != "" && state != "" && !strings.ContainsAny(raw, " \n") {
		if _, err := io.WriteString(in, raw+"\n"); err != nil {
			return errors.New("the sign-in is over; start it again")
		}
	} else {
		return errors.New("paste the code Claude's page shows, or the whole address the browser ended on")
	}
	select {
	case <-s.done:
	case <-time.After(time.Minute):
		return errors.New("the sign-in is still finishing; magpie shows the account when it's done")
	}
	switch st := s.status(); st.State {
	case "done":
		return nil
	case "failed":
		return errors.New(st.Error)
	}
	return errors.New("the sign-in was canceled")
}

// claudeTooOld is what a sign-in says when the Claude Code it ran doesn't
// know `auth login --claudeai`: that Claude Code is too old, which one, and
// how to update it, rather than its own "error: unknown option
// '--claudeai'" (zi_xiang5806 on X: 2.1.38 first on PATH). "" when what it
// said is something else.
func claudeTooOld(cli claudeCLI, said []string) string {
	var why string
	for _, l := range said {
		if lo := strings.ToLower(l); strings.Contains(lo, "unknown option") || strings.Contains(lo, "unknown command") {
			why = l
			break
		}
	}
	if why == "" {
		return ""
	}
	which := "Claude Code"
	if cli.wsl != nil {
		which += " in WSL (" + cli.wsl.Distro + ")"
	} else if cli.path != "" {
		if v := claudecode.VersionOf(cli.path); v != "" {
			which += " " + v
		}
		which += " at " + cli.path
	}
	return fmt.Sprintf("%s is too old to sign in to Claude (it said %q): update it with `claude update`, or `npm install -g @anthropic-ai/claude-code@latest` when npm installed it, then try again", which, why)
}
