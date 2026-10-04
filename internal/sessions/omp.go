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
	roots := []string{filepath.Join(OmpDir(), "sessions")}
	profiles, _ := filepath.Glob(filepath.Join(filepath.Dir(OmpDir()), "profiles", "*", "agent", "sessions"))
	roots = append(roots, profiles...)
	if d := appdir.Getenv("XDG_DATA_HOME"); d != "" && runtime.GOOS != "windows" {
		roots = append(roots, filepath.Join(d, "omp", "sessions"))
	}
	return roots
}

func ompFiles() []file {
	var out []file
	seen := map[string]bool{}
	for _, root := range ompSessionRoots() {
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
	}
	return out
}
