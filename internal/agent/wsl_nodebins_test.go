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

// The WSL probe finds a command in the folders Node's managers keep one
// in, as proc.UserBinDirs does here: sh -l reads only ~/.profile, and
// fnm's installer puts its PATH in ~/.bashrc. 千里不再 on Discord: a WSL
// Pi from fnm, not run yet (no ~/.pi), wasn't found.
func TestWSLProbeFindsNodeManagersBins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe runs in a Linux distro")
	}
	sys := t.TempDir() // PATH: nothing of pi's
	for _, c := range []struct {
		name string
		// lay sets home up and says where pi is
		lay func(home string) string
	}{
		{"fnm's default", func(home string) string {
			fnm := filepath.Join(home, ".local", "share", "fnm")
			for _, v := range []string{"v18.20.4", "v22.23.4"} {
				os.MkdirAll(filepath.Join(fnm, "node-versions", v, "installation", "bin"), 0o755)
			}
			os.MkdirAll(filepath.Join(fnm, "aliases"), 0o755)
			os.Symlink(filepath.Join(fnm, "node-versions", "v18.20.4", "installation"), filepath.Join(fnm, "aliases", "default"))
			for _, v := range []string{"v18.20.4", "v22.23.4"} {
				testenv.Program(t, filepath.Join(fnm, "node-versions", v, "installation", "bin", "pi"), "#!/bin/sh\n")
			}
			return filepath.Join(fnm, "aliases", "default", "bin")
		}},
		{"fnm with no default: the highest", func(home string) string {
			fnm := filepath.Join(home, ".local", "share", "fnm")
			for _, v := range []string{"v9.11.2", "v22.23.4", "v18.20.4"} {
				d := filepath.Join(fnm, "node-versions", v, "installation", "bin")
				os.MkdirAll(d, 0o755)
				testenv.Program(t, filepath.Join(d, "pi"), "#!/bin/sh\n")
			}
			return filepath.Join(fnm, "node-versions", "v22.23.4", "installation", "bin")
		}},
		{"mise's installs", func(home string) string {
			root := filepath.Join(home, ".local", "share", "mise", "installs", "node")
			for _, v := range []string{"9.11.2", "22.12.0", "18.20.4"} {
				d := filepath.Join(root, v, "bin")
				os.MkdirAll(d, 0o755)
				testenv.Program(t, filepath.Join(d, "pi"), "#!/bin/sh\n")
			}
			return filepath.Join(root, "22.12.0", "bin")
		}},
		{"~/.npmrc's prefix", func(home string) string {
			os.WriteFile(filepath.Join(home, ".npmrc"), []byte("registry=https://registry.npmmirror.com\nprefix = \"~/.npm-packages\"\r\n"), 0o644)
			return filepath.Join(home, ".npm-packages", "bin")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			bin := c.lay(home)
			os.MkdirAll(bin, 0o755)
			if _, err := os.Stat(filepath.Join(bin, "pi")); err != nil {
				testenv.Program(t, filepath.Join(bin, "pi"), "#!/bin/sh\necho 0.73.1\n")
			}
			cmd := exec.Command("/bin/sh", "-c", wslProbeScript)
			cmd.Env = []string{"HOME=" + home, "PATH=" + sys + ":/usr/bin:/bin"}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if want := "bin:pi " + filepath.Join(bin, "pi") + "\n"; !strings.Contains(string(out), want) {
				t.Fatalf("probe said:\n%s\nwant %q", out, want)
			}
		})
	}
}

// A command on Windows' drives isn't the distro's (#406), and it doesn't
// hide the distro's own: WSL puts Windows' PATH on the distro's, so a Pi
// npm installed on Windows was all `command -v pi` found, and the distro's
// Pi from fnm was never looked for.
func TestWSLProbeLooksPastWindowsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe runs in a Linux distro")
	}
	home, root := t.TempDir(), t.TempDir()
	win := filepath.Join(root, "winc") // Windows' C: as the distro mounts it
	winNpm := filepath.Join(win, "Users", "me", "AppData", "Roaming", "npm")
	os.MkdirAll(winNpm, 0o755)
	testenv.Program(t, filepath.Join(winNpm, "pi"), "#!/bin/sh\n")
	mounts := filepath.Join(root, "mounts")
	os.WriteFile(mounts, []byte("/dev/sdc / ext4 rw 0 0\nC:\\134 "+win+" 9p rw 0 0\n"), 0o644)
	own := filepath.Join(home, ".local", "share", "fnm", "node-versions", "v22.23.4", "installation", "bin")
	os.MkdirAll(own, 0o755)
	testenv.Program(t, filepath.Join(own, "pi"), "#!/bin/sh\necho 0.73.1\n")

	// The probe asks for a version under timeout(1), which a distro has
	// (coreutils) and macOS before 27 hasn't: there `timeout 10 pi
	// --version` was "command not found", no version, on every macOS 26
	// CI run (#5570). A stand-in runs the command as timeout would.
	tools := t.TempDir()
	if _, err := os.Stat("/usr/bin/timeout"); err != nil {
		testenv.Program(t, filepath.Join(tools, "timeout"), "#!/bin/sh\nshift\nexec \"$@\"\n")
	}

	script := strings.ReplaceAll(wslProbeScript, "/proc/mounts", mounts)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin:" + tools + ":" + winNpm}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if want := "bin:pi " + filepath.Join(own, "pi") + "\n"; !strings.Contains(string(out), want) {
		t.Fatalf("probe said:\n%s\nwant %q", out, want)
	}
	if d := parseProbe("U", string(out)); d == nil || !wslKindOf("pi").found(*d) || d.Versions["pi"] != "0.73.1" {
		t.Fatalf("parsed: %+v\nout:\n%s", d, out)
	}
}
