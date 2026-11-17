package sessions

// What was said in every agent's sessions, searched at once (cryingLiu on
// X): the Sessions page's search across agents, and the magpie-sessions
// MCP server's search_sessions, which lets an agent find what was said in
// another agent's session.
//
// Reading every session file for each query is what the one-agent search
// does (SearchSession over the agent's sessions, 8 s at most), and over
// every agent it would be tens of gigabytes a query: this computer's
// Claude Code history is 29 GB in 7,390 files, Codex's 8 GB. So the words
// said in them (the user's and the model's text, about 1% of the bytes)
// are kept in an index, said-v1.db beside sessions.json: an SQLite FTS5
// table that keeps no text, only which words are in which session
// (content='', detail='none'). It is made the first time a search asks,
// in the background, the newest sessions first, and kept up to date from
// then on as sessions.json is: a Claude Code or Codex file that grew is
// read from where the last read ended, any other session again whole
// when its files changed. A search asks the index which sessions hold
// every word, then reads only those, the newest first, with
// SearchSession, for the hit and the words round it; the index is never
// the answer, only which files are worth reading.
//
// Words are a letter-or-digit run, matched by their start ("auth" finds
// "authentication", not "oauth"); Chinese, Japanese and Korean have no
// spaces, so each two characters side by side are a word there. An agent
// magpie may not read (Reads) is left out of the index the next time it
// is brought up to date, and out of every answer at once.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/catalog"
)

// SaidHit is one session a search found, and the words round where.
type SaidHit struct {
	Session Session `json:"session"`
	Hit     Hit     `json:"hit"`
}

// SaidFound is what a search over every agent's sessions found: hits,
// the newest session first. Indexed of Total sessions are in the index;
// fewer while it is first made, when only those were searched. Cut: there
// may be more hits than were looked for. Listing: the sessions to index
// are still being found, the first time on a long history, so Indexed and
// Total don't say yet how much was searched.
type SaidFound struct {
	Hits    []SaidHit `json:"hits"`
	Indexed int       `json:"indexed"`
	Total   int       `json:"total"`
	Cut     bool      `json:"cut,omitempty"`
	Listing bool      `json:"listing,omitempty"`
}

// ErrNoWords: a search with no letter or digit to look for.
var ErrNoWords = errors.New("nothing to search for")

const (
	// saidStale: an index brought up to date this recently is searched
	// as it is (the page searches as the reader types)
	saidStale = 10 * time.Second
	// saidWait is how long a search waits for the index to be brought up
	// to date; one still being made first is searched as far as it got
	saidWait = 2 * time.Second
	// saidChunk is how many words of one read go in one row
	saidChunk = 50_000
	// saidWordMax is the runes of a word kept: a long run (a hash, base64)
	// is found by its start
	saidWordMax = 32
)

// saidSession is a session the index holds: its listing, and what says
// whether its file changed.
type saidSession struct {
	s    Session
	size int64 // the main file's
	sig  string
	// grow: its file is only ever added to (Claude Code's, Codex's), so
	// a longer one is read from where the last read ended
	grow bool
}

type saidIndex struct {
	where func() string // the index's file
	list  func() []saidSession

	mu       sync.Mutex
	db       *sql.DB
	file     string
	running  chan struct{} // closed when the refresh running ends
	at       time.Time     // when the last refresh began
	sessions map[string]saidSession
	indexed  int
	total    int
	listed   bool        // the sessions to index were found once
	quit     atomic.Bool // the refresh running stops at its next session
}

var saidIdx = &saidIndex{where: SaidPath, list: saidSessions}

// SaidPath is the index's file.
func SaidPath() string { return filepath.Join(filepath.Dir(CachePath()), "said-v1.db") }

// SearchSaid finds needle (NeedleOf) in what was said in every agent's
// sessions, or in agent's alone when it is given: at most limit hits.
func SearchSaid(ctx context.Context, needle, agent string, limit int) (SaidFound, error) {
	return saidIdx.search(ctx, needle, agent, limit)
}

// SearchSaidSince is SearchSaid of the sessions last used at since or
// after (`magpie sessions search --days`).
func SearchSaidSince(ctx context.Context, needle, agent string, since time.Time, limit int) (SaidFound, error) {
	return saidIdx.searchSince(ctx, needle, agent, since, limit)
}

// SaidReady brings the index up to date as a search does, but waits for
// it at most wait, not saidWait: a command run once (`magpie sessions
// search`) has no next search to find the rest by. progress, when
// given, is told every second how far it got.
func SaidReady(ctx context.Context, wait time.Duration, progress func(indexed, total int)) error {
	return saidIdx.ready(ctx, wait, progress)
}

