package provider

// PLUGIN-SERVED (see AGENTS.md): Cursor ("cursor") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-cursor-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/cursor) and raise the
// mover's min in internal/provider/migrate_side.go.

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
	"net/url"
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

	"github.com/yetone/magpie/internal/appdir"
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

var cursorStatus = &cliIdentity{name: "cursor", exe: func() string { return CursorExecutable() }, ask: askCursorStatus}

// cursorIdentity is who Cursor's CLI says is signed in; see cliIdentity.
func cursorIdentity() (user, plan string, ok bool) { return cursorStatus.get() }

// forgetCursorStatus has who is signed in, and the token, read afresh.
func forgetCursorStatus() {
	cursorStatus.forget()
	cursorTok.Lock()
	cursorTok.at = time.Time{}
	cursorTok.Unlock()
}

func askCursorIdentity() (user, plan string, ok bool) {
	user, plan, ok, _ = askCursorStatus()
	return user, plan, ok
}

// askCursorStatus asks `cursor-agent about` who is signed in. It is sure of
// nobody only when the CLI says so or keeps no token: an about that fails,
// runs out of time (it asks Cursor's servers) or prints something other
// than its JSON couldn't tell, and the account stays as it was (#154).
func askCursorStatus() (user, plan string, ok bool, err error) {
	path := CursorExecutable()
	if path == "" {
		return "", "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := agentProbe(ctx, path, "about", "--format", "json").Output()
	user, plan, said := parseCursorAbout(out)
	switch {
	case user != "":
		return user, plan, true, nil
	case said && err == nil, cursorSignedOut():
		return "", "", false, nil
	case err == nil:
		err = errors.New("cursor-agent about printed no account")
	}
	return "", "", false, err
}

// parseCursorAbout reads `cursor-agent about --format json`: the email and
// plan, and whether it printed that JSON at all — after a line of its own
// (an update notice), too.
func parseCursorAbout(out []byte) (user, plan string, said bool) {
	s := strings.TrimSpace(ansi.ReplaceAllString(string(out), ""))
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	var about struct {
		SubscriptionTier string `json:"subscriptionTier"`
		UserEmail        string `json:"userEmail"`
	}
	if json.Unmarshal([]byte(s), &about) != nil {
		return "", "", false
	}
	return strings.TrimSpace(about.UserEmail), strings.TrimSpace(about.SubscriptionTier), true
}

// cursorSignedOut is cursor-agent keeping no token, which is signed out
// whatever about said; a var so tests can fake it.
var cursorSignedOut = func() bool { return readCursorToken() == "" }

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
		// and the picker's models `cursor-agent models` leaves out
		if tok, err := cursorToken(); err == nil {
			ms = append(ms, cursorPickerModels(ctx, tok, ms)...)
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
	out, err := agentProbe(ctx, path, "models").Output()
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
		// some names come with zero-width spaces and doubled ones
		name := strings.Join(strings.Fields(strings.ReplaceAll(m[2], "\u200b", "")), " ")
		name = strings.TrimSpace(strings.TrimSuffix(name, "(default)"))
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
		return askCursorIdentity()
	}, cursorLinkWhole, path, "login")
}

// cursorLinkWhole says a link from `cursor-agent login` carries what
// cursor.com/loginDeepControl signs in with: a link cut short where the
// CLI broke its line (#261: "https://cursor.com/loginDeepControl?") gets
// "This sign-in link is incomplete or has expired" from the page.
func cursorLinkWhole(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	q := u.Query()
	// The CLI may wrap after the UUID, with the remaining login parameters
	// arriving in another pipe write. Credentials alone do not finish its URL.
	return q.Get("challenge") != "" && cursorLoginUUID.MatchString(q.Get("uuid")) &&
		q.Get("mode") == "login" && q.Get("redirectTarget") == "cli" &&
		(q.Get("supportsSelectedTeamLogin") == "true" || q.Get("supportsSelectedTeamLogin") == "false")
}

var cursorLoginUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
			dirs = append(dirs, filepath.Join(appdir.Getenv("LOCALAPPDATA"), "cursor-agent", "versions"))
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
		dir := appdir.Getenv("APPDATA")
		if dir == "" {
			dir = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(dir, "Cursor", "auth.json")
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	}
	dir := appdir.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cursor", "auth.json")
}

// cursorTok is the token cursor-agent keeps in the Keychain as last read,
// and when: every look at whether Cursor is signed in ran `security`, a
// plugin's accounts for each build of the providers (#746).
var cursorTok struct {
	sync.Mutex
	tok string
	at  time.Time
}

// cursorTokenTTL is how long a token read from the Keychain is taken as
// it: a sign-in or out in magpie reads it afresh (forgetCursorStatus), one
// made in cursor-agent is seen within it.
const cursorTokenTTL = 10 * time.Second

// cursorKeychainToken is cursor-agent's token in the Keychain, "" for none;
// fresh reads it whatever was read last.
func cursorKeychainToken(fresh bool) string {
	cursorTok.Lock()
	defer cursorTok.Unlock()
	if !fresh && time.Since(cursorTok.at) < cursorTokenTTL {
		return cursorTok.tok
	}
	out, err := proc.Command("security", "find-generic-password", "-s", "cursor-access-token", "-a", "cursor-user", "-w").Output()
	cursorTok.tok, cursorTok.at = "", time.Now()
	if err == nil {
		cursorTok.tok = strings.TrimSpace(string(out))
	}
	return cursorTok.tok
}

// readCursorToken is the access token cursor-agent signed in with.
func readCursorToken() string { return cursorTokenOf(false) }

// cursorTokenOf is readCursorToken, fresh from the Keychain when asked.
func cursorTokenOf(fresh bool) string {
	if runtime.GOOS == "darwin" {
		if t := cursorKeychainToken(fresh); t != "" {
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
		if t := cursorTokenOf(true); t != tok && t != "" {
			tok = t // renewed while this waited
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = agentProbe(ctx, path, "status").Run()
			cancel()
			tok = cursorTokenOf(true)
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
