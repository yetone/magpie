package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Hermes stores canonical, non-overlapping input/cache buckets. Reasoning is
// already included in output. session_model_usage includes auxiliary tasks and
// multiple billing routes; sessions supplies only unattributed main-loop usage.
// These tables contain aggregates, not individual API request usage.
func HermesDir() string {
	if home := os.Getenv("HERMES_HOME"); home != "" {
		return filepath.Clean(home)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes")
}

// HermesDirs includes the default store and live named profiles. A selected
// profile home resolves back to its root, as Hermes's profile resolver does.
func HermesDirs() []string {
	root := HermesDir()
	if filepath.Base(filepath.Dir(root)) == "profiles" {
		root = filepath.Dir(filepath.Dir(root))
	}
	dirs := []string{root}
	for _, e := range readDirectory(filepath.Join(root, "profiles")) {
		if !strings.HasPrefix(e.Name(), ".") && (e.IsDir() || e.Type()&os.ModeSymlink != 0) {
			dirs = append(dirs, filepath.Join(root, "profiles", e.Name()))
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		path := canonicalHermesPath(filepath.Join(dir, "state.db"))
		if fileExists(path) && !seen[path] {
			seen[path] = true
			out = append(out, filepath.Dir(path))
		}
	}
	return out
}

func canonicalHermesPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func hermesID(path, sid string) string {
	// Include the store even for the default profile so selecting another home
	// cannot collide with a previously indexed store or a copied session id.
	h := sha256.Sum256([]byte(canonicalHermesPath(path)))
	return "h" + hex.EncodeToString(h[:8]) + "-" + sid
}

type hermesDB struct {
	db                        *sql.DB
	sessions, messages, usage map[string]bool
	revision                  string
}

// WAL writes do not touch the main database file. Include both files in the
// cache identity, including WAL removal or truncation after a checkpoint.
func hermesRevision(path string) string {
	var parts []string
	for _, p := range []string{path, path + "-wal"} {
		info, err := os.Stat(p)
		if err != nil {
			if p == path || !os.IsNotExist(err) {
				return "" // an unknown revision is never reused
			}
			parts = append(parts, "absent")
			continue
		}
		parts = append(parts, strconv.FormatInt(info.Size(), 10), strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
	return strings.Join(parts, ":")
}

type hermesQuery interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// Only fixed table and column names below are interpolated. Values use bind
// parameters; absent columns on older schemas become harmless defaults.
func hermesColumns(db *sql.DB, table string) map[string]bool {
	cols := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid, required, pk int
		var name, kind string
		var def any
		if rows.Scan(&cid, &name, &kind, &required, &def, &pk) == nil {
			cols[name] = true
		}
	}
	return cols
}

func newHermesDB(db *sql.DB) *hermesDB {
	return &hermesDB{
		db:       db,
		sessions: hermesColumns(db, "sessions"),
		messages: hermesColumns(db, "messages"),
		usage:    hermesColumns(db, "session_model_usage"),
	}
}

func hermesExpr(cols map[string]bool, name, fallback string) string {
	if cols[name] {
		return "COALESCE(" + name + ", " + fallback + ")"
	}
	return fallback
}

func hermesSelect(cols map[string]bool, names []string, fallback string) string {
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = hermesExpr(cols, name, fallback)
	}
	return strings.Join(parts, ", ")
}

func hermesFiles() []file {
	var out []file
	for _, dir := range HermesDirs() {
		path := filepath.Join(dir, "state.db")
		db := openDB(path)
		if db == nil {
			continue
		}
		store := newHermesDB(db)
		store.revision = hermesRevision(path)
		if !store.sessions["id"] {
			continue
		}
		messageLast, usageLast := "0", "0"
		if store.messages["session_id"] && store.messages["timestamp"] {
			messageLast = "COALESCE((SELECT MAX(timestamp) FROM messages m WHERE m.session_id = sessions.id), 0)"
		}
		if store.usage["session_id"] && store.usage["last_seen"] {
			usageLast = "COALESCE((SELECT MAX(last_seen) FROM session_model_usage u WHERE u.session_id = sessions.id), 0)"
		}
		query := "SELECT id, " + hermesSelect(
			store.sessions,
			[]string{"started_at", "ended_at", "last_activity_at"},
			"0",
		) + ", " + messageLast + ", " + usageLast + " FROM sessions"
		rows, err := db.Query(query)
		if err != nil {
			continue
		}
		for rows.Next() {
			var sid string
			var start, end, last, messageAt, usageAt float64
			if rows.Scan(&sid, &start, &end, &last, &messageAt, &usageAt) != nil || sid == "" {
				continue
			}
			id := hermesID(path, sid)
			out = append(out, file{
				agent:    "hermes",
				key:      "hermes:" + id,
				path:     path + "#" + url.QueryEscape(sid),
				sid:      sid,
				main:     true,
				mod:      hermesTime(max(start, end, last, messageAt, usageAt)),
				hermes:   store,
				readOnly: true,
			})
		}
		rows.Close()
		if rows.Err() != nil {
			continue
		}
	}
	return out
}

func hermesTime(seconds float64) time.Time {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9))
}

