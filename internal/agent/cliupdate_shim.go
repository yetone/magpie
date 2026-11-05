package agent

// A version manager's shim (mise's, asdf's) stands where PATH finds an
// agent's CLI, and runs the program of the Node version it picks. The shim
// itself says nothing of how that program was installed: on a Mac or Linux
// mise's is a link to the mise binary, on Windows a copy of mise-shim.exe,
// a hard or symbolic link to mise.exe, or a .cmd running `mise x --`; none
// leads into node_modules or names it (flp on Discord: an npm -g agent under
// mise on Windows read "can't tell how it was installed"). The program is
// asked of the manager, `mise which <cmd>` / `asdf which <cmd>`, and how it
// was installed read from that: npm's own, of that Node version, with its
// prefix.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// shim is a version manager's shim an agent's CLI was found as.
type shim struct {
	kind string // mise, asdf
	mgr  string // the manager's program, to ask which program the shim runs
	name string // the command the shim stands for: claude, not claude.exe
}

// asdfExec is the line asdf's shims run: `exec asdf exec "claude" "$@"`
// (asdf 0.16 on), `exec /home/u/.asdf/bin/asdf exec "claude" "$@"` before.
var asdfExec = regexp.MustCompile(`(?m)^exec (\S*asdf) exec "`)

// shimAt says whose shim bin is; nil when it is none, or the manager to ask
// isn't here.
func shimAt(bin string) *shim {
	name := filepath.Base(bin)
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".exe", ".cmd", ".bat":
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
	}
	if s := miseShim(bin); s != nil {
		s.name = name
		return s
	}
	// asdf's: a script of its own (it has no Windows build)
	if st, err := os.Stat(bin); err == nil && st.Size() < 16<<10 {
		if b, err := os.ReadFile(bin); err == nil && bytes.Contains(b, []byte("\n# asdf-plugin: ")) {
			if m := asdfExec.FindSubmatch(b); m != nil {
				mgr := string(m[1])
				if !filepath.IsAbs(mgr) || !isFile(mgr) {
					mgr = lookTool("asdf", filepath.Join(os.Getenv("ASDF_DIR"), "bin", "asdf"), filepath.Join(filepath.Dir(filepath.Dir(bin)), "bin", "asdf"))
				}
				if mgr != "" {
					return &shim{kind: "asdf", mgr: mgr, name: name}
				}
			}
		}
	}
	return nil
}

// miseShim is bin as mise's shim: a link to mise itself (mise's shims on a
// Mac or Linux, symlink mode on Windows), or a file in mise's shims folder
// (exe, file and hardlink modes on Windows, where mise keeps the mode in
// shims\.mode).
func miseShim(bin string) *shim {
	if real, err := proc.RealPath(bin); err == nil && filepath.Base(real) != filepath.Base(bin) && isMise(real) {
		return &shim{kind: "mise", mgr: real}
	}
	dir := filepath.Dir(bin)
	if !strings.EqualFold(filepath.Base(dir), "shims") {
		return nil
	}
	ours := strings.EqualFold(filepath.Base(filepath.Dir(dir)), "mise") || isFile(filepath.Join(dir, ".mode"))
	if d := os.Getenv("MISE_DATA_DIR"); d != "" && sameFolder(filepath.Join(d, "shims"), dir) {
		ours = true
	}
	if !ours {
		return nil
	}
	// mise-shim.exe runs the mise on PATH; the installers' own folders
	// after it, for a magpie whose PATH lacks it
	home, _ := os.UserHomeDir()
	mgr := lookTool("mise", filepath.Join(home, ".local", "bin", exe("mise")),
		filepath.Join(appdir.Getenv("LOCALAPPDATA"), "mise", "bin", exe("mise")))
	if mgr == "" {
		return nil
	}
	return &shim{kind: "mise", mgr: mgr}
}

func isMise(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	return b == "mise" || b == "mise.exe"
}

func sameFolder(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// lookTool is name on PATH, else the first of at that is a file.
func lookTool(name string, at ...string) string {
	if p, err := proc.Look(name); err == nil {
		return p
	}
	for _, p := range at {
		if filepath.IsAbs(p) && isFile(p) {
			return p
		}
	}
	return ""
}

// shimWhich asks a version manager which program its shim runs, from the
// home folder (whose config a terminal opened there has); a var so tests can
// stand in for it.
var shimWhich = func(s *shim) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, s.mgr, "which", s.name)
	cmd.Stdin = nil
	cmd.Dir, _ = os.UserHomeDir()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return lastLines(string(out), 1), nil
}

var shimTargets memo

// throughShim is the program bin runs when bin is a version manager's
// shim, with the shim it is; bin itself and nil when it is no shim, or
// the manager can't say.
func throughShim(bin string) (string, *shim) {
	s := shimAt(bin)
	if s == nil {
		return bin, nil
	}
	p := shimTargets.get(bin, s.mgr, 10*time.Minute, time.Minute, func() (string, error) {
		p, err := shimWhich(s)
		if err != nil {
			return "", err
		}
		if p = runnable(strings.TrimSpace(p)); p == "" {
			return "", errors.New(s.kind + " which " + s.name + ": no program")
		}
		return p, nil
	})
	if p == "" {
		return bin, nil
	}
	return p, s
}

// runnable is p as a program that runs here: on Windows, `mise which`
// names npm's extensionless sh script, beside which are its .cmd and .ps1;
// "" when there is none.
func runnable(p string) string {
	if !filepath.IsAbs(p) {
		return ""
	}
	if runtime.GOOS == "windows" && filepath.Ext(p) == "" {
		for _, x := range []string{".exe", ".cmd", ".bat"} {
			if isFile(p + x) {
				return p + x
			}
		}
		return ""
	}
	if !isFile(p) {
		return ""
	}
	return p
}

// installedAt is how the CLI found at bin was installed, read from the
// program it is: bin itself, or what the version manager's shim at bin
// runs. A program npm puts under mise's Node needs a new shim (mise
// reshim), which mise's npm runs itself on a Mac or Linux but not on
// Windows: the update is followed by one.
func installedAt(spec cliSpec, bin string) (string, *updater) {
	real, s := throughShim(bin)
	u := howInstalled(spec, real)
	if u != nil && u.via == "self" && spec.line != nil {
		u.pkg = spec.line(installedVersion(real))
	}
	if u != nil && s != nil && s.kind == "mise" {
		u.reshim = []string{s.mgr, "reshim"}
	}
	return real, u
}
