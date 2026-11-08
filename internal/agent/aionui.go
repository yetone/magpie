package agent

// AionUi (an Electron app that runs Claude Code, Codex, Gemini CLI and its
// own chat) keeps its model providers in its own database, their keys
// encrypted, so there is nothing for magpie to write. It takes a new one
// through its deeplink, which opens its add-model form filled in for the
// user to confirm:
//
//	aionui://add-provider?api_key=…&base_url=…&name=…&platform=…
//
// (parseDeepLinkUrl in AionUi 2.2.2's main process; the renderer takes
// action "add-provider" to Settings → Model with base_url, api_key, name
// and platform.) So magpie's row is that link, the gateway as one
// OpenAI-compatible ("custom") provider, and whether AionUi has it, read
// from its database and never written.

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	_ "github.com/yetone/magpie/internal/sqlitedb"
)

// AionUiScheme begins every AionUi add-provider link.
const AionUiScheme = "aionui://add-provider?"

// AionUiLink is the link that opens AionUi's add-model form with the
// gateway at gw as an OpenAI-compatible provider named magpie, keyed so
// that its requests are counted as AionUi's. AionUi's OpenAI-compatible
// base URLs end in /v1, as its own OpenAI preset does.
func AionUiLink(gw string) string {
	v := url.Values{}
	v.Set("api_key", agentKeyAt("aionui", gw))
	v.Set("base_url", gw+"/v1")
	v.Set("name", magpieID)
	v.Set("platform", "custom")
	return AionUiScheme + v.Encode()
}

// aionuiDir is AionUi's Electron userData (~/Library/Application
// Support/AionUi, %APPDATA%\AionUi, ~/.config/AionUi); its database is
// aionui/aionui-backend.db in it.
func aionuiDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "AionUi")
}

// aionuiHas reports whether AionUi's database at path has a provider whose
// base URL is the gateway's. Read untouched, whether AionUi runs or not
// (its database is in WAL mode; sessions.OpenUntouched); no database is
// no, and one that can't be read is unknown (known false), never "not
// added".
func aionuiHas(path, gw string) (has, known bool) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, true
	} else if err != nil {
		return false, false
	}
	db, done, err := sessions.OpenUntouched(path)
	if err != nil {
		return false, false
	}
	defer done()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM providers WHERE instr(base_url, ?) > 0`, gw).Scan(&n); err != nil {
		return false, false
	}
	return n > 0, true
}

// dbStatus keeps the last read of an app's database (has: AionUi's,
// Cherry Studio's) while neither it nor its log changed, for at most
// reachedFor (a coarse clock can miss a write that kept the size), so the
// Agents page's poll doesn't copy a running app's database each time.
type dbStatus struct {
	has       func(path, gw string) (bool, bool)
	mu        sync.Mutex
	gw        string
	db, wal   fileStamp
	at        time.Time
	ok, known bool
}

func (s *dbStatus) read(path, gw string) (bool, bool) {
	db, wal := stampOf(path), stampOf(path+"-wal")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known && s.gw == gw && s.db == db && s.wal == wal && time.Since(s.at) < reachedFor {
		return s.ok, s.known
	}
	// a read that failed is tried again on the next poll
	s.ok, s.known = s.has(path, gw)
	s.gw, s.db, s.wal, s.at = gw, db, wal, time.Now()
	return s.ok, s.known
}

func aionui() *Agent {
	dir := aionuiDir()
	db := filepath.Join(dir, "aionui", "aionui-backend.db")
	st := &dbStatus{has: aionuiHas}
	return &Agent{
		ID: "aionui", Name: "AionUi", Icon: "aionui",
		Dir: dir, Path: dir,
		detect: func() bool { return dir != "" && isDir(filepath.Join(dir, "aionui")) },
		Import: func() string { return AionUiLink(gateway.URL()) },
		Added:  func() (bool, bool) { return st.read(db, gateway.URL()) },
	}
}