type hermesUsage struct {
	model, upstream, task string
	Tokens
	reasoning, calls int
	last             float64
}

func (h *hermesDB) usages(q hermesQuery, sid string, fallback hermesUsage) ([]hermesUsage, error) {
	if !h.usage["session_id"] || !h.usage["model"] {
		return []hermesUsage{fallback}, nil
	}
	query := "SELECT " + hermesSelect(
		h.usage,
		[]string{"model", "billing_provider", "task"},
		"''",
	) + ", " + hermesSelect(
		h.usage,
		[]string{
			"input_tokens",
			"output_tokens",
			"cache_read_tokens",
			"cache_write_tokens",
			"reasoning_tokens",
			"api_call_count",
			"last_seen",
		},
		"0",
	) + " FROM session_model_usage WHERE session_id = ?"
	rows, err := q.Query(query, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hermesUsage
	for rows.Next() {
		var u hermesUsage
		if err := rows.Scan(
			&u.model,
			&u.upstream,
			&u.task,
			&u.Input,
			&u.Output,
			&u.CacheRead,
			&u.CacheWrite,
			&u.reasoning,
			&u.calls,
			&u.last,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		out = append(out, fallback)
	} else if h.usage["task"] {
		// Auxiliary rows (task != '') are not included in Hermes's sessions
		// counters. Reconcile only against main-loop rows so an aux-only set
		// cannot hide the main total, while already attributed main usage is
		// subtracted exactly once.
		var main hermesUsage
		for _, u := range out {
			if u.task == "" {
				main.Tokens.add(u.Tokens)
				main.reasoning += u.reasoning
				main.calls += u.calls
			}
		}
		residual := fallback
		residual.Input = max(0, fallback.Input-main.Input)
		residual.Output = max(0, fallback.Output-main.Output)
		residual.CacheRead = max(0, fallback.CacheRead-main.CacheRead)
		residual.CacheWrite = max(0, fallback.CacheWrite-main.CacheWrite)
		residual.reasoning = max(0, fallback.reasoning-main.reasoning)
		residual.calls = max(0, fallback.calls-main.calls)
		if !residual.Tokens.zero() || residual.reasoning != 0 || residual.calls != 0 {
			out = append(out, residual)
		}
	}
	return out, nil
}

func (h *hermesDB) metadata(q hermesQuery, sid string, s *state) (hermesUsage, error) {
	var u hermesUsage
	var start, end, last float64
	query := "SELECT " + hermesSelect(
		h.sessions,
		[]string{"title", "cwd", "model", "billing_provider"},
		"''",
	) + ", " + hermesSelect(
		h.sessions,
		[]string{
			"started_at",
			"ended_at",
			"last_activity_at",
			"input_tokens",
			"output_tokens",
			"cache_read_tokens",
			"cache_write_tokens",
			"reasoning_tokens",
			"api_call_count",
		},
		"0",
	) + " FROM sessions WHERE id = ?"
	err := q.QueryRow(query, sid).Scan(&s.Title, &s.Cwd, &u.model, &u.upstream, &start, &end, &last, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite, &u.reasoning, &u.calls)
	s.Title = title(s.Title)
	s.Start, s.Last = hermesTime(start), hermesTime(max(start, end, last))
	u.last = max(start, end, last)
	return u, err
}

type hermesMessage struct {
	role, content, tools string
	at                   float64
	id                   int64
}

func (h *hermesDB) eachMessage(q hermesQuery, sid string, fn func(hermesMessage)) error {
	if !h.messages["session_id"] || !h.messages["role"] {
		return nil
	}
	// Match Hermes's display identity, including NULL versus empty tool fields.
	// Content alone would collapse legitimate repeated turns and tool results.
	type identity struct {
		role, content, tools, toolID, toolName sql.NullString
		at                                     float64
	}
	column := func(name string) string {
		if h.messages[name] {
			return name
		}
		return "NULL"
	}
	query := "SELECT " + strings.Join([]string{
		column("role"), column("content"), column("tool_calls"),
		column("tool_call_id"), column("tool_name"), column("display_kind"),
	}, ", ") + ", " + hermesExpr(h.messages, "timestamp", "0") +
		", " + hermesExpr(h.messages, "active", "1") +
		", " + hermesExpr(h.messages, "id", "rowid") +
		" FROM messages WHERE session_id = ?"
	if h.messages["active"] {
		query += " AND (COALESCE(active, 1) != 0"
		if h.messages["compacted"] {
			query += " OR COALESCE(compacted, 0) != 0"
		}
		query += ")"
	}
	if h.messages["display_metadata"] {
		// Native display projections omit model-only scaffolding. Old schemas
		// have no metadata column, and malformed legacy JSON remains visible.
		query += " AND COALESCE(json_extract(CASE WHEN json_valid(display_metadata) THEN display_metadata ELSE '{}' END, '$.model_only'), 0) = 0"
	}
	query += " ORDER BY " + hermesExpr(h.messages, "id", "rowid")
	rows, err := q.Query(query, sid)
	if err != nil {
		return err
	}
	defer rows.Close()
	type representative struct {
		message hermesMessage
		active  int
	}
	seen := map[identity]int{}
	var messages []representative
	for rows.Next() {
		var key identity
		var m hermesMessage
		var displayKind sql.NullString
		var active int
		if err := rows.Scan(&key.role, &key.content, &key.tools, &key.toolID, &key.toolName, &displayKind, &key.at, &active, &m.id); err != nil {
			return err
		}
		m.role, m.content, m.tools, m.at = key.role.String, key.content.String, key.tools.String, key.at
		if m.role == "user" && key.content.Valid {
			key.content.String = hermesDisplayContent(m.content, displayKind.String)
		}
		if i, ok := seen[key]; ok {
			current := messages[i]
			if active > current.active || active == current.active && m.id > current.message.id {
				messages[i] = representative{m, active}
			}
		} else {
			seen[key] = len(messages)
			messages = append(messages, representative{m, active})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// A copied tail retains its first row's place, even if its selected live
	// representative was inserted after later turns in another generation.
	for _, r := range messages {
		fn(r.message)
	}
	return nil
}

// parseHermes uses a read transaction for a consistent snapshot while the
// writer runs in WAL mode. The connection opens in mode=ro, which does not
// modify state.db itself; SQLite may create an empty state.db-wal and a
// state.db-shm when the store was cleanly closed. Cache identity includes
// the WAL: usage and titles can change without a DB-file mtime change.
func parseHermes(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	if f.hermes == nil {
		return s
	}
	// The connection enforces mode=ro; the transaction needs only a snapshot.
	tx, err := f.hermes.db.BeginTx(context.Background(), nil)
	if err != nil {
		return s
	}
	defer tx.Rollback()
	u, err := f.hermes.metadata(tx, f.sid, s)
	if err != nil {
		return &state{}
	}
	// Activity comes from message gaps, not the entire duration of the session.
	last := s.Last
	s.Last = s.Start
	if err := f.hermes.eachMessage(tx, f.sid, func(m hermesMessage) {
		at := hermesTime(m.at)
		s.saw(at, true)
		d := s.day(dateOf(at))
		switch m.role {
		case "user":
			d.Prompts++
			if s.Title == "" {
				s.Title = title(hermesText(m.content))
			}
		case "assistant":
			d.Replies++
			for _, tool := range hermesTools(m.tools) {
				s.tool(at, tool.Function.Name, "")
			}
		}
	}); err != nil {
		return &state{}
	}
	if last.After(s.Last) {
		s.Last = last
	}
	usage, err := f.hermes.usages(tx, f.sid, u)
	if err != nil {
		return &state{}
	}
	for _, u := range usage {
		at := hermesTime(u.last)
		if at.IsZero() {
			at = s.Last
		}
		s.use(dateOf(at), u.model, u.Tokens)
		if at.After(s.Last) {
			s.Last = at
		}
	}
	s.DBRevision = f.hermes.revision
	return s
}

type hermesTool struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func hermesTools(raw string) []hermesTool {
	var tools []hermesTool
	_ = json.Unmarshal([]byte(raw), &tools)
	return tools
}

func hermesText(raw string) string {
	encoded, ok := strings.CutPrefix(raw, "\x00json:")
	if !ok {
		return raw
	}
	raw = encoded
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &parts) == nil {
		var text []string
		for _, p := range parts {
			if p.Type == "text" {
				text = append(text, p.Text)
			}
		}
		return strings.Join(text, "\n")
	}
	return raw
}
