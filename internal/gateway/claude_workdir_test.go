package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// claudeWorkDirs runs two Claude subscription requests through a script
// standing in for Claude Code, with a temp folder of the test's own and a
// home kept in git, and gives the folders the two runs worked in and the
// folder they should share.
func claudeWorkDirs(t *testing.T, prepare func(work string)) (dirs []string, work string) {
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
while read -r line; do
  echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
  echo '{"type":"stream_event","event":{"type":"message_stop"}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := New()
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, said := range []string{"one", "two"} {
		body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"rules","messages":[{"role":"user","content":"` + said + `"}]}`
		rec := httptest.NewRecorder()
		var u Usage
		if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
			t.Fatalf("%d %s", code, msg)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "dirs"))
	return strings.Fields(string(b)), evalSymlinks(work)
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
