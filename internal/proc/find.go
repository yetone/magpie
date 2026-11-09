package proc

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// FindTool is the command-line tool name as a terminal would run it: on
// PATH, else in one of the folders a user's tools are installed in
// (UserBinDirs), else, on Windows, on the PATH the registry has now, which
// a magpie started before the tool was installed doesn't have (#839: the
// Claude sign-in said Claude Code wasn't installed, its installer having
// put claude.exe in ~/.local/bin after magpie started). A version
// manager's wrapper script found there is given as the program it runs
// (Unwrap). "" when none. Where it is on PATH is kept while those folders
// are unchanged (LookPath); Unwrap keeps its own answer by the script's
// size and time.
func FindTool(name string) string {
	return Unwrap(name, looked("find\x00"+name, func() lookAnswer {
		if p, err := lookPath(name); err == nil {
			return lookAnswer{path: p}
		}
		dirs := findDirs()
		if !mayBeIn(name, dirs) {
			return lookAnswer{}
		}
		return lookAnswer{path: toolIn(name, runtime.GOOS, dirs)}
	}).path)
}

// sandboxVar is testenv.Marker, which proc can't import: the home a test
// binary's testenv.Isolate made.
const sandboxVar = "MAGPIE_TEST_SANDBOX"

// Isolated reports whether this is a test binary testenv isolated, where
// nothing of the machine's own (its folders, its registry) is read.
func Isolated() bool { return os.Getenv(sandboxVar) != "" }

// SystemDirs are paths of the machine's own, outside any home (Homebrew's
// /opt/homebrew/bin, /usr/local/bin, an app in /Applications, the
// registry's PATH), as discovery looks in them: as they are, except in a
// test binary testenv isolated, where each lies under the test's home
// instead. A test that empties PATH means a machine without the tool; the
// host's /opt/homebrew/bin/claude was found anyway, so the tests of the
// downloaded Claude Code failed on a Mac with Homebrew's (#1525). A test
// that wants one there writes it under its home at that path.
func SystemDirs(paths ...string) []string {
	root := os.Getenv(sandboxVar)
	if root == "" {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		out = append(out, filepath.Join(root, "system", strings.TrimPrefix(p, filepath.VolumeName(p))))
	}
	return out
}

// ToolIn is name's program in the first of dirs that has one, as toolIn
// finds it on this system.
func ToolIn(name string, dirs []string) string { return toolIn(name, runtime.GOOS, dirs) }

// toolIn is name's program in the first of dirs that has one: on Windows
// name.exe or name.cmd, as an installer's binary or npm's shim, else the
// program npm's sh shim name runs, when that is a Windows program
// (npmShimExe). A file with no extension is never run on Windows itself:
// CreateProcess refuses it ("%1 is not a valid Win32 application"), and
// what npm left of its shims when an install failed partway is that sh
// one alone (CLJY on Discord: %APPDATA%\npm\claude, 308 bytes, with no
// claude.cmd beside it, was what magpie ran).
func toolIn(name, goos string, dirs []string) string {
	names := []string{name}
	if goos == "windows" {
		names = []string{name + ".exe", name + ".cmd"}
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for _, n := range names {
			p := filepath.Join(d, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
		if goos == "windows" {
			if p := npmShimExe(filepath.Join(d, name)); p != "" {
				return p
			}
		}
	}
	return ""
}

// npmShimLine is the line of npm's sh shim (cmd-shim) that runs a program
// of its package's directly: exec "$basedir/node_modules/<pkg>/<bin>" "$@".
// A shim of a node script names node first, and isn't matched.
var npmShimLine = regexp.MustCompile(`(?m)^\s*exec "\$basedir/([^"$]+\.exe)"\s+"\$@"`)

// npmShimExe is the Windows program the npm sh shim at path runs, where it
// is: Claude Code's is node_modules\@anthropic-ai\claude-code\bin\claude.exe.
// "" when path is no such shim or its program is gone.
func npmShimExe(path string) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > wrapperMax {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	m := npmShimLine.FindSubmatch(b)
	if m == nil {
		return ""
	}
	rel := filepath.FromSlash(string(m[1]))
	if !filepath.IsLocal(rel) {
		return ""
	}
	exe := filepath.Join(filepath.Dir(path), rel)
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		return ""
	}
	return exe
}
