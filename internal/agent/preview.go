package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// Before an agent is disconnected the user is shown what changes in its
// files, line by line (the owner's 断开 design: 删除、改回、保留), not a
// sentence about it. What Disconnect does depends on the stash, the record
// and each agent's own unwiring, so rather than a second account of it that
// could say otherwise, magpie runs Disconnect itself on a copy: the agent's
// config folder and magpie's own files under a temporary home, in a process
// of its own (DryRunArg), and compares the copy with the files as they are.

// DryRunArg is the command line's first word for the process that
// disconnects an agent on a copy of its files (main.go).
const DryRunArg = "agent-disconnect-dry-run"

const (
	dryProvidersVar  = "MAGPIE_DRY_PROVIDERS"
	dryProvidersFile = ".magpie-dry-providers"
)

// dryProviders is, in the process DryRun runs, the ids of the providers the
// magpie that asked for the preview has on.
var dryProviders map[string]bool

// disconnectDryRun is confined to the preview subprocess. Its temporary
// HOME isolates files, but not a running agent reached through its CLI.
var disconnectDryRun bool

// DryRun disconnects an agent in the process DisconnectPreview starts, on
// the copy under its temporary home.
func DryRun(id string) error {
	disconnectDryRun = true
	if p := os.Getenv(dryProvidersVar); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dryProviders = map[string]bool{}
		for _, id := range strings.Split(string(b), "\n") {
			if id != "" {
				dryProviders[id] = true
			}
		}
	}
	a, err := Find(id)
	if err != nil {
		return err
	}
	return a.Disconnect()
}

// FileChange is how disconnecting changes one of the agent's files.
type FileChange struct {
	Path    string     `json:"path"`              // as the user knows it, ~ for home
	Removed bool       `json:"removed,omitempty"` // the file goes
	Created bool       `json:"created,omitempty"` // the file is made
	Lines   []LineDiff `json:"lines,omitempty"`
}

// LineDiff is a line of a file disconnecting changes: Op is "-" for a line
// taken out, "+" for one put in, "~" for a line whose value goes back to
// another (Was is the line now).
type LineDiff struct {
	Op   string `json:"op"`
	Text string `json:"text"`
	Was  string `json:"was,omitempty"`
}

