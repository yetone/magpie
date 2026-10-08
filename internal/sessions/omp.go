package sessions

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

// omp (oh-my-pi, a fork of Pi) keeps its sessions as Pi does, a file per
// session, <time>_<session id>.jsonl, in a folder per working directory
// under its agent folder's sessions/, and they are read as Pi's are (piParse).
// What omp adds: a fixed-width "title" line before the header, which it
// rewrites in place (the title_change entry appended with each new title is
// read instead), the header's own title, "model_usage" entries for model
// calls outside the conversation, and its subagents' and advisor's sessions
// in the session's artifacts folder beside its file (<time>_<id>/<agent
// id>.jsonl, theirs in turn one folder deeper), which count in the session
// they ran in. `omp --resume <id>` picks one up again.

// OmpDir is omp's agent folder, ~/.omp/agent, as magpie's omp agent has it.
func OmpDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omp", "agent")
}

// ompSessionRoots are the folders omp keeps sessions in: its agent folder's
// sessions/, each profile's, and $XDG_DATA_HOME/omp/sessions once omp has
// moved its data there (Linux and macOS).
func ompSessionRoots() []string {
	roots := ompRootsIn(OmpDir())
	if d := appdir.Getenv("XDG_DATA_HOME"); d != "" && runtime.GOOS != "windows" {
		roots = append(roots, filepath.Join(d, "omp", "sessions"))
	}
	return roots
}

// ompRootsIn are the sessions folders of an omp agent folder (~/.omp/agent):
// its own, and each profile's beside it (~/.omp/profiles/<name>/agent).
func ompRootsIn(dir string) []string {
	roots := []string{filepath.Join(dir, "sessions")}
	profiles, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "profiles", "*", "agent", "sessions"))
	return append(roots, profiles...)
}

func ompFiles() []file {
	var out []file
	seen := map[string]bool{}
	for _, root := range ompSessionRoots() {
		out = ompFilesUnder(out, seen, root)
	}
	return out
}

// ompFilesIn are the sessions in a distro's omp folder (~/.omp/agent) and
// its profiles': the variables that move them (PI_CODING_AGENT_DIR,
// $XDG_DATA_HOME) are the distro's and aren't read from Windows.
func ompFilesIn(dir string) []file {
	var out []file
	seen := map[string]bool{}
	for _, root := range ompRootsIn(dir) {
		out = ompFilesUnder(out, seen, root)
	}
	return out
}

// ompProfile identifies the profile owning a main session file. Default
// and XDG stores have no profile component in their paths.
func ompProfile(path string) string {
	if path == "" {
		return ""
	}
	sessions := filepath.Dir(filepath.Dir(path))
	agent := filepath.Dir(sessions)
	profile := filepath.Dir(agent)
	profiles := filepath.Dir(profile)
	if filepath.Base(sessions) != "sessions" || filepath.Base(agent) != "agent" ||
		filepath.Base(profiles) != "profiles" || filepath.Base(filepath.Dir(profiles)) != ".omp" {
		return ""
	}
	return filepath.Base(profile)
}

// ompFilesUnder adds the sessions under one sessions folder: each
// <time>_<id>.jsonl, and its subagents' and advisor's beside it.
func ompFilesUnder(out []file, seen map[string]bool, root string) []file {
	paths, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		_, id, ok := strings.Cut(name, "_")
		if !ok || id == "" {
			continue
		}
		key := "omp:" + id
		f := file{agent: "omp", key: key, path: p, main: true}
		if !stat(&f) {
			continue
		}
		out = append(out, f)
		// its subagents and advisor, at any depth
		filepath.WalkDir(strings.TrimSuffix(p, ".jsonl"), func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				// side questions (/btw) are kept apart, as .json
				if d.Name() == "btw-history" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(q, ".jsonl") {
				return nil
			}
			sub := file{agent: "omp", key: key, path: q}
			if stat(&sub) {
				out = append(out, sub)
			}
			return nil
		})
	}
	return out
}
