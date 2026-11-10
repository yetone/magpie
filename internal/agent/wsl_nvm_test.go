package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// A command the WSL probe finds only under nvm is the one under the Node a
// terminal there gets: nvm's default, as nvm itself resolves it, else the
// highest version. The probe's glob took nvm's folders in name order, so
// a codex under v18 was taken for the default v22's, and v9's came last.
func TestWSLProbeNvmDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe runs in a Linux distro")
	}
	for _, c := range []struct {
		name, nvmSh, want string
	}{
		{"no nvm.sh: the highest, not the first by name", "", "v22.12.0"},
		// a stand-in for nvm.sh: the probe asks nvm_version default, which
		// the real one resolves through alias/default
		{"nvm's default", "nvm_version() { [ \"$1\" = default ] && echo v9.11.2; }\n", "v9.11.2"},
		{"nvm's default not installed: the highest", "nvm_version() { echo N/A; }\n", "v22.12.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			sys := filepath.Join(home, "sys") // PATH: nothing of codex's
			os.MkdirAll(sys, 0o755)
			nvm := filepath.Join(home, ".nvm")
			for _, v := range []string{"v18.20.4", "v22.12.0", "v9.11.2"} {
				bin := filepath.Join(nvm, "versions", "node", v, "bin")
				os.MkdirAll(bin, 0o755)
				testenv.Program(t, filepath.Join(bin, "codex"), "#!/bin/sh\n")
			}
			if c.nvmSh != "" {
				to, err := exec.LookPath("timeout")
				if err != nil {
					t.Skip("no timeout(1)")
				}
				if err := os.Symlink(to, filepath.Join(sys, "timeout")); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(nvm, "nvm.sh"), []byte(c.nvmSh), 0o644)
			}
			cmd := exec.Command("/bin/sh", "-c", wslProbeScript())
			cmd.Env = []string{"HOME=" + home, "PATH=" + sys + ":/usr/bin:/bin"}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			want := "bin:codex " + filepath.Join(nvm, "versions", "node", c.want, "bin", "codex") + "\n"
			if !strings.Contains(string(out), want) {
				t.Fatalf("probe said:\n%s\nwant %q", out, want)
			}
		})
	}
}
