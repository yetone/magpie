package agent

// Cindy (a desktop app that runs Claude Code, Codex and Pi) keeps its
// providers in its own database, their keys encrypted, and takes a new one
// only through its import link, which the user confirms in Cindy:
//
//	cindy://provider/import?v=1&data=<base64url of the provider's JSON>
//
// So magpie has nothing to set there: its row is the link, magpie as one
// custom provider with an endpoint for each of Cindy's runtimes, and
// whether Cindy has it already, read from Cindy's database and never
// written.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	_ "github.com/yetone/magpie/internal/sqlitedb"
)

// CindyScheme begins every Cindy import link.
const CindyScheme = "cindy://provider/import?"

type cindyEndpoint struct {
	Protocol  string   `json:"protocol"`
	BaseURL   string   `json:"baseUrl"`
	Targets   []string `json:"targets"`
	ModelsURL string   `json:"modelsUrl"`
}

// CindyLink is the link that adds the gateway at url to Cindy as the
// provider Magpie, keyed so that its requests are counted as Cindy's.
func CindyLink(url string) string {
	v1 := url + "/v1"
	models := v1 + "/models"
	payload := struct {
		Kind      string          `json:"kind"`
		Name      string          `json:"name"`
		ID        string          `json:"id"`
		Auth      map[string]any  `json:"auth"`
		Endpoints []cindyEndpoint `json:"endpoints"`
	}{
		Kind: "custom", Name: "Magpie", ID: magpieID,
		Auth: map[string]any{"method": "apiKey", "apiKey": agentKeyAt("cindy", url)},
		Endpoints: []cindyEndpoint{
			{"anthropic-messages", url, []string{"claude-code"}, models},
			{"openai-responses", v1, []string{"codex"}, models},
			{"openai-chat", v1, []string{"pi"}, models},
		},
	}
	b, _ := json.Marshal(payload)
	return CindyScheme + "v=1&data=" + base64.RawURLEncoding.EncodeToString(b)
}

// cindyHas reports whether Cindy's database at path has magpie among its
// custom providers: by the id or name the link gives it, or by the
// gateway's address in its endpoints. Read untouched, whether Cindy runs or
// not (sessions.OpenUntouched); no database is no, and one that can't be
// read is unknown (known false), never "not added".
func cindyHas(path, gw string) (has, known bool) {
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
	err = db.QueryRow(`SELECT count(*) FROM custom_providers
		WHERE id = ? OR lower(name) = ? OR instr(runtimes, ?) > 0`, magpieID, magpieID, gw).Scan(&n)
	if err != nil {
		return false, false
	}
	return n > 0, true
}

// cindyDirs are where Cindy keeps its data (~/Library/Application
// Support/Cindy on a Mac, CindyGlobal for its global build), those there
// first: there once Cindy was installed and opened.
func cindyDirs() []string {
	d, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	var have, not []string
	for _, n := range []string{"Cindy", "CindyGlobal"} {
		p := filepath.Join(d, n)
		if _, err := os.Stat(p); err == nil {
			have = append(have, p)
		} else {
			not = append(not, p)
		}
	}
	return append(have, not...)
}

// cindyClaudeHere reports whether Cindy has its own Claude Code here:
// <its data folder>/claude-code/<version>/claude, claude.exe on Windows
// (the claude-code kind's installSubdir and binaryName in
// apps/desktop/src/main/agent-binaries, Cindy 0.1.146). It runs with the
// user's ~/.claude and ~/.claude.json, as Claude Code does, and makes them
// on its first run (T_t__biu on X, #7872).
func cindyClaudeHere() bool {
	for _, d := range cindyDirs() {
		vs, err := os.ReadDir(filepath.Join(d, "claude-code"))
		if err != nil {
			continue
		}
		for _, v := range vs {
			if v.IsDir() && isFile(filepath.Join(d, "claude-code", v.Name(), exe("claude"))) {
				return true
			}
		}
	}
	return false
}

// cindyAdded reports whether any of Cindy's databases in dirs has magpie:
// one per account (cindy-<account>.db) once signed in, cindy-local-v1.db
// before. One that has it is enough; else one that couldn't be read leaves
// it unknown.
func cindyAdded(dirs []string, gw string) (added, known bool) {
	known = true
	for _, d := range dirs {
		dbs, err := filepath.Glob(filepath.Join(d, "cindy-*.db"))
		if err != nil {
			known = false
		}
		for _, db := range dbs {
			has, ok := cindyHas(db, gw)
			if has {
				return true, true
			}
			known = known && ok
		}
	}
	return false, known
}

func cindy() *Agent {
	dirs := cindyDirs()
	dir := ""
	if len(dirs) > 0 {
		dir = dirs[0]
	}
	return &Agent{
		ID: "cindy", Name: "Cindy", Icon: "cindy",
		UA:  []string{"cindy"},
		Dir: dir, Path: dir,
		detect: func() bool {
			for _, d := range dirs {
				if isDir(d) {
					return !macAppRemoved("cindy")
				}
			}
			return false
		},
		Import: func() string { return CindyLink(gateway.URL()) },
		Added:  func() (bool, bool) { return cindyAdded(dirs, gateway.URL()) },
	}
}
