package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/sessions"
)

// The Sessions page reads the sessions of Claude Code in a WSL distro magpie
// found it in (lgtm on Discord: magpie can't see the session lists of agents
// inside WSL), at the distro user's home as the agent is set up there, and
// resumes one through wsl.exe in the distro.
func TestWSLSessionsOfTheDistro(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	home := filepath.Join(root, "home", "me")
	id := "33333333-2222-3333-4444-555555555555"
	project := filepath.Join(home, ".claude", "projects", "-home-me-app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"type":"user","message":{"role":"user","content":"Tidy the README"},"uuid":"u1","timestamp":"2026-09-20T10:00:01.000Z","cwd":"/home/me/app","sessionId":"` + id + `"}
{"parentUuid":"u1","message":{"model":"claude-opus-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":10,"output_tokens":5}},"requestId":"req_1","type":"assistant","uuid":"a1","timestamp":"2026-09-20T10:00:05.000Z","cwd":"/home/me/app","sessionId":"` + id + `"}
`
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(FakeWSL(map[string]string{"Ubuntu": "home:/home/me\ndir:.claude\n"}, map[string]string{"Ubuntu": root}))
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	var got *sessions.Session
	for _, s := range sessions.List(0) {
		if s.ID == id {
			got = &s
		}
	}
	if got == nil {
		t.Fatal("the distro's Claude Code session isn't listed")
	}
	if got.WSL != "Ubuntu" || got.Agent != "claude" || got.Cwd != "/home/me/app" || got.Title != "Tidy the README" || got.Output != 5 {
		t.Fatalf("%+v", *got)
	}
	if !strings.HasPrefix(got.Resume, "wsl.exe -d 'Ubuntu' --cd '/home/me/app' ") || !strings.Contains(got.Resume, "''claude --resume "+id+"''") {
		t.Fatalf("resume %q", got.Resume)
	}
	noOwnClaude(t)
}
