package sessions

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// Dim alpha (arcships/dim, the `dima` command of npm's dimcode-alpha;
// agent id dim) keeps every session in one SQLite database,
// rust/dimcode.sqlite in its folder, which its daemon writes while it
// runs, in WAL mode. Its store (format 53, dim-app-server's
// storage/sqlite.rs) has, of what magpie reads:
//
//	agent_sessions    a session: session_id, config (JSON, its model at
//	                  $.model.modelId), created_at_ms, leader_session_id
//	                  (a member of a collaboration names its leader's)
//	session_metadata  its title, when one was given or made
//	session_catalog   its folder (cwd), its first words (preview) and its
//	                  last activity (updated_at_ms)
//	timeline_events   its events on each line (branch_id, sequence): kind
//	                  ("input/message", "model/call", "model/usage",
//	                  "step/text", "step/tool-call", …) and body, a JSON
//	                  row: {"encoding":"event","record":{"event":…}}, or
//	                  for the text and thinking deltas of one request
//	                  {"encoding":"output","records":[{"event":…},…]}; an
//	                  event is {type, name, data, create_at (ms)}
//
// A session the user started (no leader) that has a prompt of the user's
// (an input/message with data.user, as Dim's own catalog counts them) is
// a session, every line of it. A member's events are filed under its
// leader: its tools and tokens count there, its task (the leader's
// words) is no prompt of the user's. A fork copies its source's main line
// up to its session/forked event, so those events are its source's and
// counted there only. A model/usage's promptTokens holds the cache reads
// and writes (aimux's input total), so Input is what is left of it; the
// reasoning is inside completionTokens.
//
// Dim sends no session of its own to the model (no header, no
// prompt_cache_key: its session and request ids stay in its own
// recording), so its requests through magpie's gateway can't be matched
// to these sessions.
//
// magpie only reads it, through a read-only connection and in one read
// transaction a session, and never changes, locks or migrates it. A read
// that fails (busy, locked, mid-write) is not "no sessions": the last
// listing stands, cold, and so do its parses. Its sessions are listed
// read only: they are rows Dim's daemon keeps open.

// dimHome is the folder dim-cli's default_home names under the home folder.
const dimHome = ".dimcode-alpha"

// DimDirIn is Dim alpha's folder: env, Dim's DIMCODE_HOME, as given, else
// .dimcode-alpha where join puts it (under the home folder).
func DimDirIn(env string, join func(...string) string) string {
	if d := strings.TrimSpace(env); d != "" {
		return d
	}
	return join(dimHome)
}

// DimDir is Dim alpha's folder on this computer.
func DimDir() string { return dimDirOn(runtime.GOOS, appdir.Getenv) }

// dimDirOn is Dim's folder on goos: its home folder is USERPROFILE on
// Windows and HOME elsewhere, as dim-cli's default_home takes it.
func dimDirOn(goos string, getenv func(string) string) string {
	home := getenv("HOME")
	if goos == "windows" {
		home = getenv("USERPROFILE")
	}
	return DimDirIn(getenv("DIMCODE_HOME"), func(e ...string) string {
		if strings.TrimSpace(home) == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, e...)...)
	})
}

func dimDB() string { return dimDBIn(DimDir()) }

func dimDBIn(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "rust", "dimcode.sqlite")
}

// dimSession is what the listing says of a session.
type dimSession struct {
	db      *sql.DB
	title   string
	cwd     string
	preview string
	model   string
}

// dimLast is the last listing read whole, for a read that fails.
var dimLast struct {
	sync.Mutex
	path  string
	files []file
}

// dimKept is the last listing of path, cold: its parses stand, and
// nothing opens the database for them.
func dimKept(path string) []file {
	dimLast.Lock()
	defer dimLast.Unlock()
	if dimLast.path != path {
		return nil
	}
	out := slices.Clone(dimLast.files)
	for i := range out {
		out[i].cold = true
	}
	return out
}

// dimColumns are a table's columns; an error is a read that failed, not
// a table with none.
func dimColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, required, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&cid, &name, &kind, &required, &def, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// dimNeeds are the columns magpie reads, by table.
var dimNeeds = map[string][]string{
	"agent_sessions":   {"session_id", "config", "created_at_ms", "leader_session_id"},
	"session_metadata": {"session_id", "title"},
	"session_catalog":  {"session_id", "updated_at_ms", "preview", "cwd"},
	"timeline_events":  {"session_id", "branch_id", "sequence", "last_sequence", "kind", "body"},
}

// dimPrompt is the SQL that tells a row is a prompt of the user's: an
// input/message with data.user (Dim's catalog.rs), or the older
// data.text. A body is always JSON: Dim's own indexes on timeline_events
// read every row's through json_extract, so SQLite refuses one that isn't.
const dimPrompt = `(e.kind = 'input/message' AND (json_extract(e.body, '$.record.event.data.user') IS NOT NULL OR json_extract(e.body, '$.record.event.data.text') IS NOT NULL))`

