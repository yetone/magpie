package provider

// PLUGIN-SERVED (see AGENTS.md): Cursor ("cursor") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-cursor-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/cursor) and raise the
// mover's min in internal/provider/migrate_side.go.

// Cursor's built-in is retired (retiredBuiltins): what is left here is who
// cursor-agent is signed in to and the token that sign-in keeps, which the
// move (migrate_side.go) and the plugin's own-sign-in marker
// (plugin_accounts.go) read, and the contexts of Cursor's models.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// CursorExecutable finds the cursor-agent CLI; a var so tests can fake it.
var CursorExecutable = func() string {
	for _, name := range []string{"cursor-agent", "agent"} {
		if p, err := proc.LookPath(name); err == nil && (name == "cursor-agent" || isCursorAgent(p)) {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	for _, p := range append([]string{filepath.Join(home, ".local", "bin", "cursor-agent")}, proc.SystemDirs("/usr/local/bin/cursor-agent", "/opt/homebrew/bin/cursor-agent")...) {
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

var cursorStatus = &cliIdentity{name: "cursor", exe: func() string { return CursorExecutable() }, ask: askCursorStatus, mark: cursorSignInMark}

// cursorSignInMark is who cli-config.json says signed in, its authInfo, as
// the mark cliIdentity asks again on a change of: about rewrites the file
// on every run, so its size and time can't be the mark. "" when it can't
// be read.
func cursorSignInMark() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".cursor", "cli-config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "none"
		}
		return ""
	}
	var c struct {
		AuthInfo json.RawMessage `json:"authInfo"`
	}
	if json.Unmarshal(b, &c) != nil {
		return "" // half-written: asked
	}
	return "auth:" + string(c.AuthInfo)
}

// cursorIdentity is who Cursor's CLI says is signed in; see cliIdentity.
func cursorIdentity() (user, plan string, ok bool) {
	if settings.AccountDiscoveryOff() {
		return "", "", false
	}
	return cursorStatus.get()
}

// forgetCursorStatus has who is signed in, and the token, read afresh.
func forgetCursorStatus() {
	cursorStatus.forget()
	cursorTok.Lock()
	cursorTok.at, cursorTok.ask = time.Time{}, time.Time{}
	cursorTok.Unlock()
}

// askCursorStatus asks `cursor-agent about` who is signed in. It is sure of
// nobody only when the CLI says so or keeps no token: an about that fails,
// runs out of time (it asks Cursor's servers) or prints something other
// than its JSON couldn't tell, and the account stays as it was (#154).
func askCursorStatus() (user, plan string, ok bool, err error) {
	if settings.AccountDiscoveryOff() {
		return "", "", false, nil
	}
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

var ansi = lazyre.New(`\x1b\[[0-9;?]*[A-Za-z]`)

// cursorDefaultContext is the context Cursor gives a model it doesn't name
// as a 1M one.
const cursorDefaultContext = 200_000

var (
	cursorMillion = lazyre.New(`\b(\d+)M\b`)
	cursorVariant = lazyre.New(`-(fast|none|low|medium|high|xhigh|extra-high|max|thinking)$`)
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

var cursorLoginURL = lazyre.New(`https://\S+`)

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
// plugin's accounts for each build of the providers (#746). ask is when a
// read could have shown the user a dialog: one the user answered no to is
// not asked again inside keychainDeniedTTL (#2408).
var cursorTok struct {
	sync.Mutex
	tok string
	at  time.Time
	ask time.Time
}

// cursorTokenTTL is how long a token read from the Keychain is taken as
// it: a sign-in or out in magpie reads it afresh (forgetCursorStatus), one
// made in cursor-agent is seen within it.
const cursorTokenTTL = 10 * time.Second

// cursorKeychainToken is cursor-agent's token in the Keychain, "" for none;
// fresh reads it whatever was read last.
func cursorKeychainToken(fresh bool) string {
	if settings.AccountDiscoveryOff() {
		return ""
	}
	cursorTok.Lock()
	defer cursorTok.Unlock()
	ttl := cursorTokenTTL
	if cursorTok.tok == "" && time.Since(cursorTok.ask) < keychainDeniedTTL {
		ttl = keychainDeniedTTL
	}
	if !fresh && time.Since(cursorTok.at) < ttl {
		return cursorTok.tok
	}
	out, err := proc.Command("security", "find-generic-password", "-s", "cursor-access-token", "-a", "cursor-user", "-w").Output()
	cursorTok.tok, cursorTok.at = "", time.Now()
	switch {
	case err == nil:
		cursorTok.tok = strings.TrimSpace(string(out))
		cursorTok.ask = time.Time{}
	case keychainDenied(err):
		cursorTok.ask = cursorTok.at
	default:
		cursorTok.ask = time.Time{}
	}
	return cursorTok.tok
}

// readCursorToken is the access token cursor-agent signed in with.
func readCursorToken() string { return cursorTokenOf(false) }

// cursorTokenOf is readCursorToken, fresh from the Keychain when asked.
func cursorTokenOf(fresh bool) string {
	if settings.AccountDiscoveryOff() {
		return ""
	}
	if runtime.GOOS == "darwin" {
		if t := cursorKeychainToken(fresh); t != "" {
			return t
		}
	}
	b, err := readAccountFile(cursorAuthFile())
	if err != nil {
		return ""
	}
	var a struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(b, &a)
	return a.AccessToken
}
