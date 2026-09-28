package provider

// A Cursor subscription is served through the API cursor-agent talks to
// (gateway/cursor.go), with the account it is signed in to; here is who that
// account is, the models it offers, the sign-in, which is cursor-agent's own
// `login`, and the token that sign-in keeps.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/proc"
)

// CursorExecutable finds the cursor-agent CLI; a var so tests can fake it.
var CursorExecutable = func() string {
	for _, name := range []string{"cursor-agent", "agent"} {
		if p, err := exec.LookPath(name); err == nil && (name == "cursor-agent" || isCursorAgent(p)) {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "cursor-agent"), "/usr/local/bin/cursor-agent", "/opt/homebrew/bin/cursor-agent"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// isCursorAgent tells Cursor's `agent` from any other program by that name.
func isCursorAgent(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	return err == nil && strings.Contains(real, "cursor-agent")
}

var cursorStatus = &cliIdentity{name: "cursor", exe: func() string { return CursorExecutable() }, ask: func() (string, string, bool, error) { return askCursorIdentity() }}

// cursorIdentity is who Cursor's CLI says is signed in; see cliIdentity.
func cursorIdentity() (user, plan string, ok bool) { return cursorStatus.get() }

func forgetCursorStatus() { cursorStatus.forget() }

// askCursorIdentity asks `cursor-agent about`; an error is a CLI that
// didn't answer — timed out or failed before its report — not one saying
// nobody is signed in, which it does with a report whose userEmail is null.
func askCursorIdentity() (user, plan string, ok bool, err error) {
	path := CursorExecutable()
	if path == "" {
		return "", "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := agentCommand(ctx, path, "about", "--format", "json").Output()
	var about struct {
		SubscriptionTier string `json:"subscriptionTier"`
		UserEmail        string `json:"userEmail"`
	}
	if err := json.Unmarshal(out, &about); err != nil {
		return "", "", false, fmt.Errorf("cursor-agent about: %w", err)
	}
	if strings.TrimSpace(about.UserEmail) == "" {
		return "", "", false, nil
	}
	return strings.TrimSpace(about.UserEmail), strings.TrimSpace(about.SubscriptionTier), true, nil
}

func cursorAccount() (Provider, bool) {
	user, plan, ok := cursorIdentity()
	if !ok {
		return Provider{}, false
	}
	acct := &Account{Agent: "cursor", User: user, Plan: plan}
	acct.models = func() []catalog.Model { return []catalog.Model{{ID: "auto", Name: "Auto"}} }
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := cursorModels(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("cursor", "", ms)
	}
	return Provider{ID: "cursor", Name: "Cursor", Icon: "cursor", Website: "https://cursor.com", Account: acct}, true
}

var (
	ansi         = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	cursorModelL = regexp.MustCompile(`^([A-Za-z0-9][\w.:-]*) - (.+)$`)
)

// cursorModels lists what the account can use, as `cursor-agent models`
// prints it: "id - Name", one a line.
func cursorModels(ctx context.Context) ([]catalog.Model, error) {
	path := CursorExecutable()
	if path == "" {
		return nil, errorf("cursor-agent is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := agentCommand(ctx, path, "models").Output()
	if err != nil {
		return nil, errorf("cursor-agent models: %v", err)
	}
	return parseCursorModels(string(out)), nil
}

func parseCursorModels(out string) []catalog.Model {
	var ms []catalog.Model
	s := bufio.NewScanner(strings.NewReader(ansi.ReplaceAllString(out, "")))
	for s.Scan() {
		m := cursorModelL.FindStringSubmatch(strings.TrimSpace(s.Text()))
		if m == nil {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[2]), "(default)"))
		name = strings.TrimSpace(strings.TrimSuffix(name, "(current)"))
		ms = append(ms, catalog.Model{ID: m[1], Name: name, Context: cursorContext(m[1], name)})
	}
	return ms
}

// cursorDefaultContext is the context Cursor gives a model it doesn't name
// as a 1M one.
const cursorDefaultContext = 200_000

var (
	cursorMillion = regexp.MustCompile(`\b(\d+)M\b`)
	cursorVariant = regexp.MustCompile(`-(fast|none|low|medium|high|xhigh|extra-high|max|thinking)$`)
)

// cursorContext is how much of a conversation Cursor lets a model hold. Its
// ids ("claude-opus-5-5-high-fast") are its own, so no catalog knows them,
// and an agent given none took every Cursor model for its own default: a
// 1M one compacted at a fifth of it, and a 200K one — Cursor's own, where
// the name doesn't say 1M — was sent more than Cursor keeps. The name says
// which is which ("Claude Opus 5.5 1M"); under it, a model known to hold
// less keeps its own.
func cursorContext(id, name string) int {
	if m := cursorMillion.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n * 1_000_000
	}
	base := strings.TrimPrefix(id, "cursor-")
	for {
		b := cursorVariant.ReplaceAllString(base, "")
		if b == base {
			break
		}
		base = b
	}
	if base == "auto" { // Cursor's pick, not a model of that name
		return cursorDefaultContext
	}
	if n := catalog.ContextOf(base); n > 0 && n < cursorDefaultContext {
		return n
	}
	return cursorDefaultContext
}

// withCursorContexts fills in a saved list's contexts, fetched before
// magpie kept them.
func withCursorContexts(ms []catalog.Model) []catalog.Model {
	out := slices.Clone(ms)
	for i, m := range out {
		if m.Context == 0 {
			out[i].Context = cursorContext(m.ID, m.Name)
		}
	}
	return out
}

var cursorLoginURL = regexp.MustCompile(`https://\S+`)

// startCursorSignIn runs `cursor-agent login` without its browser, hands its
// link to the window, and finishes when the CLI says the account is in.
func startCursorSignIn(s *signInFlow) error {
	path := CursorExecutable()
	if path == "" {
		return errorf("install Cursor's CLI first: curl https://cursor.com/install -fsS | bash")
	}
	return runCLISignIn(s, "cursor-agent login", append(os.Environ(), "NO_OPEN_BROWSER=1"), true, nil, func() (string, string, bool) {
		forgetCursorStatus()
		user, plan, ok, _ := askCursorIdentity()
		return user, plan, ok
	}, path, "login")
}

// cursorVersionFallback is the CLI version said when no install names one.
const cursorVersionFallback = "2026.09.23-86fc751"

var cursorVersionRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}-[0-9a-f]+$`)

// CursorClientVersion is the cursor-agent the API is told it is talking
// to, "cli-<version>": the installed one, as the API turns away a version
// it no longer supports.
func CursorClientVersion() string {
	v := ""
	if p := CursorExecutable(); p != "" {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if d := filepath.Base(filepath.Dir(real)); cursorVersionRe.MatchString(d) {
				v = d
			}
		}
	}
	if v == "" {
		home, _ := os.UserHomeDir()
		dirs := []string{filepath.Join(home, ".local", "share", "cursor-agent", "versions")}
		if runtime.GOOS == "windows" {
			dirs = append(dirs, filepath.Join(os.Getenv("LOCALAPPDATA"), "cursor-agent", "versions"))
		}
		for _, dir := range dirs {
			es, _ := os.ReadDir(dir)
			for _, e := range es {
				if n := e.Name(); e.IsDir() && cursorVersionRe.MatchString(n) && n > v {
					v = n
				}
			}
		}
	}
	if v == "" {
		v = cursorVersionFallback
	}
	return "cli-" + v
}

// cursorAuthFile is where cursor-agent keeps its sign-in off a Mac's keychain.
func cursorAuthFile() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		dir := os.Getenv("APPDATA")
		if dir == "" {
			dir = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(dir, "Cursor", "auth.json")
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cursor", "auth.json")
}

// readCursorToken is the access token cursor-agent signed in with.
func readCursorToken() string {
	if runtime.GOOS == "darwin" {
		out, err := proc.Command("security", "find-generic-password", "-s", "cursor-access-token", "-a", "cursor-user", "-w").Output()
		if t := strings.TrimSpace(string(out)); err == nil && t != "" {
			return t
		}
	}
	b, err := os.ReadFile(cursorAuthFile())
	if err != nil {
		return ""
	}
	var a struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(b, &a)
	return a.AccessToken
}

// tokenExpiry is when a JWT runs out, zero when it doesn't say.
func tokenExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(c.Exp, 0)
}

var cursorRefresh sync.Mutex

// CursorToken is the token to call Cursor's API with. One about to run
// out is renewed by cursor-agent, which does that whenever it runs.
func CursorToken() (string, error) {
	tok := readCursorToken()
	if exp := tokenExpiry(tok); tok != "" && (exp.IsZero() || time.Until(exp) > 5*time.Minute) {
		return tok, nil
	}
	if path := CursorExecutable(); path != "" {
		cursorRefresh.Lock()
		if t := readCursorToken(); t != tok && t != "" {
			tok = t // renewed while this waited
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = agentCommand(ctx, path, "status").Run()
			cancel()
			tok = readCursorToken()
		}
		cursorRefresh.Unlock()
	}
	if tok == "" {
		return "", errors.New("Cursor isn't signed in; sign in from magpie's Providers page or run `cursor-agent login`")
	}
	if exp := tokenExpiry(tok); !exp.IsZero() && time.Until(exp) <= 0 {
		return "", errors.New("Cursor's sign-in has run out; sign in again from magpie's Providers page or run `cursor-agent login`")
	}
	return tok, nil
}