func dimFiles() []file { return dimFilesAt(dimDB()) }

// dimFilesAt are the sessions in Dim's database at path, one file each:
// its path the database's and #<session id>, its size the bytes of its
// events (its members' with them), its time its last activity, and rev
// all that tells it changed.
func dimFilesAt(path string) []file {
	if !Reads("dim") {
		return nil
	}
	if path == "" || !fileExists(path) {
		dimLast.Lock()
		dimLast.path, dimLast.files = "", nil
		dimLast.Unlock()
		return nil
	}
	db := openDB(path)
	if db == nil {
		return dimKept(path)
	}
	for table, need := range dimNeeds {
		cols, err := dimColumns(db, table)
		if err != nil {
			return dimKept(path)
		}
		for _, c := range need {
			if !cols[c] {
				return nil // a store of another form, not read
			}
		}
	}
	out, err := dimList(db, path)
	if err != nil {
		return dimKept(path)
	}
	dimLast.Lock()
	dimLast.path, dimLast.files = path, out
	dimLast.Unlock()
	return slices.Clone(out)
}

func dimList(db *sql.DB, path string) ([]file, error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`WITH r AS (
		SELECT session_id AS id, COALESCE(NULLIF(leader_session_id, ''), session_id) AS root FROM agent_sessions),
	g AS (SELECT r.root, COUNT(*) AS n, SUM(e.last_sequence) AS seq, SUM(octet_length(e.body)) AS size,
			SUM(CASE WHEN r.id = r.root AND ` + dimPrompt + ` THEN 1 ELSE 0 END) AS prompts
		FROM timeline_events e JOIN r ON r.id = e.session_id GROUP BY r.root),
	c AS (SELECT r.root, MAX(c.updated_at_ms) AS up FROM session_catalog c JOIN r ON r.id = c.session_id GROUP BY r.root)
	SELECT s.session_id, g.n, g.seq, g.size, COALESCE(c.up, 0), s.created_at_ms,
		COALESCE(m.title, ''), COALESCE(k.cwd, ''), COALESCE(k.preview, ''),
		COALESCE(CASE WHEN json_valid(s.config) THEN json_extract(s.config, '$.model.modelId') END, '')
	FROM g JOIN agent_sessions s ON s.session_id = g.root
	LEFT JOIN c ON c.root = g.root
	LEFT JOIN session_metadata m ON m.session_id = g.root
	LEFT JOIN session_catalog k ON k.session_id = g.root
	WHERE g.prompts > 0 AND COALESCE(s.leader_session_id, '') = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []file{}
	for rows.Next() {
		var id, name, cwd, preview, model string
		var n, seq, size, up, created int64
		if err := rows.Scan(&id, &n, &seq, &size, &up, &created, &name, &cwd, &preview, &model); err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		out = append(out, file{
			agent:    "dim",
			key:      "dim:" + id,
			path:     path + "#" + url.QueryEscape(id),
			main:     true,
			size:     size,
			mod:      modOf(catpawTime(max(up, created))),
			readOnly: true,
			more: &fileMore{
				sid: id,
				dim: &dimSession{db: db, title: name, cwd: cwd, preview: preview, model: model},
				rev: strings.Join([]string{strconv.FormatInt(n, 10), strconv.FormatInt(seq, 10), strconv.FormatInt(size, 10),
					strconv.FormatInt(up, 10), name, cwd}, "|"),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// dimEach calls each with every event a row's body holds: an event row's
// one, or an output row's deltas in order.
func dimEach(body string, each func(gjson.Result)) {
	b := gjson.Parse(body)
	if b.Get("encoding").String() == "output" {
		b.Get("records").ForEach(func(_, r gjson.Result) bool {
			each(r.Get("event"))
			return true
		})
		return
	}
	each(b.Get("record.event"))
}

// dimPromptText is a prompt's words: its user content's text blocks, but
// for what a capability put after the first (a <dim-…> element: a skill,
// a hook's context, a rule; dim-agent-types' markup.rs), or an older
// input's text.
func dimPromptText(data gjson.Result) string {
	u := data.Get("user")
	if !u.Exists() {
		return data.Get("text").String()
	}
	var parts []string
	i := 0
	u.Get("content").ForEach(func(_, b gjson.Result) bool {
		if text := b.Get("text").String(); b.Get("type").String() == "text" && (i == 0 || !dimRuntime(text)) {
			parts = append(parts, text)
		}
		i++
		return true
	})
	return strings.Join(parts, "\n")
}

// dimMarkers are the elements Dim's runtime writes for its own content
// (dim-agent-types' markup.rs MARKERS).
var dimMarkers = []string{
	"dim-agent-approval", "dim-agent-approval-ended", "dim-agent-message", "dim-agent-report",
	"dim-branch-ended", "dim-branch-idle", "dim-branch-line", "dim-branch-lines", "dim-branch-message",
	"dim-branch-parent", "dim-digest", "dim-event", "dim-event-guide", "dim-goal", "dim-hooks",
	"dim-notification", "dim-rule", "dim-scheduler", "dim-shell-input", "dim-skill", "dim-state",
	"dim-summary", "dim-workflow",
}

// dimRuntime is whether a text block opens one of those elements, as
// markup.rs' element tells it: the name whole, then '>' or a space.
func dimRuntime(text string) bool {
	open, ok := strings.CutPrefix(text, "<")
	if !ok {
		return false
	}
	for _, m := range dimMarkers {
		if rest, ok := strings.CutPrefix(open, m); ok && rest != "" {
			if r, _ := utf8.DecodeRuneInString(rest); r == '>' || unicode.IsSpace(r) {
				return true
			}
		}
	}
	return false
}

func dimPromptOf(ev gjson.Result) (string, bool) {
	data := ev.Get("data")
	if !data.Get("user").Exists() && !data.Get("text").Exists() {
		return "", false // what a capability put in, not the user's words
	}
	return dimPromptText(data), true
}

// dimTokens is a model/usage's usage in magpie's terms.
func dimTokens(u gjson.Result) Tokens {
	read, write := max(0, int(u.Get("cacheReadTokens").Int())), max(0, int(u.Get("cacheWriteTokens").Int()))
	return Tokens{
		Input:      max(0, int(u.Get("promptTokens").Int())-read-write),
		Output:     max(0, int(u.Get("completionTokens").Int())),
		CacheRead:  read,
		CacheWrite: write,
	}
}

// dimEvents are the kinds a session's summary reads.
const dimEvents = `('input/message', 'model/call', 'model/usage', 'step/text', 'step/tool-call')`

// dimRows are a session's events of kinds, and its members' with them
// when members, each line's after the copy a fork made of its source, in
// time order.
func dimRows(tx *sql.Tx, sid, kinds string, members bool) (*sql.Rows, error) {
	in := `SELECT ?1`
	if members {
		in = `SELECT session_id FROM agent_sessions WHERE session_id = ?1 OR leader_session_id = ?1`
	}
	return tx.Query(`WITH s(id) AS (`+in+`),
	f(id, at) AS (SELECT session_id, MAX(sequence) FROM timeline_events
		WHERE kind = 'session/forked' AND branch_id = 'main' AND session_id IN (SELECT id FROM s) GROUP BY session_id)
	SELECT e.session_id, e.kind, e.body FROM timeline_events e LEFT JOIN f ON f.id = e.session_id AND e.branch_id = 'main'
	WHERE e.session_id IN (SELECT id FROM s) AND e.kind IN `+kinds+` AND (f.at IS NULL OR e.sequence > f.at)
	ORDER BY COALESCE(json_extract(e.body, '$.record.event.create_at'), json_extract(e.body, '$.started_at'), 0), e.session_id, e.branch_id, e.sequence`, sid)
}

// parseDim reads one session and its members' in a read transaction, a
// snapshot while Dim writes on. A read that fails keeps the last parse.
func parseDim(f file, old *state) *state {
	failed := func() *state {
		if old != nil {
			return old
		}
		return &state{}
	}
	ds := f.m().dim
	if ds == nil {
		return failed()
	}
	sid := f.m().sid
	tx, err := ds.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return failed()
	}
	defer tx.Rollback()
	rows, err := dimRows(tx, sid, dimEvents, true)
	if err != nil {
		return failed()
	}
	defer rows.Close()
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	type call struct {
		date  string
		model string
		t     Tokens
	}
	var order []string
	calls := map[string]*call{}
	models := map[string]string{} // request → the model it asked
	first := ""
	replied := true
	for rows.Next() {
		var conv, kind, body string
		if rows.Scan(&conv, &kind, &body) != nil {
			return failed()
		}
		own := conv == sid
		dimEach(body, func(ev gjson.Result) {
			t := catpawTime(ev.Get("create_at").Int())
			data := ev.Get("data")
			s.saw(t, own)
			// a call's own events (a nested run's) are no words between
			// the user and the session's model; their tools and tokens are
			nested := ev.Get("parent_call_id").Exists()
			switch kind {
			case "input/message":
				text, ok := dimPromptOf(ev)
				if !own || nested || !ok || strings.TrimSpace(text) == "" {
					return
				}
				s.day(dateOf(t)).Prompts++
				if first == "" {
					first = title(text)
				}
				replied = false
			case "step/text":
				if own && !nested && !replied && strings.TrimSpace(data.Get("delta").String()) != "" {
					s.day(dateOf(t)).Replies++
					replied = true
				}
			case "step/tool-call":
				data.Get("calls").ForEach(func(_, c gjson.Result) bool {
					s.tool(t, c.Get("name").String(), "")
					return true
				})
			case "model/call":
				if id, m := data.Get("request_id").String(), data.Get("request.modelId").String(); id != "" && m != "" {
					models[conv+"\x00"+id] = m
				}
			case "model/usage":
				u := dimTokens(data.Get("usage"))
				if u.zero() {
					return
				}
				key := conv + "\x00" + data.Get("request_id").String()
				if data.Get("request_id").String() == "" {
					key = conv + "\x00#" + ev.Get("id").String()
				}
				c := calls[key]
				if c == nil {
					c = &call{date: dateOf(t)}
					calls[key] = c
					order = append(order, key)
				}
				// one usage a request; were it said twice, the larger
				if u.sum() > c.t.sum() {
					c.t = u
				}
				c.model = cmp.Or(models[key], c.model)
			}
		})
	}
	if rows.Err() != nil {
		return failed()
	}
	for _, k := range order {
		c := calls[k]
		s.use(c.date, cmp.Or(c.model, ds.model), c.t)
	}
	s.Title = cmp.Or(title(ds.title), title(ds.preview), first)
	s.Cwd = ds.cwd
	s.own().First = cmp.Or(first, title(ds.preview))
	return s
}

// dimTranscript reads a session's main line as Dim shows it, in one read
// transaction: the prompts, the replies, the thinking, and each tool with
// what it was given and what it answered. A member's own session and the
// other lines are not said.
func dimTranscript(s Session, add func(bool, Part) bool) error {
	i := strings.LastIndex(s.Path, "#")
	if i < 0 || s.ID == "" {
		return ErrNoTranscript
	}
	db, err := provider.OpenReadOnly(s.Path[:i])
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT kind, body FROM timeline_events WHERE session_id = ?1 AND branch_id = 'main'
		AND kind IN ('input/message', 'step/text', 'step/thinking', 'step/tool-call', 'step/tool-completed', 'step/tool-result')
		ORDER BY sequence`, s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var said strings.Builder // the deltas of the text or thinking being said
	sayKind, sayReq := "", ""
	more := true
	flush := func() {
		if said.Len() > 0 && more && strings.TrimSpace(said.String()) != "" {
			more = add(false, Part{Role: "assistant", Kind: sayKind, Text: said.String()})
		}
		said.Reset()
		sayKind, sayReq = "", ""
	}
	names := map[string]string{} // call id → tool
	answered := map[string]bool{}
	result := func(id string, r gjson.Result) {
		if id == "" || answered[id] || !more {
			return
		}
		answered[id] = true
		out := resultText(json.RawMessage(r.Get("content").Raw))
		if !r.Get("content").Exists() {
			out = r.String()
		}
		if strings.TrimSpace(out) != "" {
			more = add(true, Part{Role: "tool", Kind: "tool_result", Name: names[id], Text: out})
		}
	}
	for rows.Next() && more {
		var kind, body string
		if err := rows.Scan(&kind, &body); err != nil {
			return err
		}
		dimEach(body, func(ev gjson.Result) {
			if !more || ev.Get("parent_call_id").Exists() {
				return // a call's own events: what it answered is said
			}
			data := ev.Get("data")
			switch kind {
			case "step/text", "step/thinking":
				k := "text"
				if kind == "step/thinking" {
					k = "thinking"
				}
				if k != sayKind || data.Get("request_id").String() != sayReq {
					flush()
					sayKind, sayReq = k, data.Get("request_id").String()
				}
				said.WriteString(data.Get("delta").String())
				return
			}
			flush()
			switch kind {
			case "input/message":
				if text, ok := dimPromptOf(ev); ok && strings.TrimSpace(text) != "" {
					more = add(true, textPart("user", text))
				}
			case "step/tool-call":
				data.Get("calls").ForEach(func(_, c gjson.Result) bool {
					name := c.Get("name").String()
					names[c.Get("id").String()] = name
					in := pretty(json.RawMessage(c.Get("input").Raw))
					if strings.TrimSpace(in) == "" || in == "{}" {
						in = name
					}
					more = add(false, Part{Role: "assistant", Kind: "tool_use", Name: name, Text: in})
					return more
				})
			case "step/tool-completed":
				result(data.Get("call_id").String(), data.Get("result"))
			case "step/tool-result":
				data.Get("results").ForEach(func(_, r gjson.Result) bool {
					result(r.Get("call_id").String(), r.Get("result"))
					return more
				})
			}
		})
	}
	flush()
	if !more {
		return nil
	}
	return rows.Err()
}
