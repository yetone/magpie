package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentplug"
)

// agyLaunchDesc is agent-agy's launch: the gateway in env, magpie's own
// settings file through --settings, and remote-control as agy is.
func agyLaunchDesc() agentplug.Desc {
	return agentplug.Desc{ID: "agy", Bin: "agy", Format: "json", Model: "model", Prefix: "magpie/",
		Launch: &agentplug.Launch{Env: map[string]string{"GEMINI_API_KEY": "{key}"}, Model: "--model",
			Args: map[string]string{"--settings": "{config}"}, Plain: []string{"remote-control", "--remote-control", "-remote-control"}}}
}

// The function a distro's shell gets starts the agent with launch.args
// before the model, the user's arguments after, and runs a plain argument
// as the agent is: in bash, zsh and fish, whichever are here.
func TestPluggedDistroFunctionArgsAndPlain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the distro's shells")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "magpie settings.json")
	writeFile(t, cfg, `{"model": "magpie/relay/glm"}`)
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "agy"), "#!/bin/sh\necho \"key=$GEMINI_API_KEY args=$*\"\n")
	os.Chmod(filepath.Join(bin, "agy"), 0o755)
	l := newLaunch(agyLaunchDesc(), cfg)
	read := modelSed("json", "model", "magpie/")
	vars := l.vars("http://gw", "k1")
	for _, sh := range []string{"bash", "zsh", "fish"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("no %s", sh)
			continue
		}
		kind := "posix"
		if sh == "fish" {
			kind = "fish"
		}
		fn := filepath.Join(t.TempDir(), "fn")
		writeFile(t, fn, l.distroFunction(kind, vars, read, cfg, ""))
		run := func(args string) string {
			t.Helper()
			cmd := exec.Command(path, "-c", "source "+posixQuote(fn)+"; agy "+args)
			cmd.Env = []string{"HOME=" + dir, "PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %s: %v\n%s", sh, args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		on := "key=k1 args=--settings " + cfg + " "
		for args, want := range map[string]string{
			"-p hi":                  on + "--model magpie/relay/glm -p hi",
			"--model=own -p hi":      on + "--model=own -p hi",
			"remote-control start":   "key= args=remote-control start",
			"--remote-control":       "key= args=--remote-control",
			"-p x -remote-control=y": "key= args=-p x -remote-control=y",
		} {
			if got := run(args); got != want {
				t.Errorf("%s agy %s:\n got %s\nwant %s", sh, args, got, want)
			}
		}
		// off magpie: as it is
		writeFile(t, cfg, `{"model": "gemini-3-pro"}`)
		if got := run("-p hi"); got != "key= args=-p hi" {
			t.Errorf("%s off magpie: %s", sh, got)
		}
		writeFile(t, cfg, `{"model": "magpie/relay/glm"}`)
	}
}

// The PowerShell function starts the agent with launch.args from what
// `magpie <id> env` prints (settings=…), and runs a plain argument as the
// agent is.
func TestPluggedPowerShellFunctionArgsAndPlain(t *testing.T) {
	ps, err := exec.LookPath("pwsh")
	if err != nil {
		if ps, err = exec.LookPath("powershell"); err != nil {
			t.Skip("no PowerShell")
		}
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "magpie settings.json")
	var prog string
	if runtime.GOOS == "windows" {
		prog = filepath.Join(dir, "magpie.cmd")
		writeFile(t, prog, "@echo GEMINI_API_KEY=k1\r\n@echo settings="+cfg+"\r\n@echo model=magpie/relay/glm\r\n")
		writeFile(t, filepath.Join(dir, "agy.cmd"), "@echo key=%GEMINI_API_KEY% args=%*\r\n")
	} else {
		prog = filepath.Join(dir, "magpie")
		writeFile(t, prog, "#!/bin/sh\necho GEMINI_API_KEY=k1\necho 'settings="+cfg+"'\necho model=magpie/relay/glm\n")
		writeFile(t, filepath.Join(dir, "agy"), "#!/bin/sh\necho \"key=$GEMINI_API_KEY args=$*\"\n")
		os.Chmod(prog, 0o755)
		os.Chmod(filepath.Join(dir, "agy"), 0o755)
	}
	l := newLaunch(agyLaunchDesc(), cfg)
	fn := filepath.Join(dir, "fn.ps1")
	writeFile(t, fn, l.function("powershell", prog))
	run := func(args string) string {
		t.Helper()
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", ". '"+fn+"'; agy "+args)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agy %s: %v\n%s", args, err, out)
		}
		return strings.Join(strings.Fields(strings.ReplaceAll(string(out), `"`, "")), " ")
	}
	for args, want := range map[string]string{
		"-p hi":                "key=k1 args=--settings " + cfg + " --model magpie/relay/glm -p hi",
		"--model=own -p hi":    "key=k1 args=--settings " + cfg + " --model=own -p hi",
		"remote-control start": "key= args=remote-control start",
		"--remote-control":     "key= args=--remote-control",
	} {
		if got := run(args); got != strings.Join(strings.Fields(want), " ") {
			t.Errorf("agy %s:\n got %s\nwant %s", args, got, want)
		}
	}
}
