package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// Pi (norcleeh on X) has no setting for variables it gives what it runs:
// its settings.json has none, and docs/environment-variables.md lists only
// the ones Pi reads itself. What it does load by itself is every .ts in its
// agent folder's extensions/ (docs/extensions.md), in its own process, and
// its bash tool runs each command with process.env as it is then
// (getShellEnv in dist/utils/shell.js, @earendil-works/pi-coding-agent
// 1.4.0). So magpie writes extensions/magpie-env.ts there, which sets the
// variables in Pi's environment as Pi starts: Pi itself, its tools and what
// they run all have them. A variable already set when Pi starts, the
// user's shell's, is left as it is. The file opens with magpie's mark: one
// without it is the user's own, never written over or removed.

// piEnvMark opens the file magpie writes (the mark pi_extension.go's
// magpie.ts opens with too).
const piEnvMark = "// magpie: written by magpie"

// piEnvLine holds the variables, one JSON object on one line.
const piEnvLine = "const MAGPIE_ENV = "

const piEnvHead = piEnvMark + `, and written again as its Library's environment variables change.
// The variables magpie's Library › Environment variables gives Pi. Pi has
// no setting for variables of its own, so this extension, which Pi loads by
// itself, sets them in Pi's environment as it starts: Pi, its bash tool and
// what that runs have them. One already set when Pi starts (your shell's)
// is left as it is. Untick Pi for a variable in magpie to have it out of
// here; with this first line changed, magpie leaves the file to you.
`

const piEnvBody = `
export default function () {
	for (const [name, value] of Object.entries(MAGPIE_ENV)) {
		if (process.env[name] === undefined) process.env[name] = value;
	}
}
`

// piEnvPath is magpie's variables extension in Pi's agent folder dir.
func piEnvPath(dir string) string {
	return filepath.Join(dir, "extensions", "magpie-env.ts")
}

// readPiEnv is the variables magpie's extension at path sets: none when
// there is no file, an error when it is the user's own or can't be read.
func readPiEnv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	raw = edit.TrimBOM(raw)
	if !bytes.HasPrefix(raw, []byte(piEnvMark)) {
		return nil, fmt.Errorf("%s is your own (its first line isn't magpie's): it is left as it is", path)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), piEnvLine)
		if !ok {
			continue
		}
		out := map[string]string{}
		if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(v), ";")), &out); err != nil {
			break
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s has variables magpie can't read: it is left as it is", path)
}

// writePiEnv sets and takes out variables in magpie's extension at path,
// keeping the rest it holds; with none left the file goes.
func writePiEnv(path string, set map[string]string, del []string) error {
	cur, err := readPiEnv(path)
	if err != nil {
		return err
	}
	for _, k := range del {
		delete(cur, k)
	}
	maps.Copy(cur, set)
	if len(cur) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	// Go's JSON escapes U+2028 and U+2029, so the line is JavaScript too;
	// its keys come out sorted
	data, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(path, []byte(piEnvHead+piEnvLine+string(data)+";\n"+piEnvBody))
}