// DisconnectPreview is what Disconnect would change in the agent's files,
// found by running it on a copy. An error is a preview not to be had (the
// agent's files outside home, a WSL agent): the dialog then says it in
// words.
func DisconnectPreview(a *Agent, exe string) ([]FileChange, error) {
	if a.Native != nil {
		plan, err := a.Native.Disconnect()
		if err != nil {
			return nil, err
		}
		return plan.Preview(), nil
	}
	if a.WSL != "" || a.Path == "" {
		return nil, errors.New("no preview for this agent")
	}
	if appdir.Portable() != "" {
		return nil, errors.New("no preview for a portable magpie")
	}
	// A portable VS Code's folder is found through its launcher, on the
	// PATH the preview's process keeps, not through home: that process
	// would disconnect the build's own files, not the copy (#4151).
	if vscodePortableFile(a.Path) {
		return nil, errors.New("no preview for a portable VS Code")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	rel := func(p string) (string, bool) {
		r, err := filepath.Rel(home, p)
		return r, err == nil && r != "." && !strings.HasPrefix(r, "..")
	}
	dir := filepath.Dir(a.Path)
	dirRel, ok := rel(dir)
	cfg := appdir.Config()
	cfgRel, ok2 := rel(cfg)
	if !ok || !ok2 {
		return nil, errors.New("the agent's files are outside home")
	}
	sweepPreviews()
	tmp, err := os.MkdirTemp("", "magpie-preview-")
	if err != nil {
		return nil, err
	}
	defer removePreview(tmp)
	tmp, _ = filepath.EvalSymlinks(tmp)
	// the agent's folder: its files, copied; its folders aren't its config
	before := map[string][]byte{}
	if err := copyTop(dir, filepath.Join(tmp, dirRel), before, false); err != nil {
		return nil, err
	}
	// magpie's: its files copied (the stash, the record, the providers),
	// its folders (plugins) only read, so linked
	if err := copyTop(cfg, filepath.Join(tmp, cfgRel), nil, true); err != nil {
		return nil, err
	}
	// the stash's keys that name a file by its whole path (a plugin's
	// agent's, AtomCode's, Droid's…) name it under the copy's home, where
	// the dry run finds the file: with home's own they were never found
	// there, and the preview showed nothing put back (Purfeather,
	// plugins#799)
	if r, ok := rel(stashPath()); ok {
		if err := moveStashKeys(filepath.Join(tmp, r), home, tmp); err != nil {
			return nil, err
		}
	}
	// magpie's providers as this magpie has them: some come from another
	// agent's sign-in (Codex's auth.json) the copy doesn't hold, and a
	// model of theirs is magpie's all the same (dryProviders)
	var ids []string
	for _, p := range provider.All() {
		if p.On() {
			ids = append(ids, p.ID)
			ids = append(ids, p.Was...)
		}
	}
	held := filepath.Join(tmp, dryProvidersFile)
	if err := os.WriteFile(held, []byte(strings.Join(ids, "\n")), 0o600); err != nil {
		return nil, err
	}
	env := append(previewEnv(home, tmp), dryProvidersVar+"="+held)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, exe, DryRunArg, a.ID)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dry run: %v: %s", err, strings.TrimSpace(out.String()))
	}
	// what the agent's folder holds now against what it held
	var changes []FileChange
	copied := filepath.Join(tmp, dirRel)
	after := map[string][]byte{}
	entries, _ := os.ReadDir(copied)
	for _, e := range entries {
		if e.Type().IsRegular() {
			if b, err := os.ReadFile(filepath.Join(copied, e.Name())); err == nil {
				after[e.Name()] = b
			}
		}
	}
	shown := func(name string) string { return "~/" + filepath.ToSlash(filepath.Join(dirRel, name)) }
	// the agent's own file first, then the others by name
	names := []string{filepath.Base(a.Path)}
	for n := range before {
		if n != names[0] {
			names = append(names, n)
		}
	}
	for n := range after {
		if _, ok := before[n]; !ok {
			names = append(names, n)
		}
	}
	sortTail(names)
	for _, n := range names {
		was, had := before[n]
		now, has := after[n]
		switch {
		case had && !has:
			changes = append(changes, FileChange{Path: shown(n), Removed: true})
		case !had && has:
			changes = append(changes, FileChange{Path: shown(n), Created: true, Lines: diffLines("", string(now))})
		case had && has && !bytes.Equal(was, now):
			if l := diffLines(string(was), string(now)); len(l) > 0 {
				changes = append(changes, FileChange{Path: shown(n), Lines: l})
			}
		}
	}
	return changes, nil
}

// previewStale is how old a sandbox in the temporary dir must be for the
// sweep to take it: a live preview lasts seconds (the dry run is bounded
// at 20 seconds), so an hour-old one is a sandbox an earlier magpie was
// killed holding (#3136).
const previewStale = time.Hour

// sweepPreviews removes sandboxes an earlier magpie was killed holding,
// as sweepCursorSnaps does for its magpie-cursor-* copies.
func sweepPreviews() {
	old, _ := filepath.Glob(filepath.Join(os.TempDir(), "magpie-preview-*"))
	for _, d := range old {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > previewStale {
			removePreview(d)
		}
	}
}

// removePreview removes a sandbox, retrying while processes of the dry run
// are still letting go of files in it (Windows keeps a file locked until
// the last holder is out), and says so when it still could not.
var previewRemoveAll = os.RemoveAll

func removePreview(dir string) error {
	var err error
	for try := 0; try < 5; try++ {
		if err = previewRemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(time.Duration(try+1) * 100 * time.Millisecond)
	}
	log.Printf("agent: removing disconnect-preview sandbox %s: %v", dir, err)
	return err
}