func (x *saidIndex) ready(ctx context.Context, wait time.Duration, progress func(indexed, total int)) error {
	db, err := x.open()
	if err != nil {
		return err
	}
	if progress != nil {
		done, gone := make(chan struct{}), make(chan struct{})
		// progress is not told anything once this returns
		defer func() { close(done); <-gone }()
		go func() {
			defer close(gone)
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					x.mu.Lock()
					n, of, listed := x.indexed, x.total, x.listed
					x.mu.Unlock()
					if listed {
						progress(n, of)
					}
				}
			}
		}()
	}
	x.fresh(ctx, db, wait)
	return nil
}

// ForgetSaid closes the index, so a test's HOME can go: a refresh
// running stops after the session it is reading.
func ForgetSaid() {
	saidIdx.quit.Store(true)
	defer saidIdx.quit.Store(false)
	saidIdx.mu.Lock()
	run := saidIdx.running
	saidIdx.mu.Unlock()
	if run != nil {
		<-run
	}
	saidIdx.mu.Lock()
	defer saidIdx.mu.Unlock()
	if saidIdx.db != nil {
		saidIdx.db.Close()
	}
	saidIdx.db, saidIdx.file, saidIdx.sessions, saidIdx.at, saidIdx.indexed, saidIdx.total = nil, "", nil, time.Time{}, 0, 0
	saidIdx.listed = false
}

