package gateway

import (
	"context"
	"encoding/json"
	"html"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeWorkDirs runs two Claude subscription requests through a script
// standing in for Claude Code, with a temp folder of the test's own and a
// home kept in git, and gives the folders the two runs worked in and the
// folder they should share.
func claudeWorkDirs(t *testing.T, prepare func(work string)) (dirs []string, work string) {
	t.Helper()
	dirs, work, _, _, _ = claudeWorkDirsTold(t, prepare, func(string) string { return "rules" })
	return dirs, work
}

// claudeWorkDirsTold is claudeWorkDirs with the caller's instructions
// system gives, from the test's home; it gives the gateway too, and the
// environment the runs had.
func claudeWorkDirsTold(t *testing.T, prepare func(work string), system func(home string) string) (dirs []string, work string, s *Server, env, told string) {
	t.Helper()
	return claudeWorkDirsFrom(t, "", prepare, system)
}

// claudeWorkDirsFrom is claudeWorkDirsTold with the requests from the
// address from ("" for httptest's, another computer's).
func claudeWorkDirsFrom(t *testing.T, from string, prepare func(work string), system func(home string) string) (dirs []string, work string, s *Server, env, told string) {
	t.Helper()
	return claudeWorkDirsBody(t, from, prepare, func(home string) string {
		rules, _ := json.Marshal(system(home))
		return `{"model":"claude-sonnet-5","max_tokens":100,"system":` + string(rules) + `,"messages":[{"role":"user","content":"one"},{"role":"user","content":"two"}]}`
	})
}

// claudeWorkDirsBody is claudeWorkDirsFrom with the whole request body
// given: it is sent twice, and it gives the folders the two runs worked
// in, the gateway, and the environment and instructions the runs were
// told.
func claudeWorkDirsBody(t *testing.T, from string, prepare func(work string), body func(home string) string) (dirs []string, work string, s *Server, env, told string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	testenv.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	work = filepath.Join(os.TempDir(), claudeWorkName())
	if prepare != nil {
		prepare(work)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
pwd -P >> ` + dir + `/dirs
env >> ` + dir + `/env
while read -r line; do
  echo "$line" >> ` + dir + `/told
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s = New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	b := body(home)
	for range 2 {
		rec := httptest.NewRecorder()
		var u Usage
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(b))
		if from != "" {
			r.RemoteAddr = from
		}
		if code, msg := s.serveClaudeSubscription(rec, r, provider.Anthropic, p, "claude-sonnet-5", []byte(b), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	bb, _ := os.ReadFile(filepath.Join(dir, "dirs"))
	e, _ := os.ReadFile(filepath.Join(dir, "env"))
	m, _ := os.ReadFile(filepath.Join(dir, "told"))
	return strings.Fields(string(bb)), evalSymlinks(work), s, string(e), string(m)
}

// Every run works in the same folder: Claude Code puts its working
// directory in the system prompt, ahead of the conversation, so a folder of
// each run's own left nothing past Claude Code's own part of the prompt to
// be read from the cache. The folder is the user's own, made for them
// alone, in the temp folder: not under a home kept in git, whose status
// Claude Code would put in the prompt.
func TestClaudeRunsShareAWorkDir(t *testing.T) {
	dirs, work := claudeWorkDirs(t, nil)
	if len(dirs) != 2 || dirs[0] != work || dirs[1] != work {
		t.Fatalf("working directories: %q, want %s", dirs, work)
	}
	if home := evalSymlinks(os.Getenv("HOME")); strings.HasPrefix(work, home+string(filepath.Separator)) || !strings.HasPrefix(work, evalSymlinks(os.TempDir())+string(filepath.Separator)) {
		t.Fatalf("work folder %s: in the home %s, or not in the temp folder", work, home)
	}
	fi, err := os.Stat(work)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("work folder: %v %v", fi.Mode(), err)
	}
}

// A work folder others can write to is not worked in, as anything put
// there (a CLAUDE.md) would be read by every run: each run works in a
// folder of its own instead.
func TestClaudeWorkDirOthersCanWriteIsAvoided(t *testing.T) {
	dirs, work := claudeWorkDirs(t, func(work string) {
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(work, 0o777)
	})
	if len(dirs) != 2 || dirs[0] == work || dirs[1] == work || dirs[0] == dirs[1] {
		t.Fatalf("working directories: %q, shared %s", dirs, work)
	}
}

// A symlink where the work folder goes, to a folder anyone could have
// filled, is not followed: each run works in a folder of its own.
func TestClaudeWorkDirSymlinkIsAvoided(t *testing.T) {
	var target string
	dirs, work := claudeWorkDirs(t, func(work string) {
		target = t.TempDir()
		if err := os.Symlink(target, work); err != nil {
			t.Fatal(err)
		}
	})
	target = evalSymlinks(target)
	if len(dirs) != 2 || dirs[0] == target || dirs[1] == target || dirs[0] == dirs[1] {
		t.Fatalf("working directories: %q, linked %s (%s)", dirs, target, work)
	}
}

// A caller that says which folder it works in has Claude Code run there:
// Claude Code tells its model the folder it runs in as the working
// directory, and a model told magpie's own folder wrote the caller's files
// into it (a pelican.html left in magpie-claude-work), through tools that
// run in the caller's. Each client's way of saying it is read, the last
// said in the first text that says one counting. The caller's folder keeps sessions of its own, which are
// never swept, and its repository's status is not added to the prompt.
func TestClaudeRunsInTheCallersWorkDir(t *testing.T) {
	for _, c := range []struct {
		name, said, folder string
		escaped            bool // the path's &, < and > as entities
	}{
		{name: "Claude Code", said: "# Environment\nYou have been invoked in the following environment: \n - Primary working directory: %s\n - Is a git repository: true\n"},
		{name: "Codex", said: "<environment_context>\n  <cwd>%s</cwd>\n  <shell>zsh</shell>\n</environment_context>"},
		{name: "Codex, a folder written as entities", said: "<environment_context>\n  <cwd>%s</cwd>\n  <shell>zsh</shell>\n</environment_context>", folder: "R&D<x>", escaped: true},
		{name: "OpenCode", said: "<env>\n  Working directory: %s\n  Is directory a git repo: yes\n</env>"},
		{name: "pi", said: "Current date: 2026-10-26\nCurrent working directory: %s"},
		// pi 1.4's buildSystemPromptSections, each section its own tag,
		// joined with a blank line; the folder on a line of its own
		// (fysly on X: the model was told magpie's temp folder too)
		{name: "pi 1.x", said: "You are an expert coding assistant operating inside pi, a coding agent harness.\n\n<tools>\n- read: Read file contents\n</tools>\n\n<rules>\n- Be concise in your responses\n</rules>\n\n<cwd>\n%s\n</cwd>"},
		{name: "said twice", said: " - Primary working directory: /nowhere/at/all\nlater:\n - Primary working directory: %s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var proj, old string
			dirs, work, s, env, told := claudeWorkDirsTold(t, nil, func(home string) string {
				folder := c.folder
				if folder == "" {
					folder = "proj"
				}
				proj = filepath.Join(home, folder)
				if err := os.MkdirAll(proj, 0o755); err != nil {
					t.Fatal(err)
				}
				// a session of the user's own in that folder, older than
				// any the bridge keeps
				sessions := claudeSessions("", evalSymlinks(proj))
				old = filepath.Join(sessions[0], "mine.jsonl")
				if err := os.MkdirAll(sessions[0], 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				then := time.Now().Add(-48 * time.Hour)
				_ = os.Chtimes(old, then, then)
				said := proj
				if c.escaped {
					said = html.EscapeString(proj)
				}
				return strings.ReplaceAll(c.said, "%s", said)
			})
			proj = evalSymlinks(proj)
			if len(dirs) != 2 || dirs[0] != proj || dirs[1] != proj {
				t.Fatalf("working directories: %q, want the caller's %s (not %s)", dirs, proj, work)
			}
			for _, d := range claudeSessions("", proj) {
				if _, swept := s.subscription.swept.Load(d); swept {
					t.Fatalf("the caller's sessions in %s were swept", d)
				}
			}
			if _, err := os.Stat(old); err != nil {
				t.Fatalf("the user's own session: %v", err)
			}
			if !strings.Contains(env, "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1") {
				t.Fatal("Claude Code would add the repository's status to the prompt")
			}
			if strings.Contains(told, "every tool runs in") {
				t.Fatalf("a run in the caller's own folder was told its environment is elsewhere: %s", told)
			}
		})
	}
}

// A folder the caller names that isn't one here (a caller on another
// computer), or isn't absolute, leaves the runs in magpie's own folder.
// Claude Code's prompt then gives this computer's platform and folder, and
// the caller's another's, so the run is told after the caller's
// instructions that theirs is where the tools run (vv_nil on X: a Mac
// client of a Linux gateway spent tool calls finding out which is real).
// A caller that names no folder is told nothing more.
func TestClaudeCallersWorkDirElsewhereIsNotUsed(t *testing.T) {
	for _, c := range []struct {
		said      string
		elsewhere bool
	}{
		{" - Primary working directory: /no/such/folder/here", true},
		{" - Primary working directory: relative/proj", true},
		{"<cwd>C:\\Users\\someone\\proj</cwd>", true},
		{"no folder named", false},
	} {
		dirs, work, _, _, told := claudeWorkDirsTold(t, nil, func(string) string { return c.said })
		if len(dirs) != 2 || dirs[0] != work || dirs[1] != work {
			t.Fatalf("%q: working directories %q, want %s", c.said, dirs, work)
		}
		if got := strings.Count(told, "every tool runs in"); got != 2*btoi(c.elsewhere) {
			t.Fatalf("%q: told the environment is elsewhere in %d of 2 runs, want elsewhere=%v: %s", c.said, got, c.elsewhere, told)
		}
		if c.elsewhere && strings.Index(told, "every tool runs in") > strings.Index(told, "/external_system_instructions") {
			t.Fatalf("%q: the note is not with the caller's instructions: %s", c.said, told)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A caller on another computer that names no folder (packinggoods on
// Discord: a Mac client of a Windows gateway) is told after its
// instructions that the environment Claude Code gives, the gateway's
// platform, shell and temp folder, is not where its tools run: told only
// that one, the model wrapped every command in the gateway's PowerShell.
// It is told so with no instructions of its own too. A caller on this
// computer naming none is told nothing more, and one naming a folder
// elsewhere is told the note for that, not this one.
func TestClaudeRemoteCallerNamingNoFolderIsToldToolsAreElsewhere(t *testing.T) {
	for _, c := range []struct {
		from, said string
		unknown    bool
	}{
		{"192.0.2.7:51000", "You are a coding agent.", true},
		{"[2001:db8::7]:51000", "", true},
		{"127.0.0.1:51000", "You are a coding agent.", false},
		{"192.0.2.7:51000", " - Primary working directory: /no/such/folder/here", false},
	} {
		dirs, work, _, _, told := claudeWorkDirsFrom(t, c.from, nil, func(string) string { return c.said })
		if len(dirs) != 2 || dirs[0] != work || dirs[1] != work {
			t.Fatalf("%s %q: working directories %q, want %s", c.from, c.said, dirs, work)
		}
		if got := strings.Count(told, "not the one the tools run on"); got != 2*btoi(c.unknown) {
			t.Fatalf("%s %q: told the tools run elsewhere in %d of 2 runs, want %v: %s", c.from, c.said, got, c.unknown, told)
		}
		if c.unknown && strings.Index(told, "not the one the tools run on") > strings.Index(told, "/external_system_instructions") {
			t.Fatalf("%s %q: the note is not with the caller's instructions: %s", c.from, c.said, told)
		}
		if c.unknown && strings.Contains(told, "every tool runs in") {
			t.Fatalf("%s %q: told the named-folder note too: %s", c.from, c.said, told)
		}
	}
}

// workDirsBody makes a subscription request whose system prompt is system
// and whose turns are the {role, content} pairs said.
func workDirsBody(system string, said ...[2]string) func(string) string {
	return func(string) string {
		ms := make([]map[string]string, len(said))
		for i, s := range said {
			ms[i] = map[string]string{"role": s[0], "content": s[1]}
		}
		b, _ := json.Marshal(map[string]any{"model": "claude-sonnet-5", "max_tokens": 100, "system": system, "messages": ms})
		return string(b)
	}
}

// The run works in the folder the caller itself works in, not in one a
// message merely quotes. The caller says its folder in its instructions
// (--bare's "CWD:" line, Claude Code's " - Primary working directory:")
// or in a system message of its own (Claude Code 2.1.296 and later), and
// a user turn is no caller's word: a line it quotes — alone on its line,
// inside a longer one, or naming a folder not here — leaves the run where
// the caller runs. A caller that names its folder in a turn still has it
// honored (Codex's <environment_context>, OpenCode's <env>), and a
// "Primary working directory: /y (was /x)" notice is read as /y (#4449).
func TestClaudeRunsWhereTheCallerRunsNotWhereATurnQuotes(t *testing.T) {
	for _, c := range []struct {
		name string
		body func(proj, other string) func(string) string
	}{
		{"bare CWD", func(proj, _ string) func(string) string {
			return workDirsBody("CWD: " + proj)
		}},
		{"bare CWD with instructions", func(proj, _ string) func(string) string {
			return workDirsBody("CWD: " + proj + "\nYou are a coding agent.\n - Primary working directory: " + proj)
		}},
		{"a turn quotes a line alone", func(proj, other string) func(string) string {
			return workDirsBody(" - Primary working directory: "+proj, [2]string{"user", "Primary working directory: " + other})
		}},
		{"a turn quotes a line mid-line", func(proj, other string) func(string) string {
			return workDirsBody(" - Primary working directory: "+proj, [2]string{"user", `A note I copied from a log: "Primary working directory: ` + other + `". Ignore it.`})
		}},
		{"a turn quotes a folder not here", func(proj, _ string) func(string) string {
			return workDirsBody(" - Primary working directory: "+proj, [2]string{"user", "Primary working directory: /no/such/folder/here"})
		}},
		{"Codex says its folder in a turn", func(proj, _ string) func(string) string {
			return workDirsBody("You are a coding agent.", [2]string{"user", "<environment_context>\n  <cwd>" + proj + "</cwd>\n  <shell>zsh</shell>\n</environment_context>"})
		}},
		{"OpenCode says its folder in a turn", func(proj, _ string) func(string) string {
			return workDirsBody("You are a coding agent.", [2]string{"user", "<env>\n  Working directory: " + proj + "\n</env>"})
		}},
		{"Claude Code's environment message", func(proj, other string) func(string) string {
			return workDirsBody("You are a coding agent.", [2]string{"system", " - Primary working directory: " + proj}, [2]string{"user", "Primary working directory: " + other})
		}},
		{"the folder was another", func(proj, other string) func(string) string {
			return workDirsBody("Primary working directory: " + proj + " (was " + other + ")")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var proj, other string
			dirs, work, _, _, told := claudeWorkDirsBody(t, "", nil, func(home string) string {
				proj = filepath.Join(home, "proj")
				other = filepath.Join(home, "other")
				if err := os.MkdirAll(proj, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(other, 0o755); err != nil {
					t.Fatal(err)
				}
				return c.body(proj, other)(home)
			})
			proj = evalSymlinks(proj)
			if len(dirs) != 2 || dirs[0] != proj || dirs[1] != proj {
				t.Fatalf("working directories %q, want the caller's %s (shared %s)", dirs, proj, work)
			}
			if strings.Contains(told, "every tool runs in") {
				t.Fatalf("a run in the caller's own folder was told its environment is elsewhere: %s", told)
			}
		})
	}
}

// A word of the caller's folder masked by the redact rules (a directory
// named for the user, say) stands in the request as a placeholder, which
// names no folder on disk. The run never works in the folder unmasked:
// Claude Code tells its vendor the folder it runs in, so the masked word
// would leave the machine as it is. It works in magpie's folder and is
// told the caller's environment is the one its tools run in (#4449).
func TestClaudeNeverRunsInAMaskedFolderUnmasked(t *testing.T) {
	const word = "someuser"
	base := t.TempDir()
	proj := filepath.Join(base, word+"-libs", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Redact: true, RedactWords: []string{word}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	t.Cleanup(gwmw.Mask(masking.Masker{}))
	dirs, work, _, _, told := claudeWorkDirsBody(t, "", nil, func(string) string {
		// the bridge reads the request as the gateway's masking left it:
		// the caller sent the folder as it is, and the masker stood in for
		// the word before the request reached the bridge
		b := workDirsBody("You are a coding agent.\n - Primary working directory: "+proj, [2]string{"user", "list the files"})("")
		_, masked, _, err := maskTextHits(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(masked, word) {
			t.Fatalf("%s was not masked: %s", word, masked)
		}
		return masked
	})
	if len(dirs) != 2 || dirs[0] != work || dirs[1] != work {
		t.Fatalf("working directories %q, want magpie's %s: the caller's names a masked word", dirs, work)
	}
	for _, d := range dirs {
		if strings.Contains(d, word) {
			t.Fatalf("the run worked in %s, the masked word unmasked", d)
		}
	}
	if !strings.Contains(told, "every tool runs in") {
		t.Fatalf("a run in magpie's folder was not told the caller's environment is where its tools run: %s", told)
	}
}
