package sessions

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/settings"
)

// Codex lists, in its history and its resume picker, only the sessions made
// with the provider it uses now: a rollout's session_meta names its
// model_provider, and so does the thread's row in Codex's state database
// (state_<n>.sqlite). Sessions made through CC Switch ("custom") are gone
// from that list once Codex uses magpie, though their files are all there
// (#887). MoveCodexProvider changes a session's provider, in its files and
// in that row, and nothing else: its messages, id, title and archive stay as
// they were. A copy of each file as it was is kept in magpie's trash folder
// first, and moving it back to the provider it had is the undo.

// codexProviderRe is what a provider id may be: a key of config.toml's
// [model_providers], or a built-in one.
var codexProviderRe = regexp.MustCompile(`^[0-9A-Za-z_.-]{1,64}$`)

// CodexMove is one session moved to another provider.
type CodexMove struct {
	ID     string   `json:"id"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Files  []string `json:"files"`
	Backup string   `json:"backup"` // its files as they were
	DB     []string `json:"db,omitempty"`
}

// codexMetaProvider is the model_provider the first session_meta of a
// rollout names, "openai" (Codex's default) when it names none, and "" when
// the file has no session_meta in its first lines.
func codexMetaProvider(path string) string {
	r, err := openLines(path)
	if err != nil {
		return ""
	}
	defer r.Close()
	br := bufio.NewReaderSize(r, 64<<10)
	for i := 0; i < 8; i++ {
		line, err := br.ReadBytes('\n')
		if gjson.GetBytes(line, "type").String() == "session_meta" {
			if p := gjson.GetBytes(line, "payload.model_provider").String(); p != "" {
				return p
			}
			return "openai"
		}
		if err != nil {
			break
		}
	}
	return ""
}

// codexHomeOf is the Codex folder a rollout is kept in: the one holding its
// sessions or archived_sessions folder.
func codexHomeOf(path string) string {
	for d := filepath.Dir(path); ; {
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		if b := filepath.Base(d); b == "sessions" || b == "archived_sessions" {
			return up
		}
		d = up
	}
}

// codexConfig is what of a Codex folder's config.toml says where sessions
// go: the provider in use (a profile's when one is chosen), and where its
// databases are.
type codexConfig struct {
	Provider   string `toml:"model_provider"`
	Profile    string `toml:"profile"`
	SQLiteHome string `toml:"sqlite_home"`
	Profiles   map[string]struct {
		Provider string `toml:"model_provider"`
	} `toml:"profiles"`
}

func readCodexConfig(home string) codexConfig {
	var c codexConfig
	if b, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
		toml.Unmarshal(b, &c)
	}
	return c
}

// CodexProviderIn is the provider the Codex of a folder uses now, whose
// sessions its history lists: config.toml's model_provider, its profile's
// when it chooses one, else "openai".
func CodexProviderIn(home string) string {
	c := readCodexConfig(home)
	if p, ok := c.Profiles[c.Profile]; ok && c.Profile != "" && p.Provider != "" {
		return p.Provider
	}
	if c.Provider != "" {
		return c.Provider
	}
	return "openai"
}

// codexStateDBs are the state databases of a Codex folder: state_<n>.sqlite,
// in sqlite_home when config.toml (or CODEX_SQLITE_HOME) names one.
func codexStateDBs(home string) []string {
	dir := home
	if c := readCodexConfig(home); c.SQLiteHome != "" && home == CodexDir() {
		dir = c.SQLiteHome
	}
	if d := os.Getenv("CODEX_SQLITE_HOME"); d != "" && home == CodexDir() {
		dir = d
	}
	out, _ := filepath.Glob(filepath.Join(dir, "state_*.sqlite"))
	return out
}

// codexProviders fills in each Codex session's provider, and the one its
// Codex uses now: read from the file once, then kept in the cache.
func codexProviders(out []Managed, groups map[string][]file) {
	homes := map[string]string{}
	filled := false
	for i := range out {
		m := &out[i]
		if m.Agent != "codex" || m.ReadOnly {
			continue
		}
		fs := groups["codex:"+m.ID]
		var main *file
		for j := range fs {
			if fs[j].main && (main == nil || fs[j].path == m.Path) {
				main = &fs[j]
			}
		}
		if main == nil {
			continue
		}
		st := cache[main.path]
		if st != nil && st.Provider == "" {
			st.Provider = codexMetaProvider(main.path)
			filled = filled || st.Provider != ""
		}
		if st != nil {
			m.Provider = st.Provider
		} else {
			m.Provider = codexMetaProvider(main.path)
		}
		home := codexHomeOf(main.path)
		if _, ok := homes[home]; !ok {
			homes[home] = CodexProviderIn(home)
		}
		m.UsesProvider = homes[home]
	}
	if filled {
		saveCache()
	}
}

// MoveCodexProvider moves a Codex session to the provider to: the
// model_provider of each session_meta in its files, and of its row in
// Codex's state database. A session written to in the last minute is left
// as it is (ErrActive), as Codex may be running it. Each file is copied to
// <magpie dir>/trash/codex-provider/ before it is changed, and rewritten
// whole into a file beside it that then takes its place, keeping its time.
func MoveCodexProvider(id, to string) (CodexMove, error) {
	if !safeID.MatchString(id) {
		return CodexMove{}, errors.New("no such session")
	}
	if !codexProviderRe.MatchString(to) {
		return CodexMove{}, fmt.Errorf("%q isn't a provider id", to)
	}
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	all := allFiles()
	groups := map[string][]file{}
	var mine []file
	for _, f := range all {
		if f.agent == "codex" {
			groups[f.key] = append(groups[f.key], f)
			mine = append(mine, f)
		}
	}
	refresh(mine, all)
	var fs []file
	price := pricer()
	for _, g := range groups {
		if one, _ := assemble(g, price); one.ID == id && !one.ReadOnly {
			fs = g
			break
		}
	}
	var paths []string
	for _, f := range fs {
		for _, p := range []string{f.path, rolloutTwin(f.path)} {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && !contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return CodexMove{}, errors.New("no such session")
	}
	now := time.Now()
	for _, p := range paths {
		if recent(p, now) {
			return CodexMove{}, ErrActive
		}
	}
	mv := CodexMove{ID: id, To: to}
	for _, p := range paths {
		if mv.From = codexMetaProvider(p); mv.From != "" {
			break
		}
	}
	// the files as they were, before any is changed
	name := now.UTC().Format("20060102T150405.000000000") + "-" + id
	dir := filepath.Join(settings.Dir(), "trash", "codex-provider", name)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return CodexMove{}, err
	}
	type item struct {
		From string `json:"from"`
		Name string `json:"name"`
	}
	var items []item
	for i, p := range paths {
		it := item{From: p, Name: strconv.Itoa(i) + "-" + filepath.Base(p)}
		if err := keptCopy(p, filepath.Join(dir, "files", it.Name)); err != nil {
			os.RemoveAll(dir)
			return CodexMove{}, err
		}
		items = append(items, it)
	}
	note, _ := json.MarshalIndent(struct {
		ID    string    `json:"id"`
		From  string    `json:"from"`
		To    string    `json:"to"`
		At    time.Time `json:"at"`
		Items []item    `json:"items"`
	}{id, mv.From, to, now, items}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "move.json"), note, 0o600); err != nil {
		os.RemoveAll(dir)
		return CodexMove{}, err
	}
	mv.Backup = dir
	for _, p := range paths {
		changed, err := rewriteCodexProvider(p, to)
		if err != nil {
			// the files changed so far go back as they were
			for _, it := range items {
				if contains(mv.Files, it.From) {
					keptCopy(filepath.Join(dir, "files", it.Name), it.From)
				}
			}
			return CodexMove{}, err
		}
		if changed {
			mv.Files = append(mv.Files, p)
		}
	}
	for _, f := range fs {
		delete(cache, f.path)
	}
	saveCache()
	homes := map[string]bool{}
	for _, p := range paths {
		homes[codexHomeOf(p)] = true
	}
	for home := range homes {
		for _, db := range codexStateDBs(home) {
			if ok, err := moveCodexThread(db, id, to); err != nil {
				return mv, fmt.Errorf("the files moved, but Codex's %s didn't: %w", filepath.Base(db), err)
			} else if ok {
				mv.DB = append(mv.DB, db)
			}
		}
	}
	return mv, nil
}

// CodexThreadProviders is every provider the threads of a Codex folder
// were started on, as its state databases keep them: the Codex app resumes
// a thread on that one, whatever config.toml's model_provider is now, and
// fails to load its config for it when config.toml has no table of that
// id (#1372). A database without the threads table, or one that can't be
// read, says nothing.
func CodexThreadProviders(home string) []string {
	var out []string
	for _, p := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(p), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		rows, err := db.Query(`SELECT DISTINCT model_provider FROM threads`)
		if err == nil {
			for rows.Next() {
				var s sql.NullString
				if rows.Scan(&s) == nil && codexProviderRe.MatchString(s.String) && !contains(out, s.String) {
					out = append(out, s.String)
				}
			}
			rows.Close()
		}
		db.Close()
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// keptCopy copies a file, keeping its mode and time.
func keptCopy(from, to string) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(to, fi.ModTime(), fi.ModTime())
}

// setMetaProvider is a line with its session_meta's model_provider set to
// to, every other byte as it was; any other line as it is. A session_meta
// that names no provider (a subagent's segment, an old Codex's) is left as
// it is too, so moving back gives the file it was.
func setMetaProvider(line []byte, to string) ([]byte, bool) {
	if !bytes.Contains(line[:min(len(line), 1024)], cxMeta) || gjson.GetBytes(line, "type").String() != "session_meta" {
		return line, false
	}
	r := gjson.GetBytes(line, "payload.model_provider")
	if r.Type != gjson.String || r.Index <= 0 || r.Str == to {
		return line, false
	}
	val, _ := json.Marshal(to)
	out := append([]byte{}, line[:r.Index]...)
	out = append(out, val...)
	return append(out, line[r.Index+len(r.Raw):]...), true
}

// rewriteCodexProvider sets the provider of every session_meta in a rollout,
// compressed or not, and says whether anything changed. The file is written
// beside it and renamed over it, with the time it had.
func rewriteCodexProvider(path, to string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	in, err := openLines(path)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-"+filepath.Base(path)+"-*")
	if err != nil {
		return false, err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	var w io.Writer = tmp
	var z *zstd.Encoder
	if packed(path) {
		if z, err = zstd.NewWriter(tmp); err != nil {
			return false, err
		}
		w = z
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	br := bufio.NewReaderSize(in, 64<<10)
	changed := false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out, ok := setMetaProvider(line, to)
			changed = changed || ok
			if _, werr := bw.Write(out); werr != nil {
				return false, werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, err
		}
	}
	if !changed {
		return false, nil
	}
	if err := bw.Flush(); err != nil {
		return false, err
	}
	if z != nil {
		if err := z.Close(); err != nil {
			return false, err
		}
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	in.Close()
	os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	done = true
	return true, nil
}

// moveCodexThread sets a thread's model_provider in a Codex state
// database, and says whether it had a row there. A database without the
// threads table (another version of Codex) is left alone.
func moveCodexThread(path, id, to string) (bool, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_pragma=busy_timeout(5000)"}
	if len(u.Path) >= 2 && u.Path[1] == ':' {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return false, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('threads') WHERE name IN ('id','model_provider')`).Scan(&n); err != nil || n != 2 {
		return false, nil
	}
	res, err := db.Exec(`UPDATE threads SET model_provider = ? WHERE id = ?`, to, id)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}
