package agent

// mu (qybaihe/mu, npm's mu-agent, the mu command and the mu desktop app) is
// Pi with a judgment kernel added as an extension: its kyrn/bin/mu.mjs
// starts Pi with the package named mu (piConfig {name: mu, configDir:
// .mu}), so Pi's settings.json and models.json live in mu's own agent
// folder, ~/.mu/agent, and the desktop app runs the same Pi on the same
// folder. magpie wires it as it does Pi: defaultProvider/defaultModel/
// defaultThinkingLevel in settings.json and magpie as the provider "magpie"
// in models.json. mu's first-run check (needsSetup in mu.mjs) asks for
// `mu setup` only while models.json has no provider and settings.json no
// defaultProvider, so magpie's wiring is a setup it takes.
//
// Its requests say pi/<version>, as Pi's do (pi-ai formatUserAgent), so the
// key magpie gives it names it (magpie-mu): that is how the gateway counts
// its requests as mu's and not Pi's.
//
// Its judge (mu.json, MU_JUDGE) is mu's own and is left alone.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func mu(home string) *Agent { return muIn(here(home)) }

// muIn is mu at a place: this machine's home, where its variables move it,
// or a WSL distro's, whose variables magpie can't read.
func muIn(at place) *Agent {
	a := piLikeKeyed(at, "mu", "mu", muDir(at), func() string { return agentKeyAt("mu", at.gw()) })
	a.Aliases = []string{"mu-agent"}
	a.binNot = notMuAgent
	return a
}

// muDir is mu's agent folder at a place, as agentDirFor in mu.mjs finds
// it: $MU_AGENT_DIR, $KYRN_AGENT_DIR, $MU_CODING_AGENT_DIR or
// $KYRN_CODING_AGENT_DIR ("~" in it standing for home, as Pi expands the
// PI_CODING_AGENT_DIR mu hands it), else agent/ in mu's home: ~/.mu, or
// ~/.kyrn, its old name, while that is a folder and ~/.mu isn't (muHome).
func muDir(at place) string {
	for _, k := range []string{"MU_AGENT_DIR", "KYRN_AGENT_DIR", "MU_CODING_AGENT_DIR", "KYRN_CODING_AGENT_DIR"} {
		if d := homeDir(at.home, at.getenv(k)); d != "" {
			return d
		}
	}
	if !at.cold && !isDir(at.join(".mu")) && isDir(at.join(".kyrn")) {
		return at.join(".kyrn", "agent")
	}
	return at.join(".mu", "agent")
}

// notMuAgent says a mu command found on PATH is another program's: the
// common one is maildir-utils' mu (djcb/mu, brew's and distros' mu), a
// compiled program. mu-agent's is its kyrn/bin/mu.mjs, reached through a
// link (npm's on a Mac and Linux, `mu link`'s) or a shim naming it (npm's
// mu.cmd and mu.ps1 on Windows).
func notMuAgent(bin string) bool {
	real := bin
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		real = r
	}
	if s := filepath.ToSlash(real); strings.HasSuffix(s, "/mu.mjs") || strings.Contains(s, "/mu-agent/") {
		return false
	}
	f, err := os.Open(real)
	if err != nil {
		return true
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	return !bytes.Contains(head, []byte("mu.mjs")) && !bytes.Contains(head, []byte("mu-agent"))
}