// sortTail sorts all but the first name.
func sortTail(names []string) {
	for i := 2; i < len(names); i++ {
		for j := i; j > 1 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}

// copyTop copies a folder's files into to, keeping what they held in
// before when it is given; with link, its folders are linked there.
func copyTop(from, to string, before map[string][]byte, link bool) error {
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	const most = 16 << 20
	for _, e := range entries {
		p := filepath.Join(from, e.Name())
		st, err := os.Stat(p) // a linked file is its target
		if err != nil {
			continue
		}
		if st.IsDir() {
			if link {
				os.Symlink(p, filepath.Join(to, e.Name()))
			}
			continue
		}
		if !st.Mode().IsRegular() || st.Size() > most || notConfig(e.Name()) {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o600); err != nil {
			return err
		}
		if before != nil {
			before[e.Name()] = b
		}
	}
	return nil
}

// moveStashKeys rewrites the stash copied to path so a key naming a file
// under home names the same file under to. A stash that isn't there or
// doesn't parse is left as it is: the dry run reads it as the real one does.
func moveStashKeys(path, home, to string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[movedKey(k, home, to)] = v
	}
	b, err = json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// movedKey is k with each path in it under home put under to: home where a
// separator follows it, matched as the system matches names (Windows's
// without case: %APPDATA% may spell the home folder otherwise).
func movedKey(k, home, to string) string {
	home = strings.TrimRight(home, `/\`)
	if home == "" {
		return k
	}
	same := func(a, b string) bool { return a == b }
	if runtime.GOOS == "windows" {
		same = strings.EqualFold
	}
	var out strings.Builder
	for i := 0; i < len(k); {
		if j := i + len(home); j < len(k) && same(k[i:j], home) && (k[j] == '/' || k[j] == filepath.Separator) {
			out.WriteString(to)
			i = j
			continue
		}
		out.WriteByte(k[i])
		i++
	}
	return out.String()
}

// notConfig is a file no agent keeps its settings in: a database, a log, a
// history, which can be large and which Disconnect never writes.
func notConfig(name string) bool {
	for _, ext := range []string{".sqlite", ".sqlite-wal", ".sqlite-shm", ".db", ".db-wal", ".db-shm", ".jsonl", ".log"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// previewEnv is this process's environment with home, and the folders under
// it, moved to the copy.
func previewEnv(home, tmp string) []string {
	moved := map[string]bool{"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "APPDATA": true, "LOCALAPPDATA": true}
	for _, k := range agentenv.Vars {
		moved[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !moved[k] {
			out = append(out, kv)
			continue
		}
		if k == "HOME" || k == "USERPROFILE" {
			continue
		}
		// a folder under home is the same one under the copy's; one
		// elsewhere isn't copied, so it is left out
		if r, err := filepath.Rel(home, v); err == nil && !strings.HasPrefix(r, "..") {
			out = append(out, k+"="+filepath.Join(tmp, r))
		}
	}
	// The test binary recognizes this marker and keeps the fixture HOME
	// instead of creating a second isolated home. Production binaries ignore
	// the variable.
	return append(out, "HOME="+tmp, "USERPROFILE="+tmp, "TMPDIR="+tmp, "MAGPIE_TEST_SANDBOX="+tmp)
}

// diffLines is the lines that differ between two texts, in the order of the
// new one: a line taken out, one put in, and a line whose key stays with its
// value changed (model = "x" → model = "y") as one going back. Values that
// read as secrets are masked. Between the lines both start and end with,
// texts too far apart to compare line by line (OpenCode's table of
// thousands of magpie models taken out) keep the lines of the new one
// found in order in the old.
func diffLines(a, b string) []LineDiff {
	x, y := splitLines(a), splitLines(b)
	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	keepX, keepY := make([]bool, len(x)), make([]bool, len(y))
	for i := 0; i < pre; i++ {
		keepX[i], keepY[i] = true, true
	}
	for i := 1; i <= suf; i++ {
		keepX[len(x)-i], keepY[len(y)-i] = true, true
	}
	mx, my := x[pre:len(x)-suf], y[pre:len(y)-suf]
	if kx, ky, ok := sameLines(mx, my, 4000); ok {
		copy(keepX[pre:], kx)
		copy(keepY[pre:], ky)
	} else if len(mx)*len(my) <= 50_000_000 {
		// each line of the one, in order, kept where the other has it
		// next: what is left of a file once a block is taken out of it
		// (past that, all of it taken out and put in)
		i := 0
		for j := range my {
			for k := i; k < len(mx); k++ {
				if mx[k] == my[j] {
					keepX[pre+k], keepY[pre+j] = true, true
					i = k + 1
					break
				}
			}
		}
	}
	var out []LineDiff
	var gone, came []string
	flush := func() {
		// a line taken out and one put in under the same key, as deep in
		// the file, are one going back (Claude's own "model" with
		// modelPicker's taken out, not one of the picker's)
		for _, c := range came {
			k := lineKey(c)
			paired := false
			for i, g := range gone {
				if k != "" && lineKey(g) == k && indent(g) == indent(c) {
					out = append(out, LineDiff{Op: "~", Text: mask(c), Was: mask(g)})
					gone = append(gone[:i], gone[i+1:]...)
					paired = true
					break
				}
			}
			if !paired {
				out = append(out, LineDiff{Op: "+", Text: mask(c)})
			}
		}
		for _, g := range gone {
			out = append(out, LineDiff{Op: "-", Text: mask(g)})
		}
		gone, came = nil, nil
	}
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && keepX[i] && keepY[j]:
			flush()
			i++
			j++
		case i < len(x) && !keepX[i]:
			if strings.TrimSpace(x[i]) != "" {
				gone = append(gone, x[i])
			}
			i++
		default:
			if strings.TrimSpace(y[j]) != "" {
				came = append(came, y[j])
			}
			j++
		}
	}
	flush()
	// a bracket or brace left alone on its line says nothing
	var kept []LineDiff
	for _, l := range out {
		if t := strings.Trim(strings.TrimSpace(l.Text), "{}[](),"); t == "" {
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

// sameLines is which lines of x and of y a shortest edit keeps (Myers'
// diff: time and memory grow with the edits, not the files — an agent's
// config can be 30,000 lines, and magpie changes a few). Not ok past most
// edits.
func sameLines(x, y []string, most int) (keepX, keepY []bool, ok bool) {
	n, m := len(x), len(y)
	if most > n+m {
		most = n + m
	}
	off := most + 1
	v := make([]int, 2*off+1)
	// trace[d] is v as step d found it, for k in -d-1..d+1
	var trace [][]int
	get := func(d, k int) int { return trace[d][k+d+1] }
	for d := 0; d <= most; d++ {
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var xi int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				xi = v[off+k+1]
			} else {
				xi = v[off+k-1] + 1
			}
			yi := xi - k
			for xi < n && yi < m && x[xi] == y[yi] {
				xi++
				yi++
			}
			v[off+k] = xi
			if xi < n || yi < m {
				continue
			}
			// back from the end, marking the diagonals walked
			keepX, keepY = make([]bool, n), make([]bool, m)
			xi, yi = n, m
			for e := d; e >= 0; e-- {
				k := xi - yi
				var pk int
				if k == -e || (k != e && get(e, k-1) < get(e, k+1)) {
					pk = k + 1
				} else {
					pk = k - 1
				}
				px := get(e, pk)
				py := px - pk
				for xi > px && yi > py && xi > 0 && yi > 0 {
					keepX[xi-1], keepY[yi-1] = true, true
					xi--
					yi--
				}
				xi, yi = px, py
			}
			return keepX, keepY, true
		}
	}
	return nil, nil, false
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

// lineKey is what a line sets: TOML's and .env's key before =, JSON's and
// YAML's before :, "" for a line that sets nothing.
var keyRe = regexp.MustCompile(`^\s*(?:export\s+)?"?([A-Za-z0-9_.\-]+)"?\s*[:=]`)

func lineKey(l string) string {
	if m := keyRe.FindStringSubmatch(l); m != nil {
		return m[1]
	}
	if t := strings.TrimSpace(l); strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		return t
	}
	return ""
}

func indent(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

var secretKey = regexp.MustCompile(`(?i)(key|token|secret|password|auth)`)

// mask hides the value of a line whose key reads as a secret's; magpie's
// own token, which is no secret, stays.
func mask(l string) string {
	k := lineKey(l)
	// …_TOKENS is a count of them (CLAUDE_CODE_MAX_CONTEXT_TOKENS)
	if k == "" || !secretKey.MatchString(k) || strings.HasSuffix(strings.ToUpper(k), "TOKENS") {
		return l
	}
	m := keyRe.FindStringIndex(l)
	if m == nil { // a table header or compact array, not a key/value line
		return l
	}
	val := strings.TrimSpace(l[m[1]:])
	bare := strings.Trim(val, `"', `)
	if bare == "" || bare == "magpie" || strings.HasPrefix(bare, "{") || strings.HasPrefix(bare, "$") {
		return l
	}
	return l[:m[1]] + " ••••"
}
