package agent

// Cherry Studio (CherryHQ's Electron chat client) keeps its model
// providers in its own SQLite database, so there is nothing for magpie to
// write. It takes a new one through its providers link, which opens
// Settings → Model Provider with a dialog for the user to confirm:
//
//	cherrystudio://providers/api-keys?v=1&data=<base64 of {id, baseUrl, apiKey, name, type}>
//
// (handleProvidersProtocolUrl in its main process, 1.x and 2.x alike; 2.x's
// renderer creates the provider in useProviderDeepLinkImport.) So magpie's
// row is that link, the gateway as one OpenAI-compatible provider, and
// whether Cherry Studio has it, read from its database and never written.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	_ "github.com/yetone/magpie/internal/sqlitedb"
)

// CherryStudioScheme begins every Cherry Studio add-provider link.
const CherryStudioScheme = "cherrystudio://providers/api-keys?"

// CherryStudioLink is the link that opens Cherry Studio's add-provider
// dialog with the gateway at gw as an OpenAI-compatible provider named
// magpie, keyed so that its requests are counted as Cherry Studio's.
//
// Cherry Studio reads data as standard base64: before URLSearchParams sees
// the query it turns a raw + into _ and / into -, and back again after, so
// URL-safe base64 (whose - and _ mean + and /) would decode wrong. The
// data is query-escaped, which its URLSearchParams undoes. The base URL
// ends in /v1, as its own example's does; its formatApiHost keeps a URL
// that already has a version, and type openai is its OpenAI chat
// completions endpoint, whose models it lists from <base>/models.
func CherryStudioLink(gw string) string {
	b, _ := json.Marshal(struct {
		ID      string `json:"id"`
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Name    string `json:"name"`
		Type    string `json:"type"`
	}{magpieID, gw + "/v1", agentKeyAt("cherrystudio", gw), magpieID, "openai"})
	return CherryStudioScheme + "v=1&data=" + url.QueryEscape(base64.StdEncoding.EncodeToString(b))
}

// cherryUserData is Cherry Studio's Electron userData by default
// (~/Library/Application Support/CherryStudio, %APPDATA%\CherryStudio,
// ~/.config/CherryStudio): its package name, CherryStudio.
func cherryUserData() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "CherryStudio")
}

// cherryDirs is every userData Cherry Studio may use here: the default and
// each one the user moved it to, which it keeps in
// ~/.cherrystudio/boot-config.json under app.user_data_path (one per
// executable). ok is false when that file is there but can't be read.
func cherryDirs(home string) (dirs []string, ok bool) {
	if d := cherryUserData(); d != "" {
		dirs = append(dirs, d)
	}
	if home == "" {
		return dirs, true
	}
	b, err := os.ReadFile(filepath.Join(home, ".cherrystudio", "boot-config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return dirs, true
	}
	var cfg struct {
		Moved map[string]string `json:"app.user_data_path"`
	}
	if err != nil || json.Unmarshal(b, &cfg) != nil {
		return dirs, false
	}
	for _, d := range cfg.Moved {
		if filepath.IsAbs(d) {
			dirs = append(dirs, filepath.Clean(d))
		}
	}
	return dirs, true
}

// cherryDB is where Cherry Studio 2.x keeps its database in userData dir.
func cherryDB(dir string) string { return filepath.Join(dir, "Data", "cherrystudio.sqlite") }

// cherryRan says Cherry Studio has run with userData dir: 2.x's Data
// folder, or Chromium's Local Storage, where 1.x kept its providers.
func cherryRan(dir string) bool {
	return isDir(filepath.Join(dir, "Data")) || isDir(filepath.Join(dir, "Local Storage"))
}

// cherryHas reports whether Cherry Studio's database at path has a
// provider whose base URL is the gateway's, read untouched (WAL mode,
// sessions.OpenUntouched). One that can't be read is unknown.
func cherryHas(path, gw string) (has, known bool) {
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
	// endpoint_configs is JSON.stringify'd {"<endpoint>": {"baseUrl": …}};
	// the gateway's address is followed by its path or the closing quote,
	// so another port that begins with this one's doesn't count
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM user_provider WHERE instr(endpoint_configs, ?) > 0 OR instr(endpoint_configs, ?) > 0`,
		`"`+gw+`/`, `"`+gw+`"`).Scan(&n); err != nil {
		return false, false
	}
	return n > 0, true
}

// cherryStatus reads each userData's database through its own dbStatus.
type cherryStatus struct {
	mu   sync.Mutex
	dirs map[string]*dbStatus
}

// read says whether any of Cherry Studio's databases has the gateway. A
// userData that has run 1.x only (no database) can't be read, so it is
// unknown, as is a database or boot-config that can't be read.
func (s *cherryStatus) read(home, gw string) (has, known bool) {
	dirs, known := cherryDirs(home)
	for _, d := range dirs {
		db := cherryDB(d)
		if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) {
			if cherryRan(d) {
				known = false
			}
			continue
		}
		s.mu.Lock()
		if s.dirs == nil {
			s.dirs = map[string]*dbStatus{}
		}
		st := s.dirs[db]
		if st == nil {
			st = &dbStatus{has: cherryHas}
			s.dirs[db] = st
		}
		s.mu.Unlock()
		h, k := st.read(db, gw)
		if h {
			return true, true
		}
		if !k {
			known = false
		}
	}
	return false, known
}

func cherryStudio(home string) *Agent {
	dir := cherryUserData()
	st := &cherryStatus{}
	return &Agent{
		ID: "cherrystudio", Name: "Cherry Studio", Icon: "cherrystudio-color",
		Aliases: []string{"cherry-studio"},
		Dir:     dir, Path: dir,
		detect: func() bool {
			dirs, _ := cherryDirs(home)
			for _, d := range dirs {
				if cherryRan(d) {
					return true
				}
			}
			return false
		},
		Import: func() string { return CherryStudioLink(gateway.URL()) },
		Added:  func() (bool, bool) { return st.read(home, gateway.URL()) },
	}
}
