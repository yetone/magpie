package artifact

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "github.com/yetone/magpie/internal/sqlitedb"
)

// The db capability (docs/subsystems/artifacts.md, "Page runtime"): each
// artifact's own store of JSON documents at slash-separated paths, as
// claude.ai's db capability keeps them, so a page written for it runs
// here. It is data.db in the artifact's folder, beside its versions:
// made at the first write, kept across versions, and gone with the
// artifact. The page reaches it as its viewer; an agent, through
// artifact_data, reaches all of it.

const (
	dataName = "data.db"
	// the limits claude.ai's store holds a page to
	maxDocBytes  = 256 << 10
	maxDocDepth  = 32
	maxSegments  = 16
	maxSegBytes  = 200
	maxPathBytes = 1000
	maxFilters   = 10
	maxInValues  = 30
	maxLimit     = 1000
	// leases are clamped to these
	minLease, maxLease, defaultLease = time.Second, 10 * time.Minute, 30 * time.Second
)

// how many documents, and bytes of them, a store holds at most; a test
// lowers them
var (
	maxDocs      int64 = 25000
	maxDataBytes int64 = 256 << 20
)

// DBError is a store call's failure, by the code claude.ai's runtime gives
// a page (invalid_argument, resource_exhausted, quota_exceeded,
// unavailable).
type DBError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *DBError) Error() string { return e.Code + ": " + e.Message }

func invalid(f string, a ...any) error {
	return &DBError{Code: "invalid_argument", Message: fmt.Sprintf(f, a...)}
}

func quota(f string, a ...any) error {
	return &DBError{Code: "quota_exceeded", Message: fmt.Sprintf(f, a...)}
}

// checkSegment says why s can't be a path segment, or nil.
func checkSegment(s string) error {
	if s == "" {
		return errors.New("a path has no empty segment")
	}
	if s == "." || s == ".." {
		return fmt.Errorf("%q can't be a segment", s)
	}
	if len(s) > maxSegBytes {
		return fmt.Errorf("a segment is at most %d bytes", maxSegBytes)
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.~:@+", c)) {
			return fmt.Errorf("segment %q: segments use letters, digits and _ - . ~ : @ + only", s)
		}
	}
	return nil
}

// CheckPath is a store path's segments; doc says whether it names a
// document (an even number of segments) or a collection (odd).
func CheckPath(p string, doc bool) ([]string, error) {
	if len(p) > maxPathBytes {
		return nil, invalid("a path is at most %d bytes", maxPathBytes)
	}
	segs := strings.Split(p, "/")
	if p == "" {
		segs = nil
	}
	if len(segs) == 0 || len(segs) > maxSegments {
		return nil, invalid("a path has 1 to %d segments; %q has %d", maxSegments, p, len(segs))
	}
	for _, s := range segs {
		if err := checkSegment(s); err != nil {
			return nil, invalid("%v", err)
		}
	}
	switch {
	case doc && len(segs)%2 != 0:
		return nil, invalid("%q has %d segments, so it is a collection: a document's path has an even number", p, len(segs))
	case !doc && len(segs)%2 == 0:
		return nil, invalid("%q has %d segments, so it is a document: a collection's path has an odd number", p, len(segs))
	}
	return segs, nil
}

// Doc is one document as the store gives it.
type Doc struct {
	Path    string         `json:"path"`
	ID      string         `json:"id"`
	Data    map[string]any `json:"data"`
	Version int64          `json:"version"`
	Updated time.Time      `json:"updated"`
}

// Filter is one where() of a query.
type Filter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// Query is a read of one collection.
type Query struct {
	Collection string   `json:"collection"`
	Where      []Filter `json:"where,omitempty"`
	OrderBy    string   `json:"orderBy,omitempty"`
	Desc       bool     `json:"desc,omitempty"`
	Limit      int      `json:"limit,omitempty"`
}

// DB is one artifact's store, as one viewer reaches it.
type DB struct {
	id    string
	rules *ruleSet
	who   Viewer
}

