package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/testenv"
)

// A one-off interactive run that fails says why in app.log: the reason
// the helper gave and the end of Claude Code's last screen, which the
// card clips to its start. mydosu (#4830) found nothing there to say why
// every read of a saved account failed. Secrets and the user's folders
// don't go in.
func TestClaudeOneOffFailureLogged(t *testing.T) {
	for _, c := range []struct {
		name, what string
		run        func(ctx context.Context, cfg string) error
	}{
		{"usage", "a /usage read", func(ctx context.Context, cfg string) error { _, err := claudeUsage(ctx, cfg); return err }},
		{"test", "a test on haiku", func(ctx context.Context, cfg string) error { return askClaude(ctx, cfg, "haiku") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeWarmClaude(t, `{}`, 0)
			claudeTUIRuns = true
			t.Cleanup(func() { claudeTUIRuns = false })
			t.Setenv("TMPDIR", t.TempDir())
			cfg := savedClaudeDir(t)
			home, _ := os.UserHomeDir()
			token := "sk-ant-oat01-" + strings.Repeat("Q7x", 20)
			// the helper's reason, as it says it at its caller's deadline,
			// with a long last screen that ends on what Claude Code drew last
			screen := "Do you trust this folder? " + strings.Repeat("─", 400) + " C:\\Users\\mydosu\\AppData\\Local\\Temp\\magpie-claude-work token " + token + " ❯ No, exit END-OF-SCREEN"
			reason := "claude-tui: Claude Code asked whether to trust " + filepath.Join(home, "AppData", "magpie-claude-work") + ", not answered in the 16.8s its caller waits for it: " + screen
			helper := filepath.Join(t.TempDir(), "magpie")
			testenv.Program(t, helper, `#!/bin/sh
[ "$1" = "`+claudetui.Command+`" ] || exit 9
printf '%s\n' '`+reason+`' >&2
exit 1
`)
			claudeTUIHelper = helper
			t.Cleanup(func() { claudeTUIHelper = "" })
			logs := keepLog(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := c.run(ctx, cfg); err == nil {
				t.Fatal("the run didn't fail")
			}
			var line string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, "claude: "+c.what+" of saved account 0123456789abcdef failed: ") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("app.log says nothing of the failure:\n%s", logs.String())
			}
			for _, want := range []string{"asked whether to trust ~", "not answered in the 16.8s", "END-OF-SCREEN"} {
				if !strings.Contains(line, want) {
					t.Errorf("the line lacks %q: %s", want, line)
				}
			}
			for _, gone := range []string{home, "mydosu", strings.Repeat("Q7x", 20)} {
				if strings.Contains(line, gone) {
					t.Errorf("the line has %q: %s", gone, line)
				}
			}
			if len(line) > 1200 {
				t.Errorf("the line is %d bytes: %s", len(line), line)
			}
		})
	}
}
