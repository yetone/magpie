package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// copyTree copies the fixtures somewhere the test may change them.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (claude, codex string) {
	dir := t.TempDir()
	claude, codex = filepath.Join(dir, "claude"), filepath.Join(dir, "codex")
	copyTree(t, "testdata/claude", claude)
	copyTree(t, "testdata/codex", codex)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	// OpenCode and Pi keep nothing here unless a test puts it there
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(dir, "pi"))
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	// ZCode, dsh, Cline, Qoder, Grok Build, WorkBuddy and omp keep theirs in
	// the home folder: never the real one's
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))
	for _, env := range []string{"DSH_HOME", "CLINE_DIR", "CLINE_DATA_DIR", "CLINE_SESSION_DATA_DIR", "QODER_CONFIG_DIR", "QODERCN_CONFIG_DIR",
		"GROK_HOME", "WORKBUDDY_CONFIG_DIR"} {
		t.Setenv(env, "")
	}
	PriceOf = func(_ settings.Settings, m string) (catalog.Price, bool) {
		switch m {
		case "claude-opus-5-5":
			return catalog.Price{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}, true
		case "gpt-6-astra":
			return catalog.Price{Input: 10, Output: 50, CacheRead: 1}, true
		}
		return catalog.Price{}, false
	}
	t.Cleanup(func() { PriceOf = priceOf; Reset() })
	Reset()
	return
}

func model(s Session, name string) Model {
	for _, m := range s.Models {
		if m.Model == name {
			return m
		}
	}
	return Model{}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestList(t *testing.T) {
	setup(t)
	ss := List(0)
	if len(ss) != 2 {
		t.Fatalf("want 2 sessions (the empty one left out), got %d: %+v", len(ss), ss)
	}
	cx, cc := ss[0], ss[1]
	if cx.Agent != "codex" || cc.Agent != "claude" {
		t.Fatalf("order by last activity: %s, %s", cx.Agent, cc.Agent)
	}

	// Claude Code: a message's blocks counted once, the subagent's calls in
	if cc.ID != "11111111-2222-3333-4444-555555555555" || cc.Cwd != "/work/app" || cc.Title != "Fix the login bug in auth.go" {
		t.Fatalf("claude: %+v", cc)
	}
	if o := model(cc, "claude-opus-5-5"); o.Tokens != (Tokens{100, 50, 5000, 1000}) || !o.Priced {
		t.Fatalf("opus: %+v", o)
	}
	if h := model(cc, "claude-haiku-4-5-20251001"); h.Tokens != (Tokens{1010, 120, 200, 0}) || h.Priced {
		t.Fatalf("haiku: %+v", h)
	}
	if want := (100*4 + 50*20 + 5000*0.2 + 1000*5) / 1e6; !near(cc.Cost, want) || cc.Unpriced != 1 {
		t.Fatalf("claude cost %v unpriced %d, want %v 1", cc.Cost, cc.Unpriced, want)
	}
	if !cc.Start.Equal(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)) || !cc.Last.Equal(time.Date(2026, 9, 20, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("claude times %s %s", cc.Start, cc.Last)
	}

	// Codex: two files of one thread, the repeated count adding nothing,
	// the cache taken out of the input
	if cx.ID != "01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd" || cx.Cwd != "/work/it's" || cx.Title != "Add a README" {
		t.Fatalf("codex: %+v", cx)
	}
	if a := model(cx, "gpt-6-astra"); a.Tokens != (Tokens{8000, 400, 10000, 0}) {
		t.Fatalf("astra: %+v", a)
	}
	if l := model(cx, "gpt-6-luna"); l.Tokens != (Tokens{2000, 200, 10000, 0}) {
		t.Fatalf("luna: %+v", l)
	}
	if cx.Input != 10000 || cx.Output != 600 || cx.CacheRead != 20000 || cx.Unpriced != 1 {
		t.Fatalf("codex totals: %+v", cx)
	}
	if !cx.Last.Equal(time.Date(2026, 9, 23, 0, 27, 20, 0, time.UTC)) {
		t.Fatalf("codex last %s", cx.Last)
	}
	if runtime.GOOS != "windows" {
		if want := `cd '/work/it'\''s' && codex resume 01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd`; cx.Resume != want {
			t.Fatalf("resume %q", cx.Resume)
		}
		if want := "cd '/work/app' && claude --resume 11111111-2222-3333-4444-555555555555"; cc.Resume != want {
			t.Fatalf("resume %q", cc.Resume)
		}
	}
	if got := List(1); len(got) != 1 {
		t.Fatalf("limit: %d", len(got))
	}
}

func TestIncremental(t *testing.T) {
	claude, _ := setup(t)
	path := filepath.Join(claude, "projects", "-work-app", "11111111-2222-3333-4444-555555555555.jsonl")
	before := model(List(0)[1], "claude-opus-5-5")

	line := `{"parentUuid":"a3","isSidechain":false,"message":{"model":"claude-opus-5-5","id":"msg_3","type":"message","role":"assistant","content":[],"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}},"type":"assistant","uuid":"a4","timestamp":"2026-09-20T11:00:00.000Z","sessionId":"11111111-2222-3333-4444-555555555555"}`
	appendTo := func(s string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	// half a line, still being written: left for later
	appendTo(line[:40])
	if got := model(List(0)[1], "claude-opus-5-5"); got.Tokens != before.Tokens {
		t.Fatalf("a partial line was counted: %+v", got)
	}
	off := cache[path].Off
	appendTo(line[40:] + "\n")
	s := List(0)[1]
	if got := model(s, "claude-opus-5-5"); got.Input != before.Input+7 || got.Output != before.Output+3 {
		t.Fatalf("appended line: %+v", got)
	}
	if !s.Last.Equal(time.Date(2026, 9, 20, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("last %s", s.Last)
	}
	if cache[path].Off <= off {
		t.Fatalf("offset did not move on: %d → %d", off, cache[path].Off)
	}

	// kept on disk: a new process reads the parse back, not the file (here
	// changed in place, same size and time, so a re-read would show it)
	saved()
	b, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	var kept cacheFile
	if json.Unmarshal(b, &kept) != nil || kept.Version != cacheVersion || kept.Files[path] == nil {
		t.Fatalf("cache file: %s", b)
	}
	fi, _ := os.Stat(path)
	raw, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(raw), `"input_tokens":7,`, `"input_tokens":8,`, 1)), 0o644)
	os.Chtimes(path, fi.ModTime(), fi.ModTime())
	Reset()
	if got := model(List(0)[1], "claude-opus-5-5"); got.Input != before.Input+7 {
		t.Fatalf("parse not kept: %+v", got)
	}

	// a file that shrank is read again from the start
	os.WriteFile(path, raw[:len(raw)-len(line)-1], 0o644)
	if got := model(List(0)[1], "claude-opus-5-5"); got.Tokens != before.Tokens {
		t.Fatalf("after shrinking: %+v", got)
	}
}

