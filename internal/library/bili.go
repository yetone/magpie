package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// billion-context (npm billion-context, its CLI bili) compresses an
// agent's context as it runs: old turns are folded into summaries the
// model can open again (compress, decompress, search_context), so a long
// session stays under the window. Its native plugins are written into an
// agent's own files by its own installer (bili plugin install <agent>);
// magpie runs that for the agents switched on, and bili plugin remove for
// those switched off.
//
// Only the agents where bili's plugin works with magpie in front are
// offered: Pi, omp and OpenCode load it in process, and their requests
// still go to magpie with bili's tools added; Claude Code's goes through
// bili's local proxy, which forwards to the gateway (ANTHROPIC_BASE_URL
// .../bili/<magpie's address>). Codex's lane is an MCP server only, ZCode's
// routes only its own provider, Kimi Code's renames the default model in
// config.toml at each start (bili-kimi) and dsh's installs through dsh's
// package manager: none of those is offered.
//
// Zed has no plugin of bili's, and Hermes' sets HTTPS_PROXY alone, which
// magpie's http://127.0.0.1 provider never takes: their magpie provider
// goes through bili's /bili/ prefix with bili start answering there
// (biliURLLanes, bili_url.go).
//
// bili's remove doesn't put a file back as it was: it leaves "packages": []
// in Pi's settings.json and "extensions:" in omp's config.yml, and after
// magpie rewrote Claude Code's settings.json it took ANTHROPIC_BASE_URL out
// altogether (the agent then went to Anthropic) and put back the
// DISABLE_AUTO_COMPACT it had added. So magpie keeps each file as it was
// before the install and as the install left it, and after a remove puts
// back what the install changed and nothing changed since (biliRestore).

// Claude Code's auto-compaction stays on beside bili's plugin. bili's
// install writes DISABLE_AUTO_COMPACT=1, so a session bili doesn't
// compress has nothing left to keep it under the window: Claude Desktop's
// Code tab runs Claude Code on the same settings.json with its own base
// URL, past bili's proxy (bili's #2290), and a window bili takes larger
// than the one magpie serves lets the session reach it first. Claude Code
// then answers each message at once with "Prompt is too long", sending
// nothing. magpie sets the "1" bili added to "0", which bili leaves as a
// value of the user's (and keeps across its SessionStart hook and a
// reinstall): auto-compaction fires only at the window magpie wrote
// (CLAUDE_CODE_AUTO_COMPACT_WINDOW), which bili, when it compresses,
// keeps the session under. A value the user had before the install is
// left as theirs.

// BiliURL is billion-context's page.
const BiliURL = "https://github.com/ranxianglei/billion-context"

// biliPackage is its npm package.
const biliPackage = "billion-context"

// biliLanes are the agents (magpie's ids) bili's plugin is offered for,
// each with the files its install writes that magpie puts back.
var biliLanes = map[string]func(a *agent.Agent) []string{
	"pi":       func(a *agent.Agent) []string { return []string{a.Path} },
	"omp":      func(a *agent.Agent) []string { return []string{a.Path} },
	"opencode": func(a *agent.Agent) []string { return []string{a.Path} },
	"claude": func(a *agent.Agent) []string {
		return []string{a.Path, filepath.Join(filepath.Dir(a.Path), "commands", "acp-cache.md")}
	},
}

// BiliAgent is one agent bili's plugin can be given to.
type BiliAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	On   bool   `json:"on"`
	// Status is what bili plugin list says of it when that isn't simply
	// installed or not: partly installed, broken, an error
	Status string `json:"status,omitempty"`
}

// BiliView is the billion-context part of the Library page.
type BiliView struct {
	Path    string      `json:"path,omitempty"` // "" when bili isn't installed
	Version string      `json:"version,omitempty"`
	Agents  []BiliAgent `json:"agents"`
	URL     string      `json:"url"`
	// Install is the command Install runs, "" when there is no npm
	Install string `json:"install,omitempty"`
	// ListErr is why bili plugin list said nothing magpie could read
	ListErr string   `json:"listErr,omitempty"`
	Restart []string `json:"restart,omitempty"`
	Backup  string   `json:"backup,omitempty"`
	// Saved is what bili says it saved (bili_saved.go), nil when no bili
	// has answered here yet
	Saved *BiliSaved `json:"saved,omitempty"`
}

var biliMu sync.Mutex