// OpenDB is artifact id's store as viewer who reaches it from version n
// (0: the newest), under the rules the db declaration in force there
// sets (DBRules). It needs no db declaration: an agent reads and seeds
// the store of a page that declares one later.
func OpenDB(id string, n int, who Viewer) (*DB, error) {
	a, err := readMeta(id)
	if err != nil {
		return nil, err
	}
	if a.Version(n) == nil {
		return nil, ErrNotFound
	}
	return &DB{id: id, rules: compileRules(DBRules(a, n)), who: who}, nil
}

// DBRules is the db declaration in force for artifact a's version n (0:
// the newest): that version's, or the newest's when it declares none.
func DBRules(a *Artifact, n int) *DBConfig {
	for _, v := range []*Version{a.Version(n), a.Latest()} {
		if v == nil || !v.Capabilities.Has("db") {
			continue
		}
		c, err := ParseDBConfig(v.Capabilities["db"])
		if err == nil {
			return c
		}
	}
	return nil
}

var (
	handlesMu sync.Mutex
	handles   = map[string]*sql.DB{}
)

// handle is the artifact's open data.db; create makes it when there is none.
func handle(id string, create bool) (*sql.DB, error) {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	if h := handles[id]; h != nil {
		return h, nil
	}
	// a store is never made, or opened again, for an artifact gone
	if _, err := os.Stat(filepath.Join(folder(id), metaName)); err != nil {
		if create {
			return nil, ErrNotFound
		}
		return nil, nil
	}
	p := filepath.Join(folder(id), dataName)
	if !create {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
	}
	slashed := filepath.ToSlash(p)
	if len(slashed) >= 2 && slashed[1] == ':' {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed, RawQuery: "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"}
	h, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(1)
	if _, err := h.Exec(`CREATE TABLE IF NOT EXISTS docs (
		path TEXT PRIMARY KEY,
		coll TEXT NOT NULL,
		body TEXT NOT NULL,
		version INTEGER NOT NULL,
		updated INTEGER NOT NULL,
		lease_holder TEXT NOT NULL DEFAULT '',
		lease_until INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS docs_coll ON docs(coll);
	CREATE TABLE IF NOT EXISTS totals (k INTEGER PRIMARY KEY CHECK (k = 0), docs INTEGER NOT NULL, bytes INTEGER NOT NULL);
	INSERT OR IGNORE INTO totals VALUES (0, 0, 0);`); err != nil {
		h.Close()
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	os.Chmod(p, 0o600)
	handles[id] = h
	return h, nil
}

// closeDB lets go of the artifact's data.db, before its folder goes.
func closeDB(id string) {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	if h := handles[id]; h != nil {
		h.Close()
		delete(handles, id)
	}
}

// CloseDBs lets go of every open data.db. Windows can't remove a folder
// while a file in it is open, so a test's folder needs this first.
func CloseDBs() {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	for id, h := range handles {
		h.Close()
		delete(handles, id)
	}
}

func unavailable(err error) error {
	return &DBError{Code: "unavailable", Message: err.Error()}
}

func readDoc(path string, body string, version, updated int64) (*Doc, error) {
	d := &Doc{Path: path, ID: path[strings.LastIndex(path, "/")+1:], Version: version, Updated: time.UnixMilli(updated).UTC()}
	if err := json.Unmarshal([]byte(body), &d.Data); err != nil {
		return nil, fmt.Errorf("%s doesn't read: %w", path, err)
	}
	return d, nil
}

// Get is the document at p, nil when there is none or the viewer can't
// see it: the two are the same to the viewer.
func (db *DB) Get(p string) (*Doc, error) {
	segs, err := CheckPath(p, true)
	if err != nil {
		return nil, err
	}
	if !db.rules.can(db.who, segs, false) {
		return nil, nil
	}
	h, err := handle(db.id, false)
	if err != nil || h == nil {
		return nil, err
	}
	var body string
	var version, updated int64
	err = h.QueryRow(`SELECT body, version, updated FROM docs WHERE path = ?`, p).Scan(&body, &version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable(err)
	}
	return readDoc(p, body, version, updated)
}

// Run is the documents q matches that the viewer can see, in its order.
func (db *DB) Run(q Query) ([]*Doc, error) {
	segs, err := CheckPath(q.Collection, false)
	if err != nil {
		return nil, err
	}
	if len(q.Where) > maxFilters {
		return nil, invalid("a query has at most %d filters", maxFilters)
	}
	for _, f := range q.Where {
		if err := checkFilter(f); err != nil {
			return nil, err
		}
	}
	if q.Limit < 0 || q.Limit > maxLimit {
		return nil, invalid("a limit is 1 to %d", maxLimit)
	}
	h, err := handle(db.id, false)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return []*Doc{}, nil
	}
	rows, err := h.Query(`SELECT path, body, version, updated FROM docs WHERE coll = ? ORDER BY path`, q.Collection)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	out := []*Doc{}
	for rows.Next() {
		var p, body string
		var version, updated int64
		if err := rows.Scan(&p, &body, &version, &updated); err != nil {
			return nil, unavailable(err)
		}
		if !db.rules.can(db.who, append(segs[:len(segs):len(segs)], p[strings.LastIndex(p, "/")+1:]), false) {
			continue
		}
		d, err := readDoc(p, body, version, updated)
		if err != nil {
			continue
		}
		if matches(d.Data, q.Where) {
			out = append(out, d)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	if q.OrderBy != "" {
		sort.SliceStable(out, func(i, j int) bool {
			a, aok := out[i].Data[q.OrderBy]
			b, bok := out[j].Data[q.OrderBy]
			switch {
			case !aok || !bok:
				// a document missing the field sorts last, either way
				return aok && !bok
			case q.Desc:
				return compare(b, a) < 0
			}
			return compare(a, b) < 0
		})
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

var filterOps = []string{"==", "!=", "<", "<=", ">", ">=", "in", "not-in", "array-contains"}

func checkFilter(f Filter) error {
	if f.Field == "" {
		return invalid("a filter names a field")
	}
	ok := false
	for _, o := range filterOps {
		ok = ok || f.Op == o
	}
	if !ok {
		return invalid("%q isn't an operator; they are %s", f.Op, strings.Join(filterOps, " "))
	}
	if f.Op == "in" || f.Op == "not-in" {
		arr, isArr := f.Value.([]any)
		if !isArr || len(arr) > maxInValues {
			return invalid("%s takes an array of at most %d values", f.Op, maxInValues)
		}
	}
	return nil
}

func matches(data map[string]any, where []Filter) bool {
	for _, f := range where {
		v, has := data[f.Field]
		switch f.Op {
		case "==":
			if !has || compare(v, f.Value) != 0 {
				return false
			}
		case "!=":
			if !has || compare(v, f.Value) == 0 {
				return false
			}
		case "<", "<=", ">", ">=":
			if !has || kind(v) != kind(f.Value) {
				return false
			}
			c := compare(v, f.Value)
			if f.Op == "<" && c >= 0 || f.Op == "<=" && c > 0 || f.Op == ">" && c <= 0 || f.Op == ">=" && c < 0 {
				return false
			}
		case "in", "not-in":
			in := false
			for _, x := range f.Value.([]any) {
				in = in || has && compare(v, x) == 0
			}
			if !has || in != (f.Op == "in") {
				return false
			}
		case "array-contains":
			arr, ok := v.([]any)
			if !ok {
				return false
			}
			in := false
			for _, x := range arr {
				in = in || compare(x, f.Value) == 0
			}
			if !in {
				return false
			}
		}
	}
	return true
}

// kind orders JSON values as Firestore does: null, booleans, numbers,
// strings, arrays, objects.
func kind(v any) int {
	switch v.(type) {
	case nil:
		return 0
	case bool:
		return 1
	case float64, json.Number, int, int64:
		return 2
	case string:
		return 3
	case []any:
		return 4
	}
	return 5
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return math.NaN()
}

// compare orders two JSON values: by kind, then by value.
func compare(a, b any) int {
	ka, kb := kind(a), kind(b)
	if ka != kb {
		return ka - kb
	}
	switch ka {
	case 1:
		x, y := a.(bool), b.(bool)
		if x == y {
			return 0
		}
		if !x {
			return -1
		}
		return 1
	case 2:
		x, y := num(a), num(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case 3:
		return strings.Compare(a.(string), b.(string))
	case 4:
		x, y := a.([]any), b.([]any)
		for i := 0; i < len(x) && i < len(y); i++ {
			if c := compare(x[i], y[i]); c != 0 {
				return c
			}
		}
		return len(x) - len(y)
	case 5:
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Compare(x, y)
	}
	return 0
}

// checkBody says why data can't be a document's body.
func checkBody(data map[string]any) ([]byte, error) {
	if data == nil {
		return nil, invalid("a document's body is a JSON object")
	}
	if depth(data) > maxDocDepth {
		return nil, invalid("a document is at most %d levels deep", maxDocDepth)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, invalid("the body isn't plain JSON: %v", err)
	}
	if len(b) > maxDocBytes {
		return nil, invalid("the document is %d bytes; the most is %d (256 KiB)", len(b), maxDocBytes)
	}
	if !utf8.Valid(b) {
		return nil, invalid("the body isn't UTF-8")
	}
	return b, nil
}

func depth(v any) int {
	switch x := v.(type) {
	case map[string]any:
		d := 0
		for _, e := range x {
			d = max(d, depth(e))
		}
		return d + 1
	case []any:
		d := 0
		for _, e := range x {
			d = max(d, depth(e))
		}
		return d + 1
	}
	return 0
}

// merge puts patch into base, nested objects recursively; anything else,
// arrays included, replaces the field.
func merge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		pm, pok := v.(map[string]any)
		bm, bok := out[k].(map[string]any)
		if pok && bok {
			out[k] = merge(bm, pm)
			continue
		}
		out[k] = v
	}
	return out
}

// Change is a write the store made: the paths it touched.
type Change struct {
	Artifact string
	Paths    []string
	// Version, when set, is a new version published: no data changed
	Version int
	// Comments: the artifact's comments changed, and no data did
	Comments bool
}

var (
	watchMu  sync.Mutex
	watchers = map[string]map[chan Change]bool{}
)

// Watch gives the store's writes to artifact id as they are made in this
// process; another process's (the CLI's) show in DataVersion.
func Watch(id string) (<-chan Change, func()) {
	ch := make(chan Change, 64)
	watchMu.Lock()
	if watchers[id] == nil {
		watchers[id] = map[chan Change]bool{}
	}
	watchers[id][ch] = true
	watchMu.Unlock()
	return ch, func() {
		watchMu.Lock()
		delete(watchers[id], ch)
		if len(watchers[id]) == 0 {
			delete(watchers, id)
		}
		watchMu.Unlock()
	}
}

func notify(id string, paths ...string) { tell(Change{Artifact: id, Paths: paths}) }

// notifyVersion tells id's watchers that version n is published.
func notifyVersion(id string, n int) { tell(Change{Artifact: id, Version: n}) }

func tell(c Change) {
	watchMu.Lock()
	defer watchMu.Unlock()
	for ch := range watchers[c.Artifact] {
		select {
		case ch <- c:
		default:
			// a watcher that fell behind reads everything again (and
			// the newest version, from the store)
			select {
			case ch <- Change{Artifact: c.Artifact}:
			default:
			}
		}
	}
}

// DataVersion changes when another connection, in this process or
// another, has written artifact id's store; 0 when it has none.
func DataVersion(id string) int64 {
	h, err := handle(id, false)
	if err != nil || h == nil {
		return 0
	}
	var n int64
	h.QueryRow(`PRAGMA data_version`).Scan(&n)
	return n
}

// Op is one write of a batch: set, update or delete.
type Op struct {
	Op   string         `json:"op"`
	Path string         `json:"path"`
	Data map[string]any `json:"data,omitempty"`
}

// Set writes the whole document at p, making it if there is none.
func (db *DB) Set(p string, data map[string]any) error {
	return db.Batch([]Op{{Op: "set", Path: p, Data: data}})
}

// Update merges data into the document at p, which must be there.
func (db *DB) Update(p string, data map[string]any) error {
	return db.Batch([]Op{{Op: "update", Path: p, Data: data}})
}

// Delete takes the document at p away; one not there is done. Documents
// nested below it stay.
func (db *DB) Delete(p string) error {
	return db.Batch([]Op{{Op: "delete", Path: p}})
}

// Batch makes the writes together, or none of them.
func (db *DB) Batch(ops []Op) error {
	if len(ops) == 0 {
		return nil
	}
	if len(ops) > 500 {
		return invalid("a batch has at most 500 writes")
	}
	type prepared struct {
		op   Op
		body []byte
	}
	var ready []prepared
	for _, o := range ops {
		segs, err := CheckPath(o.Path, true)
		if err != nil {
			return err
		}
		if !db.rules.can(db.who, segs, true) {
			return invalid("this viewer can't write %s", o.Path)
		}
		p := prepared{op: o}
		switch o.Op {
		case "set":
			if p.body, err = checkBody(o.Data); err != nil {
				return err
			}
		case "update":
			if o.Data == nil {
				return invalid("an update's body is a JSON object")
			}
		case "delete":
		default:
			return invalid("%q isn't a write; they are set, update and delete", o.Op)
		}
		ready = append(ready, p)
	}
	if _, err := readMeta(db.id); err != nil {
		return err
	}
	h, err := handle(db.id, true)
	if err != nil {
		return unavailable(err)
	}
	tx, err := h.Begin()
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	// what the store holds, kept beside it so a write never reads every
	// document to know
	var docs, size int64
	if err := tx.QueryRow(`SELECT docs, bytes FROM totals`).Scan(&docs, &size); err != nil {
		return unavailable(err)
	}
	now := time.Now().UnixMilli()
	var paths []string
	for _, p := range ready {
		o := p.op
		coll := o.Path[:strings.LastIndex(o.Path, "/")]
		var oldBody string
		var version int64
		err := tx.QueryRow(`SELECT body, version FROM docs WHERE path = ?`, o.Path).Scan(&oldBody, &version)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}
		switch o.Op {
		case "delete":
			if !exists {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM docs WHERE path = ?`, o.Path); err != nil {
				return unavailable(err)
			}
			docs, size = docs-1, size-int64(len(oldBody))
			paths = append(paths, o.Path)
			continue
		case "update":
			if !exists {
				return invalid("update needs %s to exist; set makes it", o.Path)
			}
			var base map[string]any
			if err := json.Unmarshal([]byte(oldBody), &base); err != nil {
				base = map[string]any{}
			}
			if p.body, err = checkBody(merge(base, o.Data)); err != nil {
				return err
			}
		}
		if !exists && docs >= maxDocs {
			return quota("the artifact's database holds %d documents, the most it may", maxDocs)
		}
		grown := size - int64(len(oldBody)) + int64(len(p.body))
		if grown > maxDataBytes && len(p.body) > len(oldBody) {
			return quota("the artifact's database would hold over %d MiB, the most it may", maxDataBytes>>20)
		}
		if !exists {
			docs++
		}
		size = grown
		if _, err := tx.Exec(`INSERT INTO docs(path, coll, body, version, updated) VALUES(?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET body = excluded.body, version = excluded.version, updated = excluded.updated`,
			o.Path, coll, string(p.body), version+1, now); err != nil {
			return unavailable(err)
		}
		paths = append(paths, o.Path)
	}
	if _, err := tx.Exec(`UPDATE totals SET docs = ?, bytes = ?`, docs, size); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	if len(paths) > 0 {
		notify(db.id, paths...)
	}
	return nil
}

// Lease is an acquire's answer.
type Lease struct {
	Acquired  bool      `json:"acquired"`
	Version   int64     `json:"version,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Holder    string    `json:"holder,omitempty"`
}

// Acquire takes a short lease on the document at p for holder, when no
// one else holds it, merging data into the document then.
func (db *DB) Acquire(p, holder string, ttl time.Duration, data map[string]any) (*Lease, error) {
	segs, err := CheckPath(p, true)
	if err != nil {
		return nil, err
	}
	if holder == "" || len(holder) > 200 {
		return nil, invalid("acquire names its holder, at most 200 bytes")
	}
	if !db.rules.can(db.who, segs, true) {
		return nil, invalid("this viewer can't write %s", p)
	}
	if ttl <= 0 {
		ttl = defaultLease
	}
	ttl = min(max(ttl, minLease), maxLease)
	if _, err := readMeta(db.id); err != nil {
		return nil, err
	}
	h, err := handle(db.id, true)
	if err != nil {
		return nil, unavailable(err)
	}
	tx, err := h.Begin()
	if err != nil {
		return nil, unavailable(err)
	}
	defer tx.Rollback()
	now := time.Now()
	var body, lh string
	var version, until int64
	err = tx.QueryRow(`SELECT body, version, lease_holder, lease_until FROM docs WHERE path = ?`, p).Scan(&body, &version, &lh, &until)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, unavailable(err)
	}
	if exists && lh != "" && lh != holder && until > now.UnixMilli() {
		return &Lease{Acquired: false, ExpiresAt: time.UnixMilli(until).UTC()}, nil
	}
	base := map[string]any{}
	if exists {
		json.Unmarshal([]byte(body), &base)
	}
	nb, err := checkBody(merge(base, data))
	if err != nil {
		return nil, err
	}
	var docs, size int64
	if err := tx.QueryRow(`SELECT docs, bytes FROM totals`).Scan(&docs, &size); err != nil {
		return nil, unavailable(err)
	}
	if !exists && docs >= maxDocs {
		return nil, quota("the artifact's database holds %d documents, the most it may", maxDocs)
	}
	grown := size - int64(len(body)) + int64(len(nb))
	if grown > maxDataBytes && len(nb) > len(body) {
		return nil, quota("the artifact's database would hold over %d MiB, the most it may", maxDataBytes>>20)
	}
	if !exists {
		docs++
	}
	if _, err := tx.Exec(`UPDATE totals SET docs = ?, bytes = ?`, docs, grown); err != nil {
		return nil, unavailable(err)
	}
	exp := now.Add(ttl)
	coll := p[:strings.LastIndex(p, "/")]
	if _, err := tx.Exec(`INSERT INTO docs(path, coll, body, version, updated, lease_holder, lease_until) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET body = excluded.body, version = excluded.version, updated = excluded.updated,
		lease_holder = excluded.lease_holder, lease_until = excluded.lease_until`,
		p, coll, string(nb), version+1, now.UnixMilli(), holder, exp.UnixMilli()); err != nil {
		return nil, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, unavailable(err)
	}
	notify(db.id, p)
	return &Lease{Acquired: true, Version: version + 1, ExpiresAt: exp.UTC().Truncate(time.Millisecond), Holder: holder}, nil
}

// Stats is how much artifact id's store holds.
func Stats(id string) (docs int, bytes int64, err error) {
	h, err := handle(id, false)
	if err != nil || h == nil {
		return 0, 0, err
	}
	err = h.QueryRow(`SELECT docs, bytes FROM totals`).Scan(&docs, &bytes)
	return docs, bytes, err
}

// Collections are the store's collections, with how many documents each
// holds, for an agent finding its way.
func Collections(id string) (map[string]int, error) {
	h, err := handle(id, false)
	if err != nil || h == nil {
		return map[string]int{}, err
	}
	rows, err := h.Query(`SELECT coll, count(*) FROM docs GROUP BY coll ORDER BY coll`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return nil, err
		}
		out[c] = n
	}
	return out, rows.Err()
}

// secretName is the key the page tokens are made with, made once, beside
// the artifacts and readable by the user alone.
const secretName = ".secret"

var (
	secretMu sync.Mutex
	secret   []byte
	// secretAt is the file secret was read from: the key is kept for the
	// folder it belongs to, not for whatever Dir names next
	secretAt string
)

// pageSecret is that key.
func pageSecret() ([]byte, error) {
	secretMu.Lock()
	defer secretMu.Unlock()
	p := filepath.Join(Dir(), secretName)
	if secret != nil && secretAt == p {
		return secret, nil
	}
	secret = nil
	if b, err := os.ReadFile(p); err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) == 32 {
			secret, secretAt = k, p
			return k, nil
		}
	}
	defer lock()()
	if b, err := os.ReadFile(p); err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) == 32 {
			secret, secretAt = k, p
			return k, nil
		}
	}
	k := make([]byte, 32)
	rand.Read(k)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(hex.EncodeToString(k)); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	secret, secretAt = k, p
	return k, nil
}

// PageToken is what artifact id's page sends to reach its store: only
// the page magpie served knows it, so another page on this computer, or
// another artifact's, can't read or write it.
func PageToken(id string) (string, error) {
	k, err := pageSecret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("artifact-page\x00" + id))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// PageTokenOK is whether t is artifact id's page token.
func PageTokenOK(id, t string) bool {
	want, err := PageToken(id)
	return err == nil && t != "" && hmac.Equal([]byte(want), []byte(t))
}