func TestPrompt(t *testing.T) {
	for in, want := range map[string]string{
		`"<command-message>review</command-message>\n<command-name>/review</command-name>\n<command-args>42</command-args>"`: "/review 42",
		`[{"type":"text","text":"look at this"},{"type":"image","source":{}}]`:                                               "look at this",
		`[{"type":"tool_result","tool_use_id":"x","content":"ok"}]`:                                                          "",
		`"<local-command-stdout>hi</local-command-stdout>"`:                                                                  "",
		`"[Request interrupted by user]"`:                                                                                    "",
	} {
		if got := ccPrompt(json.RawMessage(in)); got != want {
			t.Errorf("ccPrompt(%s) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("字", 300)
	if got := []rune(title(long)); len(got) != 161 || got[160] != '…' {
		t.Errorf("title not cut: %d", len(got))
	}
}

func TestResumeCommand(t *testing.T) {
	if ResumeCommand("claude", "x; rm -rf /", "/tmp") != "" {
		t.Fatal("an id that isn't plain must give no command")
	}
	if got := ResumeCommand("codex", "abc", ""); got != "codex resume abc" {
		t.Fatalf("no folder: %q", got)
	}
	if ResumeCommand("goose", "abc", "/tmp") != "" {
		t.Fatal("unknown agent")
	}
}

func TestRolloutName(t *testing.T) {
	for name, id := range map[string]string{
		"rollout-2026-09-20T15-48-28-01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd.jsonl":                                      "01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd",
		"rollout-2026-09-23T00-27-05-01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd_01a0c9f1-3d6f-7f33-b3bd-25c1dd14251b.jsonl": "01a0bdc9-b5fd-7e63-9658-68bc8dce5ecd",
	} {
		if m := rolloutName.FindStringSubmatch(name); m == nil || m[1] != id {
			t.Errorf("%s: %v", name, m)
		}
	}
}