func (x *saidIndex) open() (*sql.DB, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	file := x.where()
	if x.db != nil && x.file == file {
		return x.db, nil
	}
	if x.db != nil {
		x.db.Close()
		x.db, x.sessions, x.at = nil, nil, time.Time{}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURL(file, "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS files(key TEXT PRIMARY KEY, path TEXT NOT NULL, sig TEXT NOT NULL, upto INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS chunks(rowid INTEGER PRIMARY KEY, key TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS chunks_key ON chunks(key)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS said USING fts5(t, content='', contentless_delete=1, detail='none', tokenize='unicode61 remove_diacritics 2')`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	os.Chmod(file, 0o600)
	x.db, x.file = db, file
	return db, nil
}

// fresh starts bringing the index up to date, unless it was a moment
// ago, and waits for it at most wait.
func (x *saidIndex) fresh(ctx context.Context, db *sql.DB, wait time.Duration) {
	x.mu.Lock()
	run := x.running
	if run == nil && time.Since(x.at) >= saidStale {
		run = make(chan struct{})
		x.running, x.at = run, time.Now()
		go func() {
			defer func() {
				x.mu.Lock()
				x.running = nil
				x.mu.Unlock()
				close(run)
			}()
			x.refresh(db)
		}()
	}
	x.mu.Unlock()
	if run == nil {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-run:
	case <-t.C:
	case <-ctx.Done():
	}
}

// saidRow is what the index holds of a session.
type saidRow struct {
	path, sig string
	upto      int64
}

// refresh brings the index up to date with the sessions listed now: the
// newest first, so a search while it is first made finds the latest.
func (x *saidIndex) refresh(db *sql.DB) {
	list := x.list()
	sort.SliceStable(list, func(i, j int) bool { return list[i].s.Last.After(list[j].s.Last) })
	now := make(map[string]saidSession, len(list))
	for _, ss := range list {
		now[ss.s.Agent+":"+ss.s.ID] = ss
	}
	rows := map[string]saidRow{}
	if rs, err := db.Query(`SELECT key, path, sig, upto FROM files`); err == nil {
		for rs.Next() {
			var k string
			var r saidRow
			if rs.Scan(&k, &r.path, &r.sig, &r.upto) == nil {
				rows[k] = r
			}
		}
		rs.Close()
	}
	done := 0
	for _, ss := range list {
		if r, ok := rows[ss.s.Agent+":"+ss.s.ID]; ok && saidSame(ss, r) {
			done++
		}
	}
	x.mu.Lock()
	x.sessions, x.indexed, x.total, x.listed = now, done, len(list), true
	x.mu.Unlock()
	// gone, or of an agent magpie may no longer read
	for k := range rows {
		if _, ok := now[k]; !ok {
			saidDrop(db, k)
		}
	}
	for _, ss := range list {
		if x.quit.Load() {
			return
		}
		k := ss.s.Agent + ":" + ss.s.ID
		r, had := rows[k]
		if had && saidSame(ss, r) {
			continue
		}
		if !Reads(ss.s.Agent) {
			continue
		}
		var err error
		if had && ss.grow && r.path == ss.s.Path && ss.size >= r.upto {
			err = saidRead(db, k, ss, r.upto, false)
		} else {
			err = saidRead(db, k, ss, 0, true)
		}
		if err == nil {
			x.mu.Lock()
			x.indexed++
			x.mu.Unlock()
		}
	}
}

// saidSame says whether the index holds a session as it is now.
func saidSame(ss saidSession, r saidRow) bool {
	if r.path != ss.s.Path {
		return false
	}
	if ss.grow {
		return r.upto == ss.size || r.upto == lineEnd(ss.s.Path, ss.size)
	}
	return r.sig == ss.sig
}

func saidDrop(db *sql.DB, key string) {
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if saidDropIn(tx, key) == nil {
		tx.Commit()
	}
}

func saidDropIn(tx *sql.Tx, key string) error {
	if _, err := tx.Exec(`DELETE FROM said WHERE rowid IN (SELECT rowid FROM chunks WHERE key = ?)`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE key = ?`, key); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM files WHERE key = ?`, key)
	return err
}

// saidRead reads what was said in a session, from the byte from on when
// it grows (else whole, its rows dropped first), into the index.
func saidRead(db *sql.DB, key string, ss saidSession, from int64, whole bool) error {
	var chunks []string
	words := map[string]struct{}{}
	flush := func() {
		if len(words) == 0 {
			return
		}
		var b strings.Builder
		for w := range words {
			b.WriteString(w)
			b.WriteByte(' ')
		}
		chunks = append(chunks, b.String())
		clear(words)
	}
	add := func(_ bool, p Part) bool {
		if said(p) {
			saidWords(p.Text, func(w string) {
				words[w] = struct{}{}
			})
			if len(words) >= saidChunk {
				flush()
			}
		}
		return true
	}
	upto := ss.size
	var err error
	if ss.grow {
		upto = lineEnd(ss.s.Path, ss.size)
		if whole {
			from = 0
		}
		c := Call{Agent: ss.s.Agent, File: ss.s.Path, From: from, To: upto}
		if ss.s.Agent == "codex" {
			err = codexContentOf(c, codexSaidLine, add)
		} else {
			err = claudeContentOf(c, claudeSaidLine, add)
		}
	} else {
		err = readTranscriptOf(ss.s, nil, add)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	flush()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if whole {
		if err := saidDropIn(tx, key); err != nil {
			return err
		}
	}
	for _, c := range chunks {
		r, err := tx.Exec(`INSERT INTO chunks(key) VALUES(?)`, key)
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO said(rowid, t) VALUES(?, ?)`, id, c); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO files(key, path, sig, upto) VALUES(?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET path = excluded.path, sig = excluded.sig, upto = excluded.upto`, key, ss.s.Path, ss.sig, upto); err != nil {
		return err
	}
	return tx.Commit()
}

// claudeSaidLine: a line that may say something, not a tool's answer
// (most of a session's bytes)
func claudeSaidLine(b []byte) bool { return !bytes.Contains(b, ccToolAnswer) }

var (
	ccToolAnswer = []byte(`"tool_use_id":`)
	cxMessage    = []byte(`"type":"message"`)
)

// codexSaidLine: a message, not a tool call, its output or reasoning
func codexSaidLine(b []byte) bool { return bytes.Contains(b, cxMessage) }

// lineEnd is where the last whole line of a file ends, at or before size:
// a line still being written is read when it is whole.
func lineEnd(path string, size int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return size
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	for end := size; end > 0; {
		start := max(0, end-int64(len(buf)))
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return size
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return start + int64(i) + 1
			}
		}
		end = start
	}
	return 0
}

// cjk is a letter written without spaces between words.
func cjk(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// saidRuns tells each a run of text the index keeps words of: letters
// and digits lowered, a run of Chinese, Japanese or Korean (han) apart.
func saidRuns(text string, each func(run []rune, han bool)) {
	var run []rune
	han := false
	end := func() {
		if len(run) > 0 {
			each(run, han)
			run = run[:0]
		}
	}
	for _, r := range text {
		switch {
		case cjk(r):
			if !han {
				end()
			}
			han = true
			run = append(run, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			if han {
				end()
			}
			han = false
			run = append(run, unicode.ToLower(r))
		default:
			end()
		}
	}
	end()
}

// saidWords tells emit each word of text, as the index keeps them: a run
// of letters and digits (its first saidWordMax runes), and in Chinese,
// Japanese and Korean each two characters side by side, and the last
// one of a run alone, so that one character is found by its start.
func saidWords(text string, emit func(string)) {
	saidRuns(text, func(run []rune, han bool) {
		if !han {
			emit(string(run[:min(len(run), saidWordMax)]))
			return
		}
		for i := 0; i+1 < len(run); i++ {
			emit(string(run[i : i+2]))
		}
		emit(string(run[len(run)-1:]))
	})
}

// saidTerms are the FTS5 queries a needle's words make, each to be held
// by a session: a word by its start, two CJK characters side by side as
// they are, and one standing alone by its start.
func saidTerms(needle string) []string {
	var out []string
	seen := map[string]bool{}
	put := func(q string) {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	saidRuns(needle, func(run []rune, han bool) {
		switch {
		case !han:
			put(`"` + string(run[:min(len(run), saidWordMax)]) + `"*`)
		case len(run) == 1:
			put(`"` + string(run) + `"*`)
		default:
			for i := 0; i+1 < len(run); i++ {
				put(`"` + string(run[i:i+2]) + `"`)
			}
		}
	})
	return out
}

// saidKeys are the sessions whose index holds every term.
func saidKeys(ctx context.Context, db *sql.DB, terms []string) (map[string]bool, error) {
	var keys map[string]bool
	for _, t := range terms {
		rs, err := db.QueryContext(ctx, `SELECT DISTINCT c.key FROM said JOIN chunks c ON c.rowid = said.rowid WHERE said MATCH ?`, t)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// a term FTS5 won't take narrows nothing: the files
			// are read for the words in any case
			continue
		}
		got := map[string]bool{}
		for rs.Next() {
			var k string
			if rs.Scan(&k) == nil && (keys == nil || keys[k]) {
				got[k] = true
			}
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, err
		}
		keys = got
		if len(keys) == 0 {
			break
		}
	}
	return keys, nil
}

// saidReadMost is how long a search reads the files the index named.
const saidReadMost = 6 * time.Second

func (x *saidIndex) search(ctx context.Context, needle, agent string, limit int) (SaidFound, error) {
	return x.searchSince(ctx, needle, agent, time.Time{}, limit)
}

func (x *saidIndex) searchSince(ctx context.Context, needle, agent string, since time.Time, limit int) (SaidFound, error) {
	out := SaidFound{Hits: []SaidHit{}}
	terms := saidTerms(needle)
	if len(terms) == 0 {
		return out, ErrNoWords
	}
	if limit <= 0 {
		limit = 20
	}
	db, err := x.open()
	if err != nil {
		return out, err
	}
	x.fresh(ctx, db, saidWait)
	keys, err := saidKeys(ctx, db, terms)
	if err != nil {
		return out, err
	}
	x.mu.Lock()
	var found []saidSession
	for k := range keys {
		if ss, ok := x.sessions[k]; ok && (agent == "" || ss.s.Agent == agent) && !ss.s.Last.Before(since) && Reads(ss.s.Agent) {
			found = append(found, ss)
		}
	}
	out.Indexed, out.Total, out.Listing = x.indexed, x.total, !x.listed
	x.mu.Unlock()
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].s.Last.Equal(found[j].s.Last) {
			return found[i].s.Last.After(found[j].s.Last)
		}
		return found[i].s.ID < found[j].s.ID
	})
	until := time.Now().Add(saidReadMost)
	for _, ss := range found {
		if len(out.Hits) == limit || time.Now().After(until) {
			out.Cut = true
			break
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		h, ok, err := SearchSession(ctx, ss.s, needle)
		if err == nil && ok {
			out.Hits = append(out.Hits, SaidHit{Session: ss.s, Hit: h})
		}
	}
	return out, nil
}

// saidSessions are every session whose conversation can be read, of the
// agents magpie may read, as the Sessions page lists them.
func saidSessions() []saidSession {
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	files := allFiles()
	refresh(files, files)
	groups := byKey(files, nil)
	copies, branches := ccFamily(files)
	titles := codexTitleOf()
	price := func(string) *catalog.Price { return nil }
	out := make([]saidSession, 0, len(groups))
	for k, fs := range groups {
		if copies[k] != "" || len(fs) == 0 || !HasTranscript(fs[0].agent) || !Reads(fs[0].agent) {
			continue
		}
		s, ok := assembleListed(fs, price, titles)
		if !ok || !readable(s) || s.Path == "" {
			continue
		}
		setBranch(&s, branches[k], groups)
		ss := saidSession{s: s}
		var mod modTime
		var size int64
		for _, f := range fs {
			size += f.size
			mod = max(mod, f.mod)
			if f.path == s.Path {
				ss.size = f.size
			}
		}
		ss.sig = strconv.FormatInt(size, 10) + "/" + strconv.FormatInt(int64(mod), 10)
		ss.grow = saidGrows(s)
		out = append(out, ss)
	}
	return out
}

// saidGrows: a session whose file is only added to, line by line.
func saidGrows(s Session) bool {
	switch s.Agent {
	case "claude", "qoder", "qoder-cn", "codex":
		return !packed(s.Path) && linesFile(s)
	}
	return false
}
