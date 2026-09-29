package provider

// A Grok subscription (SuperGrok, X Premium+) is served straight from the
// backend xAI's Grok Build CLI talks to, OpenAI's Responses API at
// cli-chat-proxy.grok.com, with the CLI's sign-in. Here is who the CLI is
// signed in to, the models it offers, the sign-in, which is the CLI's own
// `login`, and the requests' signing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// GrokExecutable finds the Grok Build CLI; a var so tests can fake it.
var GrokExecutable = func() string {
	home, _ := os.UserHomeDir()
	path := append(filepath.SplitList(os.Getenv("PATH")), registryPath()...)
	for _, c := range grokCandidates(runtime.GOOS, home, GrokHome(), os.Getenv("GROK_BIN_DIR"), path) {
		if isFile(c.path) && (c.own || isGrokBuild(c.path)) {
			return c.path
		}
	}
	return ""
}

// grokCandidate is a place the CLI may be; own when only its installer
// puts a grok there.
type grokCandidate struct {
	path string
	own  bool
}

// grokCandidates are the places to look for the CLI, first to last: the
// installer's bin (GROK_BIN_DIR, else ~/.grok/bin), each folder on PATH,
// then ~/.local/bin. On Windows it is grok.exe — looking for a bare grok
// there missed ~/.grok/bin, and a grok.cmd npm put on PATH first hid it
// (#180). path is the process' PATH and, on Windows, the registry's, which
// has what was installed since magpie started.
func grokCandidates(goos, home, grokHome, binDir string, path []string) []grokCandidate {
	name := "grok"
	if goos == "windows" {
		name = "grok.exe"
	}
	var out []grokCandidate
	seen := map[string]bool{}
	add := func(dir string, own bool) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		p := filepath.Join(dir, name)
		k := p
		if goos == "windows" {
			k = strings.ToLower(p)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, grokCandidate{p, own})
		}
	}
	add(binDir, true)
	add(filepath.Join(grokHome, "bin"), true)
	if home != "" {
		// the installer puts it under ~/.grok whatever GROK_HOME says
		add(filepath.Join(home, ".grok", "bin"), true)
	}
	for _, d := range path {
		add(strings.TrimSpace(d), false)
	}
	if home != "" {
		add(filepath.Join(home, ".local", "bin"), false)
	}
	return out
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// isGrokBuild tells xAI's grok from any other program by that name: its
// installer keeps it under the grok home.
func isGrokBuild(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Windows can't resolve some real paths (a subst drive, a folder
		// OneDrive keeps); the path itself still says where it is
		real = path
	}
	return strings.Contains(strings.ToLower(filepath.ToSlash(real)), "/.grok/")
}

// GrokHome is where the CLI keeps its sign-in and settings.
func GrokHome() string {
	if h := os.Getenv("GROK_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok")
}

