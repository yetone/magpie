package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The function in PowerShell reads `magpie agy env` from a magpie.exe
// linked as a GUI program, as the release is, and starts agy with it:
// the built-in agy's function and a plugin's alike.
func TestPowerShellReadsGUIMagpieEnv(t *testing.T) {
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("no PowerShell")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go to build a GUI program")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "settings.json")
	src := filepath.Join(dir, "src")
	writeFile(t, filepath.Join(src, "go.mod"), "module fake\n\ngo 1.21\n")
	writeFile(t, filepath.Join(src, "main.go"), `package main

import "fmt"

func main() {
	fmt.Println("GEMINI_API_KEY=k1")
	fmt.Println("GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:1")
	fmt.Println("settings=`+filepath.ToSlash(cfg)+`")
	fmt.Println("model=magpie/relay/glm")
}
`)
	prog := filepath.Join(dir, "bin", "magpie.exe")
	build := exec.Command(gobin, "build", "-ldflags=-H=windowsgui", "-o", prog, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(dir, "agy.cmd"), "@echo key=%GEMINI_API_KEY% args=%*\r\n")
	for name, fn := range map[string]string{
		"agy":         agyFunction("powershell", prog),
		"plugged agy": newLaunch(agyLaunchDesc(), filepath.ToSlash(cfg)).function("powershell", prog),
	} {
		file := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".ps1")
		writeFile(t, file, fn)
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", ". '"+file+"'; agy -p hi")
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		got := strings.Join(strings.Fields(strings.ReplaceAll(string(out), `"`, "")), " ")
		want := "key=k1 args=--settings " + filepath.ToSlash(cfg) + " --model magpie/relay/glm -p hi"
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}
