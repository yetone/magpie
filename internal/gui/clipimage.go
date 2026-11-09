package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/proc"
)

// copyImageOS puts a PNG on the system's clipboard with the system's own
// tool (clipImageCommand): Wails' clipboard holds text only.
func copyImageOS(ctx context.Context, b []byte) error {
	f, err := os.CreateTemp("", "magpie-clip-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	name, args, env, err := clipImageCommand(runtime.GOOS, os.Getenv, exec.LookPath, f.Name())
	if err != nil {
		return err
	}
	cmd := proc.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if runtime.GOOS == "linux" {
		// xclip and wl-copy leave a process behind that holds the
		// clipboard: one that inherited a pipe of ours would keep the
		// read open until the next copy, so they get no output to write
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("copying the picture: %w", err)
		}
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return fmt.Errorf("copying the picture: %s", firstLine(s))
		}
		return fmt.Errorf("copying the picture: %w", err)
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(s)
}

// winClipScript puts the PNG at $env:MAGPIE_CLIP_PNG on Windows' clipboard
// both as "PNG", which browsers, chat apps and Office paste with its
// pixels as they are, and as a bitmap for the rest, kept once PowerShell
// ends (SetDataObject's copy).
const winClipScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$b = [System.IO.File]::ReadAllBytes($env:MAGPIE_CLIP_PNG)
$d = New-Object System.Windows.Forms.DataObject
$d.SetData('PNG', $false, (New-Object System.IO.MemoryStream(,$b)))
$d.SetImage([System.Drawing.Image]::FromStream((New-Object System.IO.MemoryStream(,$b))))
[System.Windows.Forms.Clipboard]::SetDataObject($d, $true)`

// errNoImageClipboard is a Linux with neither wl-copy nor xclip.
var errNoImageClipboard = errors.New("no tool to copy a picture with: install wl-clipboard (Wayland) or xclip (X11), or save it instead")

// clipImageCommand is the command that puts the PNG at file on goos's
// clipboard, and what it needs in its environment: osascript on macOS,
// PowerShell on Windows (as an STA thread, which the clipboard needs),
// and on Linux wl-copy in a Wayland session, else xclip.
func clipImageCommand(goos string, getenv func(string) string, look func(string) (string, error), file string) (name string, args, env []string, err error) {
	switch goos {
	case "darwin":
		// the path is an argument, never a part of the script
		return "osascript", []string{
			"-e", "on run argv",
			"-e", "set the clipboard to (read (POSIX file (item 1 of argv)) as «class PNGf»)",
			"-e", "end run",
			file,
		}, nil, nil
	case "windows":
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-STA", "-Command", winClipScript}, []string{"MAGPIE_CLIP_PNG=" + file}, nil
	}
	if getenv("WAYLAND_DISPLAY") != "" {
		if p, err := look("wl-copy"); err == nil {
			// wl-copy reads the file from a shell's < : sh only opens it
			return "sh", []string{"-c", `exec "$0" --type image/png < "$1"`, p, file}, nil, nil
		}
	}
	if p, err := look("xclip"); err == nil {
		return p, []string{"-selection", "clipboard", "-t", "image/png", "-i", file}, nil, nil
	}
	return "", nil, nil, errNoImageClipboard
}
