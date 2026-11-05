package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
)

// writeProfile writes the account's Claude Code profile (CLAUDE_CONFIG_DIR's
// .claude.json) with folders trusted, as Claude Code records an accepted
// trust question.
func writeProfile(t *testing.T, config string, folders ...string) {
	t.Helper()
	projects := map[string]any{}
	for _, f := range folders {
		projects[claudeTrustKey(f)] = map[string]any{"hasTrustDialogAccepted": true, "allowedTools": []any{}}
	}
	b, _ := json.Marshal(map[string]any{"projects": projects})
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// trustInProfile records the user trusting folder in h's Claude Code.
func trustInProfile(t *testing.T, h *resumeHarness, folder string) {
	t.Helper()
	writeProfile(t, h.config, evalSymlinks(folder))
}

// askingClaude is a tuiHelperHarness hold for a Claude Code that asks
// whether to trust its folder, which only its user answers, unless
// magpie's own folder answered yes for it or its profile says the user
// trusted one; each run's folder goes to cwds.
func askingClaude(cwds string) string {
	return `if [ "$` + claudetui.TrustEnv + `" != 1 ] && ! grep -q hasTrustDialogAccepted "$CLAUDE_CONFIG_DIR/.claude.json" 2>/dev/null; then
  echo "claude-tui: ` + claudetui.Untrusted + `, which only its user answers" >&2
  exit 1
fi
pwd >> '` + cwds + `'`
}

// sleepy_shibe on Discord (Claude Code 2.1.318, its own account): a
// conversation in a folder the user never trusted was run in magpie's
// folder, and its session kept there. Once passedOver's 10 minutes were
// up, its next run went in the caller's folder again: no session of the
// conversation's there, so Claude Code was told the whole conversation,
// asked about the folder, and the run went in magpie's folder started
// anew, told the whole conversation once more, the conversation's branch
// dropped. The folder is known untrusted from the user's own answers now,
// and the conversation goes on from its session in magpie's folder.
//
// When the user's answers can't be read, the run goes in the caller's
// folder as before, and once Claude Code asks, in magpie's folder from the
// conversation's session there: once, not from the transcript again.
func TestClaudeTUIUntrustedFolderKeepsItsSession(t *testing.T) {
	for _, c := range []struct {
		name    string
		profile string
		starts  int
	}{
		{"untrusted", `{"projects":{}}`, 1},
		{"unknown", `{"projects":`, 2},
	} {
		t.Run(c.name, func(t *testing.T) { untrustedKeepsSession(t, c.profile, c.starts) })
	}
}

func untrustedKeepsSession(t *testing.T, profile string, starts int) {
	caller := t.TempDir()
	cwds := filepath.Join(t.TempDir(), "cwds")
	h, started := tuiHelperHarness(t, askingClaude(cwds))
	if err := os.MkdirAll(h.config, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.config, ".claude.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	system := "Primary working directory: " + caller
	h.ask(system, "a1")
	first := h.runs()
	if len(first) != 1 {
		t.Fatalf("runs: %v", first)
	}
	var sid string
	for pid := range first {
		sid = "s" + pid
	}
	// past untrustedFor
	h.s.subscription.untrusted.Range(func(k, _ any) bool {
		h.s.subscription.untrusted.Store(k, time.Now().Add(-untrustedFor-time.Minute))
		return true
	})
	for i := range idleMost {
		h.ask(fmt.Sprintf("rules %d", i), "x")
	}
	eventually(t, "the conversation's run still going", func() bool { return len(h.s.subscription.idle) == idleMost })

	before, was := h.runs(), helperStarts(started)
	h.ask(system, "a1", "a2")
	var resumed []string
	for pid, log := range h.runs() {
		if _, ok := before[pid]; !ok {
			resumed = append(resumed, log)
		}
	}
	if n := helperStarts(started) - was; n != starts {
		t.Fatalf("the turn started the helper %d times, want %d", n, starts)
	}
	if len(resumed) != 1 || !strings.Contains(resumed[0], "--resume "+sid+" ") {
		// a transcript of the conversation is resumed too (replaySession),
		// written anew under an id of its own
		t.Fatalf("the turn did not resume the conversation's session %s:\n%s", sid, strings.Join(resumed, "\n---\n"))
	}
	told := resumed[0][strings.Index(resumed[0], "\ntold "):]
	if !strings.Contains(told, "a2") || strings.Contains(told, "a1") {
		t.Fatalf("the resumed run was told more than the turn:\n%s", told)
	}
	b, _ := os.ReadFile(cwds)
	for _, d := range strings.Fields(string(b)) {
		if evalSymlinks(d) == evalSymlinks(caller) {
			t.Fatalf("a run went in the caller's folder %s, which the user never trusted", caller)
		}
	}
}

// A folder the user trusts in Claude Code after magpie went around it is
// run in from the next run on, not after untrustedFor.
func TestClaudeTUIFolderTrustedLaterRunInAtOnce(t *testing.T) {
	caller := t.TempDir()
	cwds := filepath.Join(t.TempDir(), "cwds")
	h, started := tuiHelperHarness(t, askingClaude(cwds))
	h.ask("Primary working directory: "+caller, "hi")
	trustInProfile(t, h, caller)
	starts := helperStarts(started)
	h.ask("Primary working directory: "+caller+`\nanother conversation`, "hello")
	if n := helperStarts(started) - starts; n != 1 {
		t.Fatalf("the run started the helper %d times, want once", n)
	}
	b, _ := os.ReadFile(cwds)
	ran := strings.Fields(string(b))
	if len(ran) == 0 || evalSymlinks(ran[len(ran)-1]) != evalSymlinks(caller) {
		t.Fatalf("the run after the user trusted %s ran in %q", caller, ran)
	}
}

// A profile that can't be read says nothing about the folder: the run goes
// in the caller's folder as it did before, and passedOver alone decides.
func TestClaudeTrustsUnreadableProfileIsUnknown(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if err := os.WriteFile(filepath.Join(config, ".claude.json"), []byte(`{"projects":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeTrusts(t.TempDir()); err == nil {
		t.Fatal("a half-written profile read as known")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "none"))
	if ok, err := claudeTrusts(t.TempDir()); ok || err != nil {
		t.Fatalf("no profile at all: %v %v, want untrusted", ok, err)
	}
}

// claudeTrustedBy follows Claude Code 2.1.316's own check (UI, zh, jh, Y0/lr):
// the key of the repository's main checkout, or any folder from the run's
// up to the repository's root, never past it.
func TestClaudeTrustedFolders(t *testing.T) {
	top := evalSymlinks(t.TempDir())
	mk := func(p ...string) string {
		d := filepath.Join(append([]string{top}, p...)...)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	write := func(path, s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := mk("repo")
	mk("repo", ".git", "worktrees", "wt")
	sub := mk("repo", "src", "pkg")
	nested := mk("repo", "vendor", "lib")
	mk("repo", "vendor", "lib", ".git")
	wt := mk("wts", "wt")
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	write(filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), "../..\n")
	write(filepath.Join(repo, ".git", "worktrees", "wt", "gitdir"), filepath.Join(wt, ".git")+"\n")
	plain := mk("plain", "a", "b")
	sm := mk("repo", "mods", "m")
	mk("repo", ".git", "modules", "m")
	write(filepath.Join(sm, ".git"), "gitdir: ../../.git/modules/m\n")
	link := filepath.Join(t.TempDir(), "link")
	linked := os.Symlink(repo, link) == nil // Windows wants a privilege for it

	trusted := func(folders ...string) map[string]bool {
		m := map[string]bool{}
		for _, f := range folders {
			m[claudeTrustKey(f)] = true
		}
		return m
	}
	for _, c := range []struct {
		name    string
		trusted []string
		dir     string
		want    bool
	}{
		{"the repository's root", []string{repo}, repo, true},
		{"a folder in the repository", []string{repo}, sub, true},
		{"a folder between the run's and the root", []string{filepath.Join(repo, "src")}, sub, true},
		{"a worktree of a trusted checkout", []string{repo}, wt, true},
		{"the worktree itself", []string{wt}, wt, true},
		{"a folder above the worktree", []string{filepath.Join(top, "wts")}, wt, false},
		{"a repository in a trusted one", []string{repo}, nested, false},
		{"a submodule of a trusted repository", []string{repo}, sm, false},
		{"a folder above the repository", []string{top}, repo, false},
		{"outside a repository, a folder above", []string{filepath.Join(top, "plain")}, plain, true},
		{"outside a repository, nothing trusted", nil, plain, false},
		{"another folder", []string{repo}, filepath.Join(top, "wts"), false},
	} {
		if got := claudeTrustedBy(trusted(c.trusted...), c.dir); got != c.want {
			t.Errorf("%s: claudeTrustedBy = %v, want %v", c.name, got, c.want)
		}
	}

	// a link to the repository is trusted by its real path, as Claude Code
	// sees it from its working directory
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeProfile(t, config, repo)
	if ok, err := claudeTrusts(link); linked && (!ok || err != nil) {
		t.Fatalf("a link to a trusted repository: %v %v", ok, err)
	}
	// keys are NFC: a folder named in NFD is trusted by its NFC key
	nfd := mk("café")
	writeProfile(t, config, filepath.Join(top, "café"))
	if ok, err := claudeTrusts(nfd); !ok || err != nil {
		t.Fatalf("a folder named in NFD: %v %v", ok, err)
	}
}
