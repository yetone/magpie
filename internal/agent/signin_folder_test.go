package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// jiangshangmuyun on X: Codex, never installed, was on the Agents page
// after a ChatGPT account was added in Providers. With Codex signed in to
// nothing, that sign-in is written to ~/.codex/auth.json for Codex, and
// the folder it made was taken for Codex. A folder holding only the file
// magpie's sign-in writes (Claude Code's .credentials.json off the Mac
// too) is no sign of the agent; anything the agent writes beside it is.
func TestSignInFileAloneIsNoAgent(t *testing.T) {
	for _, tc := range []struct {
		id, dir, file string
		mk            func(string) *Agent
		other         []string
	}{
		{"codex", ".codex", "auth.json", codex, []string{"config.toml", "sessions/", "version.json"}},
		{"claude", ".claude", ".credentials.json", claude, []string{"settings.json", "projects/", "history.jsonl"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			fresh := func() (string, string) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("USERPROFILE", home)
				t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
				t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
				t.Setenv("CODEX_HOME", "")
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				t.Setenv("PATH", t.TempDir())
				dir := filepath.Join(home, tc.dir)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				return home, dir
			}
			home, _ := fresh()
			if tc.mk(home).Detected() {
				t.Fatalf("~/%s holding only magpie's %s taken for the agent", tc.dir, tc.file)
			}
			for _, o := range tc.other {
				home, dir := fresh()
				p := filepath.Join(dir, o)
				var err error
				if o[len(o)-1] == '/' {
					err = os.MkdirAll(p, 0o700)
				} else {
					err = os.WriteFile(p, []byte("{}"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !tc.mk(home).Detected() {
					t.Errorf("~/%s with %s beside %s not taken for the agent", tc.dir, o, tc.file)
				}
			}
		})
	}
}
