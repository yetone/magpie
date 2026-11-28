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
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// What the Agents page says beside an agent's 「接入」 switch (the owner's
// design): before it is on, what the agent runs on now — its own
// subscription, sign-in or providers — so the user can tell what turning
// it on touches; once on, whether copies of it already running still have
// the list they started with.

// Source is what an agent not connected to magpie runs on now: "sub" (a
// Claude subscription), "chatgpt" (a ChatGPT sign-in), "key" (an API key
// of its own), "providers:N" (N providers of its own); "" when unknown.
//
// With account discovery off (#1535) no sign-in is read for it: Claude
// Code's account in .claude.json, Codex's auth.json and OpenCode's are
// left alone, and only what the agent's own settings say is told.
func (a *Agent) Source() string {
	if a.WSL != "" || a.Path == "" {
		return ""
	}
	dir := filepath.Dir(a.Path)
	signIns := !settings.AccountDiscoveryOff()
	switch a.ID {
	case "claude":
		// signed in to claude.ai: ~/.claude.json (or the one in
		// CLAUDE_CONFIG_DIR) has the account
		for _, p := range []string{filepath.Join(dir, ".claude.json"), filepath.Join(filepath.Dir(dir), ".claude.json")} {
			if !signIns {
				break
			}
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
		if !signIns {
			break
		}
		if codexChatGPT(dir) {
			return "chatgpt"
		}
		var k struct {
			Key string `json:"OPENAI_API_KEY"`
		}
		if b, ok := provider.CodexSignIn(dir); ok && json.Unmarshal(b, &k) == nil && k.Key != "" {
			return "key"
		}
	case "opencode", "mimocode", "deveco":
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
		if b, err := readOwnAuth(filepath.Join(home, ".local", "share", a.ID, "auth.json")); err == nil && json.Unmarshal(b, &auth) == nil {
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

// FailoverSaid is, for an agent not connected whose requests still go
// through magpie for account failover alone (FailingOver), what the CLI
// and the TUI say of it, the Agents page's line in words of their own:
// why it goes through magpie, and what turns that off (#1385). "" when it
// doesn't.
func (a *Agent) FailoverSaid() string {
	if a.FailingOver == nil || !a.FailingOver() {
		return ""
	}
	return "not connected · goes through magpie only to fail over to your other ChatGPT accounts; tick Only switch Codex's sign-in under Providers › ChatGPT (magpie accounts switch-only codex on), or switch them off, to stop it"
}

// startsWith is, for an agent that reads magpie's models only as it
// starts, how to find its processes (patterns for pgrep -f), the files it
// reads them from, and the part of those files magpie writes for it. The
// files' times alone don't say when magpie changed that part: the agent
// writes the same files (Codex keeps each project's trust, a notice seen,
// the model picked in its app in config.toml), and a copy started before
// such a write has magpie's list all the same (#729).
//
// Of Codex's model lists only which models they hold counts, not every
// byte of each entry: the Codex app's list (deskList) carries the ChatGPT
// backend's entries as the backend sends them, asked for again each hour,
// and every entry's details follow magpie's version and models.dev. A
// copy running from before such a rewrite has the same models to pick
// and works on, yet the row told the user to quit and reopen the Codex
// app several times a day (Qyburn3 on X).
func (a *Agent) startsWith() (pats []string, files []string, reads func() string) {
	switch a.ID {
	case "codex":
		list, desk := codexLists(a.Path)
		return []string{`(^|/)codex( |$)`}, []string{a.Path, list, desk}, func() string {
			var b strings.Builder
			codexWiring(&b, a.Path)
			b.WriteString(listedModels(list))
			b.WriteString(listedModels(desk))
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

// codexLists are the model lists magpie writes beside Codex's config at
// path: its catalog as Codex's provider, and the Codex app's list beside
// the sign-in (deskList).
func codexLists(path string) (list, desk string) {
	dir := filepath.Dir(path)
	return filepath.Join(dir, "magpie-models.json"), filepath.Join(dir, "magpie-desktop-models.json")
}

// codexWiring writes where Codex's config sends it, as magpie writes it:
// its provider, its catalog's name, its base URL and magpie's table.
func codexWiring(b *strings.Builder, path string) {
	for _, k := range []string{"model_provider", "model_catalog_json", "openai_base_url"} {
		v, _ := edit.GetTOMLTop(path, k)
		b.WriteString(k + "=" + v + "\n")
	}
	t, _ := edit.GetTOMLTable(path, "model_providers."+magpieID)
	j, _ := json.Marshal(t)
	b.Write(j)
}

// listedModels is which models the Codex model list at path holds, each
// slug with its visibility, in no order: what a copy started on it can
// pick. A list that doesn't parse is taken whole, its every byte; none
// there is "".
func listedModels(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var l struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &l) != nil {
		return "\n" + string(raw)
	}
	ms := make([]string, 0, len(l.Models))
	for _, m := range l.Models {
		ms = append(ms, m.Slug+" "+m.Visibility)
	}
	slices.Sort(ms)
	return "\n" + filepath.Base(path) + ":" + strings.Join(ms, ",")
}

// readsBefore is what an older magpie read of the files startsWith names,
// for a digest it stashed to be taken as the one of now when nothing
// changed since: Codex's lists byte for byte. Without it every Codex
// started before the update was told to reopen once more. nil for an
// agent whose part read the same then.
func (a *Agent) readsBefore() func() string {
	if a.ID != "codex" {
		return nil
	}
	list, desk := codexLists(a.Path)
	return func() string {
		var b strings.Builder
		codexWiring(&b, a.Path)
		c, _ := os.ReadFile(list)
		b.Write(c)
		d, _ := os.ReadFile(desk)
		b.Write(d)
		return b.String()
	}
}

// runningProc is a process found for an agent: how long it has run, and
// its command line.
type runningProc struct {
	up  time.Duration
	cmd string
}

// running lists the processes a pgrep -f pattern matches. A var so tests
// can say.
var running = listRunning

// changedAt is when magpie last changed what the agent reads at start. It
// is kept in the stash with a digest of what that was, so neither the
// agent's own writes to the same files nor magpie starting again move it;
// a change first seen is put at the files' latest write, or at now when
// no file was written since the last one. Zero when the files aren't
// there.
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
	was, at, ok := strings.Cut(stashShared()[key], " ")
	n, err := strconv.ParseInt(at, 10, 64)
	if ok && err == nil && was == digest {
		return time.Unix(0, n)
	}
	// stashed by an older magpie, over the same files as they are now
	if ok && err == nil && a.readsBefore() != nil {
		old := sha256.Sum256([]byte(a.readsBefore()()))
		if was == hex.EncodeToString(old[:8]) {
			stash(map[string]string{key: digest + " " + at})
			return time.Unix(0, n)
		}
	}
	// a change no file's time shows (Claude Desktop's models are the
	// gateway's, not a file's) is put at when it is first seen
	if ok && err == nil && !mod.After(time.Unix(0, n)) {
		mod = time.Now()
	}
	stash(map[string]string{key: digest + " " + strconv.FormatInt(mod.UnixNano(), 10)})
	return mod
}

// Stale is how many copies of the agent are running that started before
// magpie last changed what it reads at start: they still have the list
// they started with until reopened. On Windows only the Codex daemon is
// told.
func (a *Agent) Stale() int { return len(a.StaleCopies()) }

// StaleCopy is a copy of an agent still running on the list it started
// with: what kind of copy it is, which says how it is reopened, and when
// it started.
type StaleCopy struct {
	// Kind, for Codex: "app" (the ChatGPT or Codex desktop app, its
	// app-server and helpers), "ide" (an editor extension's), "daemon"
	// (the background app-server the CLI leaves running), "helper" (a
	// subcommand the daemon spawns beside itself, no copy to reopen) or
	// "cli"; "" for other agents; "embedded" is another app's own Codex,
	// named by App
	Kind string `json:"kind,omitempty"`
	App  string `json:"app,omitempty"`
	// Bundle is the desktop app's own, for "app": what the row's Restart
	// quits and opens again
	Bundle string    `json:"bundle,omitempty"`
	Since  time.Time `json:"since"`
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
	// Windows lists its processes slowly and without their start: only
	// the Codex daemon, as KeepCodexDaemonCurrent last found it
	if a.ID == "codex" && a.WSL == "" && runtime.GOOS == "windows" {
		return codexDaemonBehind()
	}
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
		for _, p := range runningLately(pat) {
			if strings.Contains(p.cmd, others) && !strings.Contains(p.cmd, dir+"/") {
				continue
			}
			since := time.Now().Add(-p.up)
			if !since.Before(changed.Add(-time.Second)) {
				continue
			}
			// to the second: since is now less how long it has run, two
			// clock reads apart, and a time that moved by microseconds
			// each poll rewrote the last-known state each time
			since = since.Truncate(time.Second)
			c := StaleCopy{Since: since}
			if a.ID == "codex" {
				var app string
				c.Kind, app = codexCopyKind(p.cmd)
				if c.Kind == "helper" {
					// a daemon subcommand's helper reads no list, a
					// restart of the daemon doesn't end it, and there
					// is no Codex to quit (#1461)
					continue
				}
				switch c.Kind {
				case "embedded":
					c.App = app
				case "app":
					c.Bundle = app
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
	case daemonHelper(cmd):
		// a subcommand the managed daemon spawns beside itself (codex
		// app-server daemon pid-update-loop): long-lived, reads no list,
		// and outlives a daemon restart (#1461)
		return "helper", ""
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

// daemonHelper says the command line is a subcommand the managed daemon
// spawns beside itself: app-server daemon <sub> (pid-update-loop, …). The
// daemon itself carries --managed-daemon and is told apart above.
func daemonHelper(cmd string) bool {
	f := strings.Fields(cmd)
	for i, s := range f {
		if s == "app-server" && i+2 < len(f) && f[i+1] == "daemon" {
			return true
		}
	}
	return false
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
