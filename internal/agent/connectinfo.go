package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

// What the Agents page says beside an agent's 「接入」 switch (the owner's
// design): before it is on, what the agent runs on now — its own
// subscription, sign-in or providers — so the user can tell what turning
// it on touches; once on, whether copies of it already running still have
// the list they started with.

// Source is what an agent not connected to magpie runs on now: "sub" (a
// Claude subscription), "chatgpt" (a ChatGPT sign-in), "key" (an API key
// of its own), "providers:N" (N providers of its own); "" when unknown.
func (a *Agent) Source() string {
	if a.WSL != "" || a.Path == "" {
		return ""
	}
	dir := filepath.Dir(a.Path)
	switch a.ID {
	case "claude":
		// signed in to claude.ai: ~/.claude.json (or the one in
		// CLAUDE_CONFIG_DIR) has the account
		for _, p := range []string{filepath.Join(dir, ".claude.json"), filepath.Join(filepath.Dir(dir), ".claude.json")} {
			if v, _ := edit.GetJSON(p, "oauthAccount.accountUuid"); v != "" {
				return "sub"
			}
		}
		if v, _ := edit.GetJSON(a.Path, "env.ANTHROPIC_API_KEY"); v != "" {
			return "key"
		}
		if v, _ := edit.GetJSON(a.Path, "apiKeyHelper"); v != "" {
			return "key"
		}
	case "codex":
		if codexChatGPT(dir) {
			return "chatgpt"
		}
		if v, _ := edit.GetJSON(filepath.Join(dir, "auth.json"), "OPENAI_API_KEY"); v != "" {
			return "key"
		}
	case "opencode", "mimocode":
		// the providers in its config, and those signed in through it
		n := map[string]bool{}
		var cfg map[string]json.RawMessage
		if v, _ := edit.GetJSON(a.Path, "provider"); json.Unmarshal([]byte(v), &cfg) == nil {
			for k := range cfg {
				n[k] = true
			}
		}
		home, _ := os.UserHomeDir()
		var auth map[string]json.RawMessage
		if b, err := os.ReadFile(filepath.Join(home, ".local", "share", a.ID, "auth.json")); err == nil && json.Unmarshal(b, &auth) == nil {
			for k := range auth {
				n[k] = true
			}
		}
		delete(n, magpieID)
		if len(n) > 0 {
			return "providers:" + strconv.Itoa(len(n))
		}
	}
	return ""
}

// startsWith is, for an agent that reads magpie's models only as it
// starts, how to find its processes (patterns for pgrep -f), the files it
// reads them from, and the part of those files magpie writes for it. The
// files' times alone don't say when magpie changed that part: the agent
// writes the same files (Codex keeps each project's trust, a notice seen,
// the model picked in its app in config.toml), and a copy started before
// such a write has magpie's list all the same (#729).
func (a *Agent) startsWith() (pats []string, files []string, reads func() string) {
	switch a.ID {
	case "codex":
		list := filepath.Join(filepath.Dir(a.Path), "magpie-models.json")
		return []string{`(^|/)codex( |$)`}, []string{a.Path, list}, func() string {
			var b strings.Builder
			for _, k := range []string{"model_provider", "model_catalog_json", "openai_base_url"} {
				v, _ := edit.GetTOMLTop(a.Path, k)
				b.WriteString(k + "=" + v + "\n")
			}
			t, _ := edit.GetTOMLTable(a.Path, "model_providers."+magpieID)
			j, _ := json.Marshal(t)
			b.Write(j)
			c, _ := os.ReadFile(list)
			b.Write(c)
			return b.String()
		}
	case "claude":
		return []string{`(^|/)claude( |$)`}, []string{a.Path}, func() string {
			env, _ := edit.GetJSON(a.Path, "env")
			model, _ := edit.GetJSON(a.Path, "model")
			return env + "\n" + model
		}
	}
	return nil, nil, nil
}

// runningProc is a process found for an agent: how long it has run, and
// its command line.
type runningProc struct {
	up  time.Duration
	cmd string
}

// running lists the processes a pgrep -f pattern matches. A var so tests
// can say.
var running = func(pat string) []runningProc {
	out, _ := proc.Command("pgrep", "-f", pat).Output()
	pids := strings.Fields(string(out))
	if len(pids) == 0 {
		return nil
	}
	out, _ = proc.Command("ps", "-o", "etime=,command=", "-p", strings.Join(pids, ",")).Output()
	var ps []runningProc
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		et, cmd, _ := strings.Cut(strings.TrimSpace(l), " ")
		if up, ok := elapsed(et); ok {
			ps = append(ps, runningProc{up, strings.TrimSpace(cmd)})
		}
	}
	return ps
}