// grokCredential is the CLI's sign-in, as its auth.json keeps it.
type grokCredential struct {
	Key       string    `json:"key"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
	Issuer    string    `json:"oidc_issuer"`
}

// readGrokCredential reads the sign-in the CLI keeps in its home. magpie
// only reads it: the CLI refreshes it with a token that changes each time.
func readGrokCredential(home string) (grokCredential, bool) {
	var all map[string]grokCredential
	if !readJSON(filepath.Join(home, "auth.json"), &all) {
		return grokCredential{}, false
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c := all[k]; c.Key != "" {
			return c, true
		}
	}
	return grokCredential{}, false
}

func grokAccount() (Provider, bool) {
	if GrokExecutable() == "" {
		return Provider{}, false
	}
	ls := grokLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	home := ls[0].Home
	acct := &Account{Agent: "grok", User: ls[0].User, Plan: ls[0].Plan, Home: home}
	if home == GrokHome() {
		acct.Home = "" // the CLI's own, wherever it is
	}
	grokSigned(acct, home)
	acct.body = grokBody
	acct.models = func() []catalog.Model { return []catalog.Model{{ID: "grok-4.7", Name: "Grok 4.7"}} }
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := grokModels(ctx, acct.sign)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("grok", grokBase, ms)
	}
	return Provider{ID: "grok", Name: "Grok (SuperGrok)", Icon: "xai", Website: "https://x.ai/cli", Responses: grokBase, Account: acct}, true
}

// grokSigned has the account's requests signed with the sign-in in home.
func grokSigned(acct *Account, home string) {
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		c, err := grokAccessToken(home, GrokExecutable(), false)
		if err != nil {
			return err
		}
		grokHeaders(req, c.Key)
		if m := bodyModel(body); m != "" {
			req.Header.Set("x-grok-model-override", m)
		}
		var v struct {
			Key string `json:"prompt_cache_key"`
		}
		if json.Unmarshal(body, &v) == nil && v.Key != "" {
			req.Header.Set("x-grok-conv-id", v.Key)
		}
		return nil
	}
}

// grokTools are the tool types Grok's backend takes; it turns the whole
// request away over another, as over Codex's freeform apply_patch (custom)
// or its sub-agent tools, grouped in a namespace.
var grokTools = map[string]bool{"function": true, "web_search": true, "x_search": true, "image_generation": true,
	"collections_search": true, "file_search": true, "code_execution": true, "code_interpreter": true,
	"mcp": true, "shell": true, "tool_search": true}

// grokBody leaves out the tools Grok's backend doesn't take, as a backend
// magpie translates for goes without them: Codex, without apply_patch,
// edits files through its shell. Codex's web_search says whether it may
// reach the live web, which Grok's doesn't take either; it searches live.
// And Codex hands reasoning back with "content": null, which the backend
// can't read the encrypted reasoning beside ("Could not decode the
// compaction blob"), so a null content goes.
func grokBody(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tools"`)) && !bytes.Contains(body, []byte(`"reasoning"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	dirty := false
	if tools, ok := m["tools"].([]any); ok {
		kept := tools[:0:0]
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				ty, _ := tm["type"].(string)
				if !grokTools[ty] {
					dirty = true
					continue
				}
				if _, ok := tm["external_web_access"]; ok {
					delete(tm, "external_web_access")
					dirty = true
				}
			}
			kept = append(kept, t)
		}
		m["tools"] = kept
		if tc, ok := m["tool_choice"].(map[string]any); ok {
			if ty, _ := tc["type"].(string); !grokTools[ty] {
				delete(m, "tool_choice")
				dirty = true
			}
		}
	}
	input, _ := m["input"].([]any)
	for _, it := range input {
		if im, ok := it.(map[string]any); ok && im["type"] == "reasoning" {
			if c, ok := im["content"]; ok && c == nil {
				delete(im, "content")
				dirty = true
			}
		}
	}
	if !dirty {
		return body
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return b
}

// grokHeaders say a request comes from the Grok CLI, which the backend
// wants to know the version of: without it the CLI is "outdated".
func grokHeaders(req *http.Request, token string) {
	v := grokVersion()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-grok-client-version", v)
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-identifier", "grok-shell")
	req.Header.Set("x-grok-client-mode", "headless")
	goos, arch := runtime.GOOS, runtime.GOARCH
	if goos == "darwin" {
		goos = "macos"
	}
	switch arch {
	case "arm64":
		arch = "aarch64"
	case "amd64":
		arch = "x86_64"
	}
	req.Header.Set("User-Agent", "grok-shell/"+v+" ("+goos+"; "+arch+")")
}

// grokClientVersion is the Grok CLI version magpie says it is, unless the
// installed one is newer.
const grokClientVersion = "1.0.41"

var grokVersionCache struct {
	sync.Mutex
	v  string
	at time.Time
}

var grokSemver = regexp.MustCompile(`\d+\.\d+\.\d+`)

func grokVersion() string {
	grokVersionCache.Lock()
	defer grokVersionCache.Unlock()
	if time.Since(grokVersionCache.at) < 10*time.Minute {
		return grokVersionCache.v
	}
	v := grokClientVersion
	if exe := GrokExecutable(); exe != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if out, err := proc.CommandContext(ctx, exe, "--version").Output(); err == nil { // "grok 1.0.41 (4220f3b224a6)"
			if c := grokSemver.FindString(string(out)); c != "" && compareClaudeVersion(c, v) > 0 {
				v = c
			}
		}
		cancel()
	}
	grokVersionCache.v, grokVersionCache.at = v, time.Now()
	return v
}

// grokModels lists what the account can use, with each model's context
// window and efforts, as the CLI's backend lists them.
func grokModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, grokBase+"/models", nil)
	if err != nil {
		return nil, err
	}
	if err := sign(ctx, req, nil); err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return nil, errorf("Grok models: %s", APIError(b, res.Status))
	}
	ms := parseGrokModels(b)
	if len(ms) == 0 {
		return nil, errorf("Grok listed no models")
	}
	return ms, nil
}

func parseGrokModels(b []byte) []catalog.Model {
	var v struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Context int    `json:"context_window"`
			Backend string `json:"api_backend"`
			Efforts []struct {
				Value string `json:"value"`
			} `json:"reasoning_efforts"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var ms []catalog.Model
	for _, d := range v.Data {
		if d.ID == "" || (d.Backend != "" && d.Backend != "responses") {
			continue
		}
		m := catalog.Model{ID: d.ID, Name: d.Name, Context: d.Context, Images: true, APIs: []string{"responses"}}
		if m.Name == "" {
			m.Name = d.ID
		}
		// listed hardest first; kept easiest first, as the others are
		for i := len(d.Efforts) - 1; i >= 0; i-- {
			if e := d.Efforts[i].Value; e != "" {
				m.Efforts = append(m.Efforts, e)
			}
		}
		ms = append(ms, m)
	}
	return ms
}

// grokRefreshMargin is how close to its expiry a borrowed token has the CLI
// refresh it first.
const grokRefreshMargin = 5 * time.Minute

var grokRefresh sync.Mutex

// grokAccessToken is the CLI's sign-in, with a token still good. One about
// to expire is first refreshed by the CLI in its own home: a `grok models`
// there renews it the way any use of the CLI does; so is one a run has
// found expired.
func grokAccessToken(home, binary string, expired bool) (grokCredential, error) {
	c, ok := readGrokCredential(home)
	if ok && (expired || time.Until(c.ExpiresAt) < grokRefreshMargin) && binary != "" {
		grokRefresh.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := proc.CommandContext(ctx, binary, "models")
		cmd.Dir = filepath.Dir(home)
		cmd.Env = netproxy.Env(grokOwnEnv(os.Environ(), home))
		_ = cmd.Run()
		cancel()
		grokRefresh.Unlock()
		c, ok = readGrokCredential(home)
	}
	if !ok {
		return c, errorf("Grok is not signed in; run `grok login`")
	}
	if !c.ExpiresAt.IsZero() && time.Until(c.ExpiresAt) <= 0 {
		return c, errorf("Grok's sign-in has expired; run `grok login`")
	}
	return c, nil
}

// grokOwnEnv runs the CLI as the user runs it, in its own home.
func grokOwnEnv(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "GROK_HOME", "GROK_AUTH_PROVIDER_COMMAND", "GROK_AUTH_EXPIRED":
			continue
		}
		out = append(out, e)
	}
	return append(out, "GROK_HOME="+home)
}

