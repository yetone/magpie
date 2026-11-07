package sessions

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
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
	// the ids of its [model_providers] tables
	Providers map[string]any `toml:"model_providers"`
}

// readCodexConfig is the folder's codexConfig, parsed again only when
// config.toml changes: a config.toml holding a table for each of ten
// thousand projects took ~0.15s of CPU to parse, on each visit to Sessions
// (#1665). What it gives is shared and must not be changed.
func readCodexConfig(home string) codexConfig {
	c, _ := filememo.Read("sessions codex config", filepath.Join(home, "config.toml"), func(b []byte) (codexConfig, error) {
		var c codexConfig
		toml.Unmarshal(b, &c)
		return c, nil
	})
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
	if d := appdir.Getenv("CODEX_SQLITE_HOME"); d != "" && home == CodexDir() {
		dir = d
	}
	out, _ := filepath.Glob(filepath.Join(dir, "state_*.sqlite"))
	return out
}

// codexProviders fills in each Codex session's provider, and the one its
// Codex uses now: read from the file once, then kept in the cache.
func codexProviders(out []Managed, groups map[string][]file) {
	homes := map[string]string{}
	paged := map[string]map[string]string{}
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
			paged[home] = codexPagedProviders(home)
		}
		// a paginated thread is on the provider its row names, which
		// RetagPagedCodexThreads moves without its files
		if p := paged[home][m.ID]; p != "" {
			m.Provider = p
		}
		m.UsesProvider = homes[home]
	}
	if filled {
		saveCache()
	}
}

// pagedMemo keeps each state database's paginated threads' providers
// while the database and its write-ahead log are as they were: Sessions
// asks on every poll, and a history is thousands of rows.
var pagedMemo = struct {
	sync.Mutex
	m map[string]pagedRead
}{m: map[string]pagedRead{}}

type pagedRead struct {
	stamp string
	ids   map[string]string
}

// codexPagedProviders are the providers a Codex folder's state databases
// keep for its paginated threads, by id. A database without history_mode
// (an older Codex), or one that can't be read, says nothing.
func codexPagedProviders(home string) map[string]string {
	out := map[string]string{}
	for _, p := range codexStateDBs(home) {
		stamp := ""
		for _, f := range []string{p, p + "-wal"} {
			if fi, err := os.Stat(f); err == nil {
				stamp += fmt.Sprintf("%d.%d;", fi.ModTime().UnixNano(), fi.Size())
			}
		}
		pagedMemo.Lock()
		r, ok := pagedMemo.m[p]
		pagedMemo.Unlock()
		if !ok || r.stamp != stamp {
			r = pagedRead{stamp: stamp, ids: readPagedProviders(p)}
			pagedMemo.Lock()
			pagedMemo.m[p] = r
			pagedMemo.Unlock()
		}
		for id, prov := range r.ids {
			out[id] = prov
		}
	}
	return out
}

// forgetPaged drops what codexPagedProviders read: magpie wrote a row
func forgetPaged() {
	pagedMemo.Lock()
	clear(pagedMemo.m)
	pagedMemo.Unlock()
}

