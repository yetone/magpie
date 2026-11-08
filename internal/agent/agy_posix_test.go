//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The function agy magpie puts in a POSIX profile is read by every shell
// that reads the file: dash too, as sh -lc when ~/.profile reads ~/.bashrc
// (ljlwlcz on X: fnOS, where bash's "function agy {" failed an SSH probe's
// sh -lc). Each shell here reads the block after an alias named agy, as a
// profile does, and then agy starts the agy on PATH.
func TestAgyFunctionInEveryPOSIXShell(t *testing.T) {
	var shells []string
	for _, sh := range []string{"dash", "sh", "bash", "zsh", "mksh", "busybox"} {
		if p, err := exec.LookPath(sh); err == nil {
			shells = append(shells, p)
		}
	}
	if len(shells) == 0 {
		t.Skip("no POSIX shell here")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "home")
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\necho \"agy key=$GEMINI_API_KEY gw=$GOOGLE_GEMINI_BASE_URL args=$*\"\n"
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{\n  \"model\": \"magpie/relay/glm-4.6\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "no such magpie")
	for _, c := range []struct{ name, fn, want string }{
		{"here", agyFunction("posix", gone), "agy key= gw= args=a b"},
		{"distro", agyDistroFunction("posix", "k'1", "http://172.20.0.1:3425"), "agy key=k'1 gw=http://172.20.0.1:3425 args=--model magpie/relay/glm-4.6 a b"},
	} {
		profile := filepath.Join(dir, c.name+".profile")
		body := "alias agy='echo ALIAS' 2>/dev/null\n# >>> magpie agy >>>\n" + c.fn + "\n# <<< magpie agy <<<\n"
		if err := os.WriteFile(profile, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, sh := range shells {
			args := []string{"-c", `. "$1" && agy a b`, "sh", profile}
			if filepath.Base(sh) == "busybox" {
				args = append([]string{"sh"}, args...)
			}
			cmd := exec.Command(sh, args...)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if got := strings.TrimSpace(string(out)); err != nil || got != c.want {
				t.Errorf("%s in %s: %v\n%s\nwant %s\nprofile:\n%s", c.name, sh, err, got, c.want, body)
			}
		}
	}
}
