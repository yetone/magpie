package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// readsEnv matches the two ways these packages name a variable they read to
// find an agent's folder: as the argument of a Getenv, a LookupEnv or a
// place's getenv, and as the env of a table entry, which is how Qoder's two
// builds are named (read through at.getenv(b.env)).
var readsEnv = regexp.MustCompile(`(?:Getenv|LookupEnv|getenv)\("([A-Z][A-Z_0-9]*)"\)|env:\s*"([A-Z][A-Z_0-9]*)"`)

// notAnAgent are the variables the same sources read that are not an agent's
// folder, so no sandbox clears them through agentenv.Vars.
var notAnAgent = map[string]string{
	"APPDATA":               "Windows' own folder, which a sandbox sets to one of its own rather than clears",
	"LOCALAPPDATA":          "the same",
	"PATH":                  "the process' own",
	"USER":                  "the process' own",
	"XDG_CONFIG_HOME":       "magpie's own folder's, which appdir decides",
	"XDG_DATA_HOME":         "the same",
	"XDG_DATA_DIRS":         "the system's own data folders, where a .deb puts Lody's desktop entry; a test that reaches agent.OpenLody sets it to its own, and DeepSeek Harness Desktop's entries are read there through proc.SystemDirs",
	"XDG_CACHE_HOME":        "Go's build cache's too, which a sandbox keeps shared; Air's ACP OpenCode under it is found by path in a test's own home (#2423)",
	"MAGPIE_ADDR":           "magpie's own",
	"MAGPIE_DEBUG":          "magpie's own debug flag",
	"MAGPIE_PUBLIC_URL":     "magpie's own public URL",
	"MAGPIE_TRUST_PROXY":    "magpie's own: a proxy here signs its clients in (#1022)",
	"MAGPIE_EFFORT_UPDATES": "magpie's own switch for effort updates (#617)",
	"MAGPIE_SITE_ORIGIN":    "a site origin magpie's Omarchy theme answers, to try the site locally",
	"MAGPIE_PARTNERS":       "where magpie's own partner list is read from (a URL, or off), not a folder",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "Claude Code's behaviour flag, not an agent's folder",
	"GIT_SSH":             "the user's own SSH program for git, which a skill fetched with their git keeps",
	"GIT_SSH_COMMAND":     "the same, as a command",
	"XDG_CURRENT_DESKTOP": "the desktop the user runs, which says whether xdg-mime's answer about an app's links is the whole one (agent.OpenAppLink), not a folder",
	"TZ":                  "the machine's time zone, which a routing rule's hours fall back to (c888ef272), not a folder",
}

// TestFolderVarsAreListed says every variable the packages that find an
// agent's folder read is one a sandbox clears. Nothing said so before, so a
// new agent could take a variable of its own and every sandbox would go on
// meeting the agent a developer really has installed — which is how #522 came
// about, four sandboxes clearing lists of their own that had each drifted.
//
// The five packages are this one, sessions, provider, library and gateway,
// with appdir's codex.go, where CODEX_HOME is read for all of them:
// every agent's folder variable magpie reads is read in one of them, and the
// rest of the repository reads only magpie's own variables, the process' and
// the desktop's. Their sources are read as files rather than imported, since a
// test in sessions or provider cannot import this package, which imports
// them; what the walk looks at is what they say, not what they do.
//
// What this cannot see is a variable read through a name the sources build at
// runtime, as omoDir reads OMO_CODING_AGENT_DIR and SENPI_CODING_AGENT_DIR
// out of a slice, or through a constant (const fooEnv = "FOO_HOME" and then
// os.Getenv(fooEnv)). Both are in agentenv.Vars; a third taken either way
// would not be caught here.
func TestFolderVarsAreListed(t *testing.T) {
	listed := make(map[string]bool, len(agentenv.Vars))
	for _, v := range agentenv.Vars {
		listed[v] = true
	}
	dirs := []string{".", filepath.Join("..", "sessions"), filepath.Join("..", "provider"), filepath.Join("..", "library"), filepath.Join("..", "gateway")}
	found := map[string]string{}
	var files []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			files = append(files, filepath.Join(dir, n))
		}
	}
	files = append(files, filepath.Join("..", "appdir", "codex.go"))
	for _, n := range files {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range readsEnv.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if _, ok := found[name]; !ok {
				found[name] = n
			}
		}
	}
	// One name read each way the pattern matches, so that a change in how a
	// variable is read empties the scan loudly instead of leaving the test
	// nothing to complain of.
	for _, canary := range []string{"CODEX_HOME", "QODER_CONFIG_DIR"} {
		if _, ok := found[canary]; !ok {
			t.Errorf("%s was not found in these sources, so readsEnv no longer matches the way a variable is read", canary)
		}
	}
	// And no excuse outlives the variable it was for.
	for n := range notAnAgent {
		if _, ok := found[n]; !ok {
			t.Errorf("%s is in notAnAgent, but nothing in these sources reads it any more", n)
		}
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if listed[n] || notAnAgent[n] != "" {
			continue
		}
		t.Errorf("%s, read by %s, is not in agentenv.Vars, so no sandbox clears it and a test meets the agent a developer really has installed: add it to Vars, or to notAnAgent here with a reason if it is not an agent's folder", n, found[n])
	}
}