func readPagedProviders(path string) map[string]string {
	ids := map[string]string{}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
	if len(u.Path) >= 2 && u.Path[1] == ':' {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return ids
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, model_provider FROM threads WHERE history_mode = 'paginated'`)
	if err != nil {
		return ids
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var prov sql.NullString
		if rows.Scan(&id, &prov) == nil && codexProviderRe.MatchString(prov.String) {
			ids[id] = prov.String
		}
	}
	return ids
}

// MoveCodexProvider moves a Codex session to the provider to: the
// model_provider of each session_meta in its files, and of its row in
// Codex's state database. A session written to in the last minute is left
// as it is (ErrActive), as Codex may be running it, and so is one a Codex
// has open (holdCodexThread). Each file is copied to
// <magpie dir>/trash/codex-provider/ before it is changed, and rewritten
// whole into a file beside it that then takes its place, keeping its time.
func MoveCodexProvider(id, to string) (CodexMove, error) {
	if !safeID.MatchString(id) {
		return CodexMove{}, errors.New("no such session")
	}
	if !codexProviderRe.MatchString(to) {
		return CodexMove{}, fmt.Errorf("%q isn't a provider id", to)
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	all := allFiles()
	groups := byKey(all, func(f *file) bool { return f.agent == "codex" })
	var mine []file
	for _, f := range all {
		if f.agent == "codex" {
			mine = append(mine, f)
		}
	}
	refresh(mine, all)
	var fs []file
	price, titles := pricer(), codexTitleOf()
	for _, g := range groups {
		if one, _ := assembleListed(g, price, titles); one.ID == id && !one.ReadOnly {
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
	return moveCodexPaths(id, to, paths, time.Now())
}

// moveCodexPaths moves one Codex thread, whose files are paths, to the
// provider to, as MoveCodexProvider says; storeMu and mu are held.
func moveCodexPaths(id, to string, paths []string, now time.Time) (CodexMove, error) {
	for _, p := range paths {
		if recent(p, now) {
			return CodexMove{}, ErrActive
		}
	}
	var homes []string
	for _, p := range paths {
		if h := codexHomeOf(p); !contains(homes, h) {
			homes = append(homes, h)
		}
	}
	// the thread held in each of its Codex homes until its files and its
	// row have moved, before anything is copied
	for _, home := range homes {
		letGo, err := holdCodexThread(home, id)
		if err != nil {
			return CodexMove{}, err
		}
		defer letGo()
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
	for _, p := range paths {
		delete(cache, p)
	}
	saveCache()
	for _, home := range homes {
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

// errThreadOpen is a thread a Codex has open: it holds the thread's writer
// lock, and writes on to the file it opened whatever takes its place. It
// is ErrActive too, so it is left, and said, as one written to in the
// last minute is.
var errThreadOpen error = threadOpen{}

type threadOpen struct{}

func (threadOpen) Error() string        { return "a Codex has the session open" }
func (threadOpen) Is(target error) bool { return target == ErrActive }

// holdCodexThread holds the thread id of the Codex home home as Codex
// holds one it writes to, archives or deletes
// (codex-rs/rollout/src/writer_lock.rs, acquire): its lock file in
// thread-writer-locks, made and locked under the home's coordination
// lock. A Codex that resumes the thread meanwhile is turned away ("already
// has an active writer"). It is errThreadOpen when a Codex holds it.
// letGo lets it go and takes the lock file away under the coordination
// lock, as Codex's guard does. A home without thread-writer-locks (a Codex
// from before them) has none to hold, and is given none: the port
// (PortCodexRollouts) takes one there for a Codex that holds its threads.
func holdCodexThread(home, id string) (letGo func(), err error) {
	if home == "" {
		return func() {}, nil
	}
	locks := filepath.Join(home, "thread-writer-locks")
	if fi, err := os.Stat(locks); errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir() {
		return func() {}, nil
	} else if err != nil {
		return nil, err
	}
	coord := filepath.Join(locks, ".coordination.lock")
	unlock, held := filelock.Lock(coord, filelock.Wait)
	if !held {
		return nil, errThreadOpen
	}
	defer unlock()
	path := filepath.Join(locks, strings.ToLower(id)+".lock")
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if got, err := filelock.TryLock(lf); !got {
		lf.Close()
		if err != nil {
			return nil, err
		}
		return nil, errThreadOpen
	}
	return func() {
		unlock, held := filelock.Lock(coord, filelock.Wait)
		filelock.Unlock(lf)
		lf.Close()
		if held {
			os.Remove(path)
			unlock()
		}
	}, nil
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

// codexThreadsOn are the ids of the threads a Codex folder's state
// databases keep on the provider p. A database without the threads table,
// or one that can't be read, says nothing.
func codexThreadsOn(home, p string) []string {
	var out []string
	for _, path := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		rows, err := db.Query(`SELECT id FROM threads WHERE model_provider = ?`, p)
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil && safeID.MatchString(id) && !contains(out, id) {
					out = append(out, id)
				}
			}
			rows.Close()
		}
		db.Close()
	}
	return out
}

// CodexThreadsBusyOn says whether a thread a Codex folder's state
// databases keep on the provider p was written to in the last
// ActiveWindow, or is held open by a Codex (its writer lock), which a
// move of p's threads leaves where it is: such a move has more to do once
// the thread is quiet and let go. The Codex app holds the lock of every
// thread it has loaded, the chat on screen among them, for as long as it
// keeps it (codex-rs rollout recorder, writer_lock.rs), however long ago
// it was written to. A database that can't be read, and a rollout that
// can't be found, say nothing.
func CodexThreadsBusyOn(home, p string) bool {
	now := time.Now()
	locked := codexLockedIDs(home)
	for _, path := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		var paths []string
		var held bool
		rows, err := db.Query(`SELECT id, rollout_path FROM threads WHERE model_provider = ?`, p)
		if err == nil {
			for rows.Next() {
				var id, r string
				if rows.Scan(&id, &r) != nil {
					continue
				}
				if locked[strings.ToLower(id)] {
					held = true
				}
				if r != "" {
					paths = append(paths, r)
				}
			}
			rows.Close()
		}
		db.Close()
		for _, r := range paths {
			if fi, err := os.Stat(filepath.Clean(r)); err == nil && now.Sub(fi.ModTime()) < ActiveWindow {
				return true
			}
		}
		if held {
			return true
		}
	}
	return false
}

// codexLockedIDs are the threads of a Codex home a Codex holds open now:
// those whose lock file in thread-writer-locks is locked, as
// holdCodexThread finds them, by lower-case id. Each lock is tried under
// the home's coordination lock and let go at once, so a Codex taking one
// meanwhile waits on the coordination lock rather than being turned away;
// no lock file is made or taken away. A home without thread-writer-locks,
// or with none held, has none.
func codexLockedIDs(home string) map[string]bool {
	if home == "" {
		return nil
	}
	locks := filepath.Join(home, "thread-writer-locks")
	ents, err := os.ReadDir(locks)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".lock"); ok && !e.IsDir() && safeID.MatchString(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	unlock, ok := filelock.Lock(filepath.Join(locks, ".coordination.lock"), filelock.Wait)
	if !ok {
		return nil
	}
	defer unlock()
	out := map[string]bool{}
	for _, id := range ids {
		f, err := os.OpenFile(filepath.Join(locks, id+".lock"), os.O_RDWR, 0)
		if err != nil {
			continue
		}
		got, err := filelock.TryLock(f)
		if got {
			filelock.Unlock(f)
		} else if err == nil {
			out[strings.ToLower(id)] = true
		}
		f.Close()
	}
	return out
}

// MoveCodexThreads moves every thread of a Codex folder its state database
// keeps on the provider from to the provider to, each as MoveCodexProvider
// moves one: its files' session_meta and its row, a copy of the files kept
// in magpie's trash first. Codex lists only the threads of the provider it
// is on (app-server thread/list, the TUI's resume picker), so the threads
// started while magpie was Codex's provider were gone from Codex's lists
// once Codex was off magpie (kaizaoji on X: "the context doesn't exist").
// A thread written to in the last minute, or one a Codex has open, is left
// for the next call, and so is one whose files can't be found; it says
// what it moved and the first error it met, having gone on past it.
func MoveCodexThreads(home, from, to string) ([]CodexMove, error) {
	if !codexProviderRe.MatchString(from) || !codexProviderRe.MatchString(to) || from == to {
		return nil, fmt.Errorf("can't move Codex's threads from %q to %q", from, to)
	}
	moved, _, err := moveCodexThreadIDs(home, from, to, codexThreadsOn(home, from), 0)
	return moved, err
}

// MoveCodexThreadIDs moves those of the threads ids that a Codex folder's
// state database still keeps on the provider from to the provider to, as
// MoveCodexThreads moves them: to undo a move of those threads alone,
// where moving every thread of from would take others with them.
func MoveCodexThreadIDs(home, from, to string, ids []string) ([]CodexMove, error) {
	if !codexProviderRe.MatchString(from) || !codexProviderRe.MatchString(to) || from == to {
		return nil, fmt.Errorf("can't move Codex's threads from %q to %q", from, to)
	}
	on := codexThreadsOn(home, from)
	var still []string
	for _, id := range ids {
		if contains(on, id) {
			still = append(still, id)
		}
	}
	moved, _, err := moveCodexThreadIDs(home, from, to, still, 0)
	return moved, err
}

// moveCodexThreadIDs moves the threads ids of a Codex folder from the
// provider from to the provider to, as MoveCodexThreads says, in the
// order given. With a budget over 0 it stops once the files it has moved
// come to that many bytes, a copy of each kept, and says more when it
// stopped there with a thread still to move.
func moveCodexThreadIDs(home, from, to string, ids []string, budget int64) (moved []CodexMove, more bool, first error) {
	if len(ids) == 0 {
		return nil, false, nil
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	// each thread's files: its rollout, its segments (<id>_<child>) and
	// their compressed twins, in sessions/ or archived_sessions/
	files := map[string][]string{}
	for _, sub := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(home, sub), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), "rollout-") {
				return nil
			}
			for _, id := range ids {
				if strings.Contains(d.Name(), id) {
					files[id] = append(files[id], p)
					break
				}
			}
			return nil
		})
	}
	var spent int64
	now := time.Now()
	for _, id := range ids {
		if len(files[id]) == 0 {
			continue
		}
		if budget > 0 && spent >= budget {
			more = true
			break
		}
		for _, p := range files[id] {
			if fi, err := os.Stat(p); err == nil {
				spent += fi.Size()
			}
		}
		mv, err := moveCodexPaths(id, to, files[id], now)
		if err != nil {
			if !errors.Is(err, ErrActive) && first == nil {
				first = fmt.Errorf("thread %s: %w", id, err)
			}
			continue
		}
		mv.From = from
		moved = append(moved, mv)
	}
	return moved, more, first
}

// MovePackedCodexThreads moves the threads of a Codex folder that
// RelabelCodexThreads leaves, those Codex has packed (.zst, a week after
// their last turn), from the provider from to the provider to, as
// MoveCodexThreads moves one: a copy of its files kept in magpie's trash,
// its file written beside it and renamed over it, and one Codex has open
// or wrote to in the last minute left. Only those whose last turn is
// since since, newest first, and no more once the files moved come to
// budget bytes: a history of thousands of packed threads is gigabytes,
// and the threads a user goes back to are the recent ones. The rest wait
// for the next call, and more says there are some: nothing else may call
// again for hours, and the Codex app refuses each one left (Lice9735 on
// X, a chat from 25 days before). A database whose threads have no updated_at (an
// older Codex) moves none. Codex 0.166 resumes a thread only on the
// provider it is on now ("This chat uses openai, but your current
// provider is magpie"), so a packed thread left on OpenAI couldn't be
// reopened while magpie was Codex's provider.
func MovePackedCodexThreads(home, from, to string, since time.Time, budget int64) (moved []CodexMove, more bool, err error) {
	return moveRecentCodexThreads(home, from, to, since, budget, true)
}

// MoveRecentCodexThreads is MovePackedCodexThreads for every thread on
// from whose last turn is since since, plain or packed: for a provider
// whose id isn't magpie's length, which RelabelCodexThreads can't write
// in place, so each one moved is rewritten with a copy kept. A thread of
// the user's own provider (a relay's, "myrelay") was listed by the Codex
// app once magpie was its provider, and refused: "This chat uses myrelay,
// but your current provider is magpie" (hxy920830 on X).
func MoveRecentCodexThreads(home, from, to string, since time.Time, budget int64) (moved []CodexMove, more bool, err error) {
	return moveRecentCodexThreads(home, from, to, since, budget, false)
}

func moveRecentCodexThreads(home, from, to string, since time.Time, budget int64, packedOnly bool) ([]CodexMove, bool, error) {
	if !codexProviderRe.MatchString(from) || !codexProviderRe.MatchString(to) || from == to || budget <= 0 {
		return nil, false, fmt.Errorf("can't move Codex's threads from %q to %q", from, to)
	}
	var ids []string
	for _, p := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(p), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		rs, err := db.Query(`SELECT id, rollout_path FROM threads WHERE model_provider = ? AND updated_at >= ? ORDER BY updated_at DESC`, from, since.Unix())
		if err == nil {
			for rs.Next() {
				var id, path string
				if rs.Scan(&id, &path) != nil || !safeID.MatchString(id) || contains(ids, id) {
					continue
				}
				path = filepath.Clean(path)
				if !filepath.IsAbs(path) || !strings.Contains(filepath.Base(path), id) || !sameDir(codexHomeOf(path), home) {
					continue
				}
				// packed: the plain file gone, the .zst there and on
				// from; or, packedOnly not, the plain file on from
				plain, _ := strings.CutSuffix(path, zstSuffix)
				if fi, err := os.Lstat(plain); err == nil {
					if packedOnly || !fi.Mode().IsRegular() || codexMetaProvider(plain) != from {
						continue
					}
				} else if fi, err := os.Lstat(plain + zstSuffix); err != nil || !fi.Mode().IsRegular() || codexMetaProvider(plain+zstSuffix) != from {
					continue
				}
				ids = append(ids, id)
			}
			rs.Close()
		}
		db.Close()
	}
	return moveCodexThreadIDs(home, from, to, ids, budget)
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
			if ok {
				// a session_meta names its own history_mode: Codex keeps
				// places in a paginated rollout by byte (fitLine)
				if out, ok = fitLine(line, out, paginated(line)); !ok {
					return false, fmt.Errorf("%s keeps its history by byte, and %q is longer than the provider it names: Codex couldn't open it moved", filepath.Base(path), to)
				}
			}
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
	// the user's session on the disk before it is renamed over the old
	// one: a machine that goes down between can leave it all zero (#1505)
	if err := steady.Sync(tmp); err != nil {
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
	defer forgetPaged()
	res, err := db.Exec(`UPDATE threads SET model_provider = ? WHERE id = ?`, to, id)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}

// RelabelCodexThreads moves the threads of a Codex folder its state
// databases keep on the provider from to the provider to, where that can
// be done in place: ids of one length (Codex's "openai" and magpie's
// "magpie"), and a rollout Codex keeps as plain lines. The id in the
// first line's session_meta, which Codex's thread/list and resume picker
// filter on, is written over byte for byte, the file's time kept, and the
// thread's row follows. Nothing else of the file is read or written, so a
// history of thousands of threads moves in moments where MoveCodexThreads
// would copy and rewrite every one of them; moving it back is the undo,
// and no copy is kept. Rollouts Codex has packed (.zst, a week after
// their last turn) are left on from, and so is a thread written to in the
// last minute, one whose file names another provider, and one outside the
// folder's sessions. It says the ids it moved and the first error it met,
// having gone on past it.
func RelabelCodexThreads(home, from, to string) ([]string, error) {
	if !codexProviderRe.MatchString(from) || !codexProviderRe.MatchString(to) || from == to || len(from) != len(to) {
		return nil, fmt.Errorf("can't relabel Codex's threads from %q to %q", from, to)
	}
	type row struct{ db, id, path string }
	var rows []row
	for _, p := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(p), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		rs, err := db.Query(`SELECT id, rollout_path FROM threads WHERE model_provider = ?`, from)
		if err == nil {
			for rs.Next() {
				var id, path string
				if rs.Scan(&id, &path) == nil && safeID.MatchString(id) {
					rows = append(rows, row{p, id, path})
				}
			}
			rs.Close()
		}
		db.Close()
	}
	if len(rows) == 0 {
		return nil, nil
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	var moved []string
	var first error
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	now := time.Now()
	for _, r := range rows {
		path := filepath.Clean(r.path)
		if !filepath.IsAbs(path) || packed(path) || !strings.Contains(filepath.Base(path), r.id) || !sameDir(codexHomeOf(path), home) {
			continue
		}
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < ActiveWindow {
			continue
		}
		// a packed twin beside it: Codex is packing it now
		if _, err := os.Lstat(path + zstSuffix); err == nil {
			continue
		}
		ok, err := relabelHead(path, from, to, fi.ModTime())
		if err != nil {
			fail(fmt.Errorf("thread %s: %w", r.id, err))
			continue
		}
		if !ok {
			continue
		}
		delete(cache, path)
		if _, err := moveCodexThread(r.db, r.id, to); err != nil {
			fail(fmt.Errorf("thread %s: the file moved, but Codex's %s didn't: %w", r.id, filepath.Base(r.db), err))
			continue
		}
		moved = append(moved, r.id)
	}
	if len(moved) > 0 {
		saveCache()
	}
	return moved, first
}

// relabelHead writes to over from as the model_provider of a rollout's
// first line, its session_meta, in place, and puts the file's time back.
// It says whether the file now names to: false, with no error, for a file
// whose first line isn't a session_meta naming from or to.
func relabelHead(path, from, to string, mod time.Time) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	// the first line, up to 1MB: a session_meta carries the base
	// instructions, tens of KB
	line, err := bufio.NewReaderSize(io.LimitReader(f, 1<<20), 64<<10).ReadBytes('\n')
	if err != nil {
		f.Close()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	end := len(line) - 1
	line = line[:end]
	if gjson.GetBytes(line, "type").String() != "session_meta" {
		f.Close()
		return false, nil
	}
	r := gjson.GetBytes(line, "payload.model_provider")
	if r.Type != gjson.String || r.Index <= 0 || r.Index+len(r.Raw) > end {
		f.Close()
		return false, nil
	}
	if r.Str == to {
		f.Close()
		return true, nil
	}
	want, _ := json.Marshal(from)
	if r.Str != from || r.Raw != string(want) {
		f.Close()
		return false, nil
	}
	val, _ := json.Marshal(to)
	if _, err := f.WriteAt(val, int64(r.Index)); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, os.Chtimes(path, mod, mod)
}

// sameDir says whether a and b are one folder, however spelled: a Codex
// home reached through a link, or in another case on Windows.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// RetagPagedCodexThreads moves the paginated threads of a Codex folder
// (history_mode "paginated", Codex 0.166 on) that its state databases keep
// on the provider from to the provider to, by their rows alone. Codex
// reads such a thread's provider from its row, never from its files: a
// paginated rollout may hold only a suffix of the thread, so its listing
// and its resume take the row's (codex-rs thread-store read_thread,
// "Paginated history may contain only a suffix, so its display metadata
// lives in SQLite"), and resume refuses a provider other than the one
// Codex is on ("This chat uses openai, but your current provider is
// magpie. Switch to openai to continue", Parry_Moody and Cyrus on
// Discord). The file moves (RelabelCodexThreads, MovePackedCodexThreads)
// left every packed one older than 30 days, and every one past a sync's
// budget, on the provider it was started on; a row is a few bytes, so
// every one moves at once, of any age and any id's length, no file read,
// written or copied, and moving them back is the undo. A thread a Codex
// has open, whose writer would put its provider back, and one written to
// in the last minute stay for the next call, as do a database without
// history_mode (an older Codex, whose threads go by their files) and a
// legacy thread. It says the ids it moved and the first error it met,
// having gone on past it.
func RetagPagedCodexThreads(home, from, to string) ([]string, error) {
	return retagPagedCodexThreads(home, from, to, nil)
}

// RetagPagedCodexThreadIDs is RetagPagedCodexThreads for those of the
// threads ids still on from: to undo a move of those threads alone.
func RetagPagedCodexThreadIDs(home, from, to string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return retagPagedCodexThreads(home, from, to, ids)
}

func retagPagedCodexThreads(home, from, to string, only []string) ([]string, error) {
	if !codexProviderRe.MatchString(from) || !codexProviderRe.MatchString(to) || from == to {
		return nil, fmt.Errorf("can't move Codex's threads from %q to %q", from, to)
	}
	var moved []string
	var first error
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, p := range codexStateDBs(home) {
		ids, err := retagPaged(home, p, from, to, only)
		moved = append(moved, ids...)
		if err != nil {
			fail(fmt.Errorf("Codex's %s: %w", filepath.Base(p), err))
		}
	}
	return moved, first
}

// retagPaged is RetagPagedCodexThreads for one state database, a chunk of
// threads held and written in one transaction at a time; with only, those
// of its ids alone.
func retagPaged(home, path, from, to string, only []string) ([]string, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_pragma=busy_timeout(5000)"}
	if len(u.Path) >= 2 && u.Path[1] == ':' {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('threads') WHERE name IN ('id','model_provider','history_mode','rollout_path')`).Scan(&n); err != nil || n != 4 {
		return nil, nil
	}
	type row struct{ id, path string }
	var rows []row
	rs, err := db.Query(`SELECT id, rollout_path FROM threads WHERE model_provider = ? AND history_mode = 'paginated'`, from)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var r row
		if rs.Scan(&r.id, &r.path) == nil && safeID.MatchString(r.id) && (only == nil || contains(only, r.id)) {
			rows = append(rows, r)
		}
	}
	rs.Close()
	var moved []string
	var first error
	now := time.Now()
	const chunk = 200
	for len(rows) > 0 {
		part := rows[:min(chunk, len(rows))]
		rows = rows[len(part):]
		var held []string
		var letGos []func()
		for _, r := range part {
			if r.path != "" {
				if fi, err := os.Stat(filepath.Clean(r.path)); err == nil && now.Sub(fi.ModTime()) < ActiveWindow {
					continue
				}
			}
			letGo, err := holdCodexThread(home, r.id)
			if err != nil {
				if !errors.Is(err, ErrActive) && first == nil {
					first = fmt.Errorf("thread %s: %w", r.id, err)
				}
				continue
			}
			letGos = append(letGos, letGo)
			held = append(held, r.id)
		}
		done, err := retagRows(db, held, from, to)
		forgetPaged()
		for _, letGo := range letGos {
			letGo()
		}
		moved = append(moved, done...)
		if err != nil && first == nil {
			first = err
		}
	}
	return moved, first
}

// retagRows sets the rows ids still on from, and paginated, to to, in one
// transaction, and says which ones it set.
func retagRows(db *sql.DB, ids []string, from, to string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	var done []string
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE threads SET model_provider = ? WHERE id = ? AND model_provider = ? AND history_mode = 'paginated'`, to, id, from)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			done = append(done, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return done, nil
}
