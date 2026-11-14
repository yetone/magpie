package library

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// TestBiliFindsClaudeOffPath: magpie found Claude Code in a folder its
// PATH doesn't reach (proc.FindTool's; ~/.local/bin stands for it here),
// and bili's install, which runs `claude mcp add bili` by name, failed with
// "spawnSync claude ENOENT" (fwxkogaia on X). The real biliRun is given a
// bili that runs claude by name, as bili's installer does.
func TestBiliFindsClaudeOffPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are sh scripts")
	}
	run := biliRun // the real one, before the fake takes its place
	h := sandbox(t)
	f := newFakeBili(t, h)
	os.Remove(filepath.Join(h, "bin", "claude"))
	claude := filepath.Join(h, ".local", "bin", "claude")
	testenv.Program(t, claude, "#!/bin/sh\necho \"claude $*\"\n")
	write(t, filepath.Join(h, ".claude", "settings.json"), "{}\n")
	proc.Forget()

	// bili's own folder is another: npm's prefix, not ~/.local
	bili := filepath.Join(h, "npm", "bin", "bili")
	testenv.Program(t, bili, "#!/bin/sh\nexec claude mcp add bili\n")
	var cc *agent.Agent
	for _, a := range biliAgents(nil) {
		if a.ID == "claude" {
			cc = a
		}
	}
	if cc == nil {
		t.Fatal("Claude Code in ~/.local/bin not offered")
	}
	out, err := run(bili, []string{biliTool(cc)}, 30*time.Second, "plugin", "install", "claude")
	if err != nil || out != "claude mcp add bili" {
		t.Fatalf("bili ran claude: %q, %v", out, err)
	}

	// SetBili tells bili's install and remove of Claude Code's program
	if _, err := SetBili("claude", true); err != nil {
		t.Fatal(err)
	}
	if _, err := SetBili("claude", false); err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(f.tools), func(p string) bool { return p != claude })); n != 2 {
		t.Fatalf("install and remove told of %v, want %s twice", f.tools, claude)
	}
}

// TestBiliPATH: bili's folder first, then each tool's folder PATH lacks,
// then PATH as it was.
func TestBiliPATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	bin := filepath.Join("/x", "bili", "bili")
	on := filepath.Join("/usr", "bin")
	got := biliPATH(bin, []string{filepath.Join(on, "opencode"), filepath.Join("/h", ".local", "bin", "claude"), "", filepath.Join("/h", ".local", "bin", "other")}, on+sep+"/bin")
	want := strings.Join([]string{filepath.Join("/x", "bili"), filepath.Join("/h", ".local", "bin"), on, "/bin"}, sep)
	if got != want {
		t.Fatalf("PATH %q, want %q", got, want)
	}
}