// biliPath is bili's, "" when it isn't installed: on PATH, or in a folder
// a user's tools are installed in (proc.FindTool: npm -g's, with ~/.local
// as its prefix where the documented install puts it, bun's including a
// BUN_INSTALL), which may not be on magpie's PATH yet. On Windows npm's
// shim is bili.cmd and bun's is bili.exe.
var biliPath = func() string { return proc.FindTool("bili") }

// biliInstaller is billion-context's documented install: npm's global
// install with ~/.local as the prefix, so it needs no administrator; on
// Windows npm's own global folder, which is the user's. nil without npm.
var biliInstaller = func() []string {
	npm, err := proc.Look("npm")
	if err != nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return []string{npm, "install", "-g", biliPackage}
	}
	return []string{npm, "install", "-g", biliPackage, "--prefix=" + filepath.Join(home(), ".local")}
}

// biliRun runs bili with nothing to answer it.
var biliRun = func(bin string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	// a bili in ~/.local/bin is a node script: its folder goes first, and
	// node is found where the user's shell finds it
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, fmt.Errorf("bili %s didn't finish in %s", strings.Join(args, " "), timeout)
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("bili %s: %s", strings.Join(args, " "), lastLines(text, 4))
	}
	return text, nil
}

// biliAgents are the agents here bili's plugin can be given to. One in
// WSL isn't: bili there is the distro's, and magpie runs this machine's.
func biliAgents(agents []*agent.Agent) []*agent.Agent {
	if agents == nil {
		agents = detected()
	}
	var out []*agent.Agent
	for _, a := range agents {
		_, plugin := biliLanes[a.ID]
		if _, url := biliURLLanes[a.ID]; (plugin || url) && a.WSL == "" && !a.CLIMissing() {
			out = append(out, a)
		}
	}
	return out
}

// biliStatusLine is a row of bili plugin list: the agent, then its
// status, then the version and how it updates, apart by two spaces or more.
var biliColumns = lazyre.New(`\s{2,}`)

// parseBiliList is each agent's status as bili plugin list prints it.
func parseBiliList(out string) map[string]string {
	st := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		cols := biliColumns.Split(strings.TrimSpace(line), -1)
		if len(cols) < 2 || cols[0] == "agent" {
			continue
		}
		st[cols[0]] = cols[1]
	}
	return st
}

// biliOn is whether a status says the plugin is there and whole.
func biliOn(status string) bool {
	return status == "installed" || strings.HasPrefix(status, "installed (")
}

// ReadBili says whether bili is installed and which agents have its plugin.
// biliVersion is the version in `bili --version`'s output. Its output is
// read with stderr, where bili writes warnings first (a config key it
// doesn't know: "2026-11-09T23:44:06.211 [warn] [v=0.1.213] ..."), so
// the version is the first word of the line that starts with one.
func biliVersion(out string) string {
	for _, l := range strings.Split(out, "\n") {
		w, _, _ := strings.Cut(strings.TrimSpace(l), " ")
		if biliVersionRe.MatchString(w) {
			return w
		}
	}
	return ""
}

