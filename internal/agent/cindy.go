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
	"slices"
	"time"

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

// cindyBuilds are Cindy's two builds, each with its own data folder under
// the user's config folder (Electron's appData: ~/Library/Application
// Support, %APPDATA%, $XDG_CONFIG_HOME or ~/.config) and its own bundle id
// (userDataDirNameByRegion and appIdByRegion in maker-shared's
// brandIdentity.ts, Cindy 0.1.148): the cn build keeps Electron's default
// folder, Cindy, from productName; the global build moves it to
// CindyGlobal before anything is read (regionUserData.ts). Both are
// Cindy.app on a Mac.
var cindyBuilds = []struct{ dir, id string }{{"Cindy", "com.xd.cindycn"}, {"CindyGlobal", "com.xd.cindy"}}

// CindyFolders are the data folders of both of Cindy's builds, here or not.
func CindyFolders() []string {
	d, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range cindyBuilds {
		out = append(out, filepath.Join(d, b.dir))
	}
	return out
}

// cindyUsed is when Cindy last wrote its database in d (cindy-local-v1.db
// before a sign-in, cindy-<account>.db after, and the -wal and -shm beside
// them), zero when d holds none. That database is what says a build keeps
// its data in d: the global build makes the cn build's folder too, for
// what the two share (shared-system-skills, shared-skill-mutation-locks,
// shared-worktree-recycle-journals: built-in-skill-activation.ts,
// sharedMutationLease.ts and recycleJournal.ts write them under appData's
// Cindy whatever the build), and magpie's own writes there made
// codex-home and pi-agent-home in it.
func cindyUsed(d string) time.Time {
	var at time.Time
	ps, _ := filepath.Glob(filepath.Join(d, "cindy-*.db*"))
	for _, p := range ps {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(at) {
			at = fi.ModTime()
		}
	}
	return at
}

// CindyData reports whether d is a folder a build of Cindy keeps its data
// in (cindyUsed), not only one it or magpie made files in.
func CindyData(d string) bool { return !cindyUsed(d).IsZero() }

// cindyDirs are Cindy's data folders, the one it reads first: those with a
// build's database in them (cindyUsed) before those without; of two with
// one, on a Mac the installed build's (Cindy.app's bundle id; only one
// Cindy.app can be in a folder), else the one written last; then those
// that are there. Before, the cn build's Cindy came first whenever it was
// there, and on the global build it always is (cindyUsed), so the Library
// wrote Cindy's MCP servers into a Cindy/codex-home/config.toml nothing
// reads, and CindyGlobal's, which the global build's Codex reads, had none.
func cindyDirs() []string {
	type folder struct {
		path  string
		used  time.Time
		there bool
		app   bool
	}
	var fs []folder
	both := 0
	for _, p := range CindyFolders() {
		f := folder{path: p, used: cindyUsed(p), there: isDir(p)}
		if !f.used.IsZero() {
			both++
		}
		fs = append(fs, f)
	}
	if both > 1 && removedOS == "darwin" {
		for i := range fs {
			fs[i].app = macBundle(desktopApp{id: cindyBuilds[i].id, names: []string{"Cindy.app"}})
		}
	}
	slices.SortStableFunc(fs, func(a, b folder) int {
		switch {
		case a.used.IsZero() != b.used.IsZero():
			return trueFirst(!a.used.IsZero(), !b.used.IsZero())
		case a.used.IsZero():
			return trueFirst(a.there, b.there)
		case a.app != b.app:
			return trueFirst(a.app, b.app)
		}
		return b.used.Compare(a.used)
	})
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.path
	}
	return out
}

// trueFirst orders true before false.
func trueFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// cindyClaudeHere reports whether Cindy has its own Claude Code here:
// <its data folder>/claude-code/<version>/claude, claude.exe on Windows
// (the claude-code kind's installSubdir and binaryName in
// apps/desktop/src/main/agent-binaries, Cindy 0.1.146). It runs with the
// user's ~/.claude and ~/.claude.json, as Claude Code does, and makes them
// on its first run (T_t__biu on X, #7872).
func cindyClaudeHere() bool { return cindyBundled("claude-code", exe("claude")) }

// cindyCodexHere reports whether Cindy has its own Codex here:
// <its data folder>/codex-package/<version>/bin/codex, codex.exe on
// Windows (the codex kind's installSubdir and binaryName in
// apps/desktop/src/main/agent-binaries/binaryStatus.ts, Cindy 0.1.148).
// Cindy runs it with CODEX_HOME at its own codex-home, but Codex makes
// <CODEX_HOME>/tmp/arg0-<os> on any run (codex-rs/arg0), so a run of it
// without that variable makes ~/.codex (Cindy 0.1.148 gives even its
// version read a temporary one so as not to, binary-version-probe.ts),
// and Cindy installs skills into ~/.codex/skills. It reads
// ~/.codex/AGENTS.md (copied into codex-home, codex-global-rules.ts) and
// ~/.codex/skills (linked, codex-global-skills.ts).
func cindyCodexHere() bool { return cindyBundled("codex-package", "bin", exe("codex")) }

// cindyBundled reports whether either of Cindy's data folders has its own
// copy of an agent: <folder>/<kind>/<version>/<rel…>, the program itself
// and not only its version folder (a download not finished).
func cindyBundled(kind string, rel ...string) bool {
	for _, d := range cindyDirs() {
		vs, err := os.ReadDir(filepath.Join(d, kind))
		if err != nil {
			continue
		}
		for _, v := range vs {
			if v.IsDir() && isFile(filepath.Join(append([]string{d, kind, v.Name()}, rel...)...)) {
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