// startGrokSignIn runs `grok login` with its device code, hands its link to
// the window, and finishes when the CLI says the account is in. With the
// CLI signed in already, a further account signs in in a home of magpie's,
// so the CLI's own sign-in stays as it is.
func startGrokSignIn(s *signInFlow) error {
	path := GrokExecutable()
	if path == "" {
		c, _ := cliFor("grok")
		manual := c.sh
		if runtime.GOOS == "windows" {
			manual = c.ps
		}
		return errorf("install Grok Build first: %s", manual)
	}
	if _, ok := readGrokCredential(GrokHome()); !ok {
		return runCLISignIn(s, "grok login", nil, true, nil, func() (string, string, bool) {
			c, ok := readGrokCredential(GrokHome())
			return c.Email, "", ok
		}, path, "login", "--device-auth")
	}
	home, err := newGrokHome()
	if err != nil {
		return err
	}
	err = runCLISignIn(s, "grok login", grokOwnEnv(os.Environ(), home), false, func() { removeGrokHome(home) }, func() (string, string, bool) {
		user, err := addGrokLogin(home)
		return user, "", err == nil
	}, path, "login", "--device-auth")
	if err != nil {
		removeGrokHome(home)
	}
	return err
}

// agentCommand runs an agent's CLI with magpie's proxy.
func agentCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := proc.CommandContext(ctx, path, args...)
	cmd.Env = netproxy.Env(nil)
	return cmd
}

// runCLISignIn runs an agent's own login command, hands the first link it
// prints to the window, and finishes when the command does and identity
// says who is signed in. using says whether the agent now uses that
// account; failed, when there is one, undoes what a sign-in that did not
// finish left behind.
func runCLISignIn(s *signInFlow, what string, env []string, using bool, failed func(), identity func() (user, plan string, ok bool), path string, args ...string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := proc.CommandContext(ctx, path, args...)
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = netproxy.Env(env)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	s.mu.Lock()
	s.stop = cancel
	s.mu.Unlock()
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sent := false
		var tail []string
		for sc.Scan() {
			line := ansi.ReplaceAllString(sc.Text(), "")
			if u := cursorLoginURL.FindString(line); u != "" && !sent {
				sent = true
				got <- u
			}
			if strings.TrimSpace(line) != "" {
				tail = append(tail, strings.TrimSpace(line))
			}
		}
		if !sent {
			close(got)
		}
		err := cmd.Wait()
		cancel()
		forgetAccountCaches()
		if err == nil {
			if user, plan, ok := identity(); ok {
				s.finish(SignInState{State: "done", User: user, Plan: plan, Using: using})
				return
			}
		}
		if failed != nil {
			failed()
		}
		msg := what + " didn't finish"
		if n := len(tail); n > 0 {
			msg = tail[n-1]
		}
		s.finish(SignInState{State: "failed", Error: msg})
	}()
	select {
	case u, ok := <-got:
		if !ok {
			return fmt.Errorf("%s gave no link to open", what)
		}
		s.mu.Lock()
		s.st.URL = u
		s.mu.Unlock()
		return nil
	case <-time.After(30 * time.Second):
		cancel()
		return fmt.Errorf("%s gave no link to open", what)
	}
}

// GrokUser is who the grok with this home is signed in to.
func GrokUser(home string) (string, bool) {
	c, ok := readGrokCredential(home)
	return c.Email, ok
}