var biliVersionRe = lazyre.New(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]*)?$`)

func ReadBili() *BiliView {
	return readBili(detected())
}

func readBili(agents []*agent.Agent) *BiliView {
	v := &BiliView{Agents: []BiliAgent{}, URL: BiliURL, Path: biliPath()}
	var st map[string]string
	if v.Path == "" {
		if c := biliInstaller(); c != nil {
			v.Install = biliShown(c)
		}
	} else {
		if out, err := biliRun(v.Path, 30*time.Second, "--version"); err == nil {
			v.Version = biliVersion(out)
		}
		v.Saved = readBiliSaved()
		out, err := biliRun(v.Path, 30*time.Second, "plugin", "list")
		if st = parseBiliList(out); err != nil || len(st) == 0 {
			v.ListErr = lastLines(out, 2)
			if err != nil {
				v.ListErr = err.Error()
			}
		}
	}
	for _, a := range biliAgents(agents) {
		ba := BiliAgent{ID: a.ID, Name: a.Name, Icon: a.Icon}
		if l, ok := biliURLLanes[a.ID]; ok {
			ba.On = l.on(a)
		} else if s, ok := st[a.ID]; ok {
			ba.On = biliOn(s)
			if !ba.On && s != "not installed" {
				ba.Status = s
			}
		}
		v.Agents = append(v.Agents, ba)
	}
	return v
}

// biliShown is the install command as the page shows it: npm by name.
func biliShown(c []string) string {
	c = slices.Clone(c)
	c[0] = strings.TrimSuffix(filepath.Base(c[0]), ".cmd")
	return strings.Join(c, " ")
}

// biliState is each file of an agent bili's install wrote, as it was
// before (nil: not there) and as the install left it.
type biliFile struct {
	Path   string  `json:"path"`
	Before *string `json:"before"`
	After  *string `json:"after"`
	// Marks are the .bili-bak and .bili-last bili keeps beside it that
	// weren't there before the install: taken away with it
	Marks []string `json:"marks,omitempty"`
}

func biliStateFile() string { return filepath.Join(settings.Dir(), "bili-plugins.json") }

func readBiliState() map[string][]biliFile {
	m := map[string][]biliFile{}
	if b, err := os.ReadFile(biliStateFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeBiliState(m map[string][]biliFile) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(biliStateFile()), 0o700); err != nil {
		return err
	}
	return edit.WriteAtomic(biliStateFile(), b)
}

func biliRead(path string) *string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// SetBili gives bili's plugin to an agent with bili's own installer, or
// takes it away with bili's own remover and puts back what the install
// changed in the agent's files.
func SetBili(id string, on bool) (*BiliView, error) {
	biliMu.Lock()
	defer biliMu.Unlock()
	agents := detected()
	var a *agent.Agent
	for _, x := range biliAgents(agents) {
		if x.ID == id {
			a = x
		}
	}
	files, ok := biliLanes[id]
	_, url := biliURLLanes[id]
	switch {
	case !ok && !url:
		return nil, fmt.Errorf("billion-context isn't offered for %s", id)
	case a == nil:
		return nil, fmt.Errorf("%s isn't installed", id)
	}
	bin := biliPath()
	if bin == "" {
		return nil, fmt.Errorf("billion-context isn't installed — install it first (%s)", BiliURL)
	}
	if url {
		return setBiliURL(a, agents, bin, on)
	}
	paths := files(a)
	b := newBackups()
	for _, p := range append(slices.Clone(paths), claudeJSON()) {
		if a.ID != "claude" && p == claudeJSON() {
			continue
		}
		if err := b.keep(a.ID, p); err != nil {
			return nil, err
		}
	}
	state := readBiliState()
	if on {
		var snap []biliFile
		for _, p := range paths {
			f := biliFile{Path: p, Before: biliRead(p)}
			for _, m := range []string{p + ".bili-bak", p + ".bili-last"} {
				if !exists(m) {
					f.Marks = append(f.Marks, m)
				}
			}
			snap = append(snap, f)
		}
		out, err := biliRun(bin, 2*time.Minute, "plugin", "install", biliLane(id))
		if err != nil {
			return nil, err
		}
		if id == "claude" {
			if err := biliKeepCompact(snap[0].Path, snap[0].Before); err != nil {
				return nil, err
			}
		}
		for i := range snap {
			snap[i].After = biliRead(snap[i].Path)
		}
		state[id] = snap
		if err := writeBiliState(state); err != nil {
			return nil, err
		}
		v := readBili(agents)
		if !slices.ContainsFunc(v.Agents, func(x BiliAgent) bool { return x.ID == id && x.On }) {
			return nil, fmt.Errorf("bili plugin install %s ran, but bili doesn't list it as installed: %s", biliLane(id), lastLines(out, 3))
		}
		v.Restart, v.Backup = []string{id}, b.dir
		if b.dir != "" {
			pruneBackups()
		}
		return v, nil
	}
	cur := map[string]*string{}
	for _, p := range paths {
		cur[p] = biliRead(p)
	}
	if _, err := biliRun(bin, 2*time.Minute, "plugin", "remove", biliLane(id)); err != nil {
		return nil, err
	}
	snap := state[id]
	var errs []error
	for _, p := range paths {
		var f *biliFile
		for i := range snap {
			if snap[i].Path == p {
				f = &snap[i]
			}
		}
		if err := biliRestore(p, f, cur[p], biliRead(p)); err != nil {
			errs = append(errs, err)
		}
		if f != nil {
			for _, m := range f.Marks {
				if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
					errs = append(errs, err)
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	delete(state, id)
	if err := writeBiliState(state); err != nil {
		return nil, err
	}
	v := readBili(agents)
	v.Restart, v.Backup = []string{id}, b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return v, nil
}

// biliCompact is the setting of Claude Code's that turns auto-compaction
// off, and biliCompactOn its value magpie writes in place of bili's "1".
const (
	biliCompact   = "env.DISABLE_AUTO_COMPACT"
	biliCompactOn = "0"
)

// biliKeepCompact turns Claude Code's auto-compaction back on in its
// settings.json at path after bili's install turned it off, unless
// before (the file before the install) had a DISABLE_AUTO_COMPACT of the
// user's own.
func biliKeepCompact(path string, before *string) error {
	if v, has := edit.GetJSON(path, biliCompact); !has || v != "1" {
		return nil
	}
	if b, ok := parseJSONC(before); !ok {
		return nil
	} else if _, had := jsonAt(b, []string{"env", "DISABLE_AUTO_COMPACT"}); had {
		return nil
	}
	return edit.SetJSON(path, edit.KV{Path: biliCompact, Value: biliCompactOn})
}

// keepBiliCompact does biliKeepCompact for a Claude Code plugin magpie
// installed before it did so itself (KeepLinked runs it at start), and has the record of the install say
// "0" where it said bili's "1", so that switching the plugin off takes the
// "0" away with the rest of the install.
func keepBiliCompact() {
	biliMu.Lock()
	defer biliMu.Unlock()
	state := readBiliState()
	snap := state["claude"]
	if len(snap) == 0 || snap[0].After == nil {
		return
	}
	f := &snap[0]
	if v, _ := edit.GetJSON(f.Path, biliCompact); v != "1" {
		return
	}
	if err := biliKeepCompact(f.Path, f.Before); err != nil {
		return
	}
	if v, _ := edit.GetJSON(f.Path, biliCompact); v != biliCompactOn {
		return
	}
	after, ok := parseJSONC(f.After)
	if v, _ := jsonAt(after, []string{"env", "DISABLE_AUTO_COMPACT"}); !ok || v != "1" {
		return
	}
	b, err := edit.PatchJSON([]byte(*f.After), []edit.KV{{Path: biliCompact, Value: biliCompactOn}}, nil)
	if err != nil {
		return
	}
	s := string(b)
	f.After = &s
	_ = writeBiliState(state)
}

// biliLane is bili's name of an agent's plugin: magpie's id, as both use it.
func biliLane(id string) string { return id }

// biliRestore writes p after bili's remove: f is the file before and after
// the install (nil when magpie didn't install it), cur as it was just
// before the remove and now as the remove left it. What the install changed
// and nothing has changed since goes back to what it was; what was changed
// since (magpie picking another model, the user) stays as the remove left
// it; and what the remove changed that the install hadn't goes back.
func biliRestore(p string, f *biliFile, cur, now *string) error {
	var out *string
	switch {
	case f != nil && same(cur, f.After):
		// nothing touched it since the install: as it was before, byte for byte
		out = f.Before
	case strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonc"):
		var before, after *string
		if f != nil {
			before, after = f.Before, f.After
		}
		s, err := biliMergeJSON(before, after, cur, now)
		if err != nil {
			// a file magpie can't read as JSON is left as bili left it
			return nil
		}
		out = s
		if f != nil && f.Before != nil && out != nil && sameJSON(*out, *f.Before) {
			out = f.Before
		}
	default:
		return nil
	}
	if same(out, now) {
		return nil
	}
	if out == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return edit.WriteAtomic(p, []byte(*out))
}

func same(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func parseJSONC(s *string) (map[string]any, bool) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return map[string]any{}, true
	}
	var m map[string]any
	if json.Unmarshal(jsonc.ToJSON(edit.TrimBOM([]byte(*s))), &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

func sameJSON(a, b string) bool {
	x, ok1 := parseJSONC(&a)
	y, ok2 := parseJSONC(&b)
	return ok1 && ok2 && jsonEqual(x, y)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// jsonAt is the value at path in m, and whether there is one.
func jsonAt(m map[string]any, path []string) (any, bool) {
	var v any = m
	for _, k := range path {
		o, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = o[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// jsonDiffs are the paths where a and b differ: down through objects both
// have, an object, array or value that differs as a whole below that.
func jsonDiffs(a, b map[string]any, at []string, out *[][]string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		p := append(slices.Clone(at), k)
		x, okx := a[k]
		y, oky := b[k]
		xm, isx := x.(map[string]any)
		ym, isy := y.(map[string]any)
		switch {
		case okx && oky && isx && isy:
			jsonDiffs(xm, ym, p, out)
		case okx != oky || !jsonEqual(x, y):
			*out = append(*out, p)
		}
	}
}

func under(p, q []string) bool {
	return len(p) >= len(q) && slices.Equal(p[:len(q)], q)
}

// keyPath is p as edit's key paths spell it, false when a key has a
// character they can't hold.
func keyPath(p []string) (string, bool) {
	parts := make([]string, len(p))
	for i, k := range p {
		if k == "" || strings.ContainsAny(k, `*?|#@\!=<>%`) {
			return "", false
		}
		parts[i] = strings.ReplaceAll(k, ".", `\.`)
	}
	return strings.Join(parts, "."), true
}