// changedAt is when magpie last changed what the agent reads at start. It
// is kept in the stash with a digest of what that was, so neither the
// agent's own writes to the same files nor magpie starting again move it;
// a change first seen is put at the files' latest write. Zero when the
// files aren't there.
func (a *Agent) changedAt(files []string, reads func() string) time.Time {
	var mod time.Time
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(mod) {
			mod = st.ModTime()
		}
	}
	if mod.IsZero() {
		return mod
	}
	sum := sha256.Sum256([]byte(reads()))
	digest := hex.EncodeToString(sum[:8])
	key := a.ID + ".started"
	if was, at, ok := strings.Cut(stashLoad()[key], " "); ok && was == digest {
		if n, err := strconv.ParseInt(at, 10, 64); err == nil {
			return time.Unix(0, n)
		}
	}
	stash(map[string]string{key: digest + " " + strconv.FormatInt(mod.UnixNano(), 10)})
	return mod
}

// Stale is how many copies of the agent are running that started before
// magpie last changed what it reads at start: they still have the list
// they started with until reopened. Zero where it can't be told (Windows).
func (a *Agent) Stale() int { return len(a.StaleCopies()) }

// StaleCopy is a copy of an agent still running on the list it started
// with: what kind of copy it is, which says how it is reopened, and when
// it started.
type StaleCopy struct {
	// Kind, for Codex: "app" (the ChatGPT or Codex desktop app, its
	// app-server and helpers), "ide" (an editor extension's), "daemon"
	// (the background app-server the CLI leaves running) or "cli"; ""
	// for other agents; "embedded" is another app's own Codex, named by
	// App
	Kind  string    `json:"kind,omitempty"`
	App   string    `json:"app,omitempty"`
	Since time.Time `json:"since"`
}

// StaleCopies are the copies Stale counts. Reopening one kind doesn't end
// another: on macOS the Codex app keeps running, its app-server with it,
// when its window is closed (its window-all-closed doesn't quit a packaged
// app on darwin), an editor's Codex runs until the editor's window is
// reloaded, and the CLI's managed daemon runs on with ppid 1 until it is
// restarted itself. So each is told apart for the row to say which one is
// left. One app's processes (its app-server, its exec-server) are one copy.
// One run from another home's agent dir (an app-server daemon Codex left
// running for a CODEX_HOME of its own, under its packages) reads other
// files, and isn't counted.
func (a *Agent) StaleCopies() []StaleCopy {
	pats, files, reads := a.startsWith()
	if len(pats) == 0 || a.WSL != "" || runtime.GOOS == "windows" {
		return nil
	}
	changed := a.changedAt(files, reads)
	if changed.IsZero() {
		return nil
	}
	dir := filepath.Dir(a.Path)
	others := "/" + filepath.Base(dir) + "/"
	var out []StaleCopy
	apps := map[string]int{}
	for _, pat := range pats {
		for _, p := range running(pat) {
			if strings.Contains(p.cmd, others) && !strings.Contains(p.cmd, dir+"/") {
				continue
			}
			since := time.Now().Add(-p.up)
			if !since.Before(changed.Add(-time.Second)) {
				continue
			}
			c := StaleCopy{Since: since}
			if a.ID == "codex" {
				var app string
				c.Kind, app = codexCopyKind(p.cmd)
				if c.Kind == "embedded" {
					c.App = app
				}
				if app != "" {
					if i, ok := apps[app]; ok {
						if since.Before(out[i].Since) {
							out[i].Since = since
						}
						continue
					}
					apps[app] = len(out)
				}
			}
			out = append(out, c)
		}
	}
	return out
}

// codexCopyKind tells what runs a Codex process from its command line, and
// for one of a desktop app the app's bundle.
func codexCopyKind(cmd string) (kind, app string) {
	exe := cmd
	if i := strings.Index(cmd, " -"); i > 0 {
		exe = cmd[:i]
	}
	switch {
	case slices.ContainsFunc(strings.Fields(cmd), func(f string) bool {
		return f == "--managed-daemon" || strings.HasPrefix(f, "--managed-daemon=")
	}):
		return "daemon", ""
	case strings.Contains(exe, "/extensions/"):
		return "ide", ""
	case strings.Contains(exe, ".app/Contents/"):
		// the outermost bundle: ChatGPT.app's codex runs from a
		// CodexCLI.app inside it
		return "app", exe[:strings.Index(exe, ".app/")+len(".app")]
	case slices.Contains(strings.Fields(cmd), "app-server"):
		// an app that ships a Codex of its own and talks to its
		// app-server (Agents Anywhere's connector, from its folder in
		// Application Support): reopening that app ends it. A codex
		// installed there by a version manager (fnm) and run in a
		// terminal is no app-server, and stays "cli".
		for _, under := range []string{"/Library/Application Support/", "/.local/share/"} {
			if _, rest, ok := strings.Cut(exe, under); ok {
				if name, _, ok := strings.Cut(rest, "/"); ok && name != "" {
					return "embedded", name
				}
			}
		}
	}
	return "cli", ""
}

// elapsed reads ps's etime, [[dd-]hh:]mm:ss.
func elapsed(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, true
}