// biliURL is the ANTHROPIC_BASE_URL bili's Claude Code plugin writes: its
// proxy, then the URL it forwards to.
var biliURL = lazyre.New(`^https?://(?:127\.0\.0\.1|localhost|\[::1\]):\d+/bili/(https?://.+)$`)

// biliMergeJSON is now with what the install changed (before → after) and
// nothing changed since (cur still has it) put back as before, and what
// the remove changed beyond that (cur → now) put back as cur. Without a
// record of the install, a Claude Code base URL the remove took out goes
// back to the address bili forwarded to.
func biliMergeJSON(before, after, cur, now *string) (*string, error) {
	B, ok1 := parseJSONC(before)
	I, ok2 := parseJSONC(after)
	C, ok3 := parseJSONC(cur)
	R, ok4 := parseJSONC(now)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errors.New("not JSON")
	}
	type change struct {
		path []string
		v    any
		has  bool
	}
	var changes []change
	var installed [][]string
	if after != nil {
		jsonDiffs(B, I, nil, &installed)
	}
	for _, p := range installed {
		ci, hc := jsonAt(C, p)
		ii, hi := jsonAt(I, p)
		if hc == hi && jsonEqual(ci, ii) {
			v, has := jsonAt(B, p)
			changes = append(changes, change{p, v, has})
		}
	}
	if after == nil {
		// no record of the install: all the remove took out may be bili's,
		// except the address Claude Code was sent to before bili's proxy
		base := []string{"env", "ANTHROPIC_BASE_URL"}
		c, hc := jsonAt(C, base)
		_, hr := jsonAt(R, base)
		if s, ok := c.(string); ok && hc && !hr {
			if m := biliURL.FindStringSubmatch(s); m != nil {
				changes = append(changes, change{base, m[1], true})
			}
		}
	} else {
		var removed [][]string
		jsonDiffs(C, R, nil, &removed)
		for _, p := range removed {
			if slices.ContainsFunc(installed, func(q []string) bool { return under(p, q) || under(q, p) }) {
				continue
			}
			v, has := jsonAt(C, p)
			changes = append(changes, change{p, v, has})
		}
	}
	if len(changes) == 0 {
		return now, nil
	}
	raw := []byte("{}\n")
	if now != nil {
		raw = []byte(*now)
	}
	var set []edit.KV
	var del []string
	for _, c := range changes {
		kp, ok := keyPath(c.path)
		if !ok {
			continue
		}
		if !c.has {
			del = append(del, kp)
			continue
		}
		b, err := json.Marshal(c.v)
		if err != nil {
			return nil, err
		}
		set = append(set, edit.KV{Path: kp, Value: json.RawMessage(b)})
	}
	out, err := edit.PatchJSON(raw, set, del)
	if err != nil {
		return nil, err
	}
	// an object the remove emptied that wasn't there before goes too
	s := string(out)
	if now == nil && len(set) == 0 {
		return nil, nil
	}
	return &s, nil
}

// InstallBili installs billion-context the way its README says.
func InstallBili() (*BiliView, error) {
	biliMu.Lock()
	defer biliMu.Unlock()
	if biliPath() != "" {
		return ReadBili(), nil
	}
	c := biliInstaller()
	if c == nil {
		return nil, fmt.Errorf("npm isn't installed — install Node.js first, or see %s", BiliURL)
	}
	out, err := runPlain(c, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	proc.Forget()
	v := ReadBili()
	if v.Path == "" {
		return nil, fmt.Errorf("%s ran, but bili still isn't found: %s", biliShown(c), lastLines(out, 3))
	}
	return v, nil
}
