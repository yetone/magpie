package usage

// The request feed (rdsaltbo on X, #6060: a WakaToken plugin keeping its own
// ledger of what magpie reads). GET /v1/magpie/usage/requests?cursor= reads
// the ledger as changes, not as pages of a time-sorted list.
//
// Every row of the whole ledger (the gateway's calls, the calls read from
// the agents' session files, the ones clients reported, other computers'
// shared days) has an id made of what the record itself says and no figure
// that can be mended later (feedID), so it is the same on every read and
// after a restart. The feed keeps, for each id, a version of what a reader
// would want to upsert again (tokens, status, model, errors, the reported
// cost) and the sequence number of its last change. A read lists the ids
// changed after a cursor's sequence: the new ones, the mended ones, and the
// ones that went (a session call the gateway turned out to have served, a
// clear): as tombstones. A change is found by reading the ledger again, so
// a call a session file brings late, whatever its time, is just a new id.
//
// The state is a journal next to usage.jsonl (usage-feed.jsonl), appended
// to before a read answers, so a cursor never gets ahead of what is kept.
// Tombstones are kept FeedKeep; a cursor from before the oldest one pruned,
// from another journal (epoch) or from other filters is refused with a
// FeedError, never answered with a gap.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// FeedKeep is how long a tombstone is kept, and so how long a client may be
// away and still resume. A var for the tests.
var FeedKeep = 30 * 24 * time.Hour

// Cursor words that are no cursor: Start reads the whole ledger as it is
// now, Now reads nothing and gives the cursor to read what comes after.
const (
	FeedStart = "start"
	FeedNow   = "now"
)

// FeedError is a cursor magpie can't resume from. Code is one of
// cursor_invalid, cursor_expired, cursor_reset and cursor_filter_changed.
type FeedError struct{ Code, Message string }

func (e *FeedError) Error() string { return e.Message }

// FeedRequest is one read.
type FeedRequest struct {
	Cursor string
	Limit  int // 100 when none, 500 at most
	Filter Filter
	// Key narrows the feed to one gateway key's calls, tombstones too
	Key string
}

// FeedItem is one change: the row as it is now, or, Gone, the id that no
// longer is a row.
type FeedItem struct {
	ID   string
	Seq  int64
	Gone bool
	Row  Row
}

// FeedPage is a read's answer.
type FeedPage struct {
	Items []FeedItem
	Next  string
	More  bool
}

type feedEntry struct {
	seq  int64
	ver  uint64
	gone bool
	key  string // the gateway key of the call
	at   int64  // unix seconds it went, for a tombstone
}

type feedLine struct {
	Epoch string `json:"epoch,omitempty"`
	Floor int64  `json:"floor,omitempty"`
	Seq   int64  `json:"s,omitempty"`
	ID    string `json:"i,omitempty"`
	Ver   string `json:"v,omitempty"`
	Gone  bool   `json:"g,omitempty"`
	Key   string `json:"k,omitempty"`
	At    int64  `json:"t,omitempty"`
}

var feed struct {
	sync.Mutex
	loaded  bool
	epoch   string
	seq     int64
	floor   int64
	entries map[string]*feedEntry
	lines   int
	pruned  time.Time
	// the rows of the last read, for the pages that follow it
	memo    *feedMemo
	timer   *time.Timer
	failure error
}

// feedMemo is the rows of a read that left more behind it, for the page
// that follows it and no other: a read anywhere else sees the ledger as it
// is, so a call made since is in it.
type feedMemo struct {
	at   time.Time
	rows map[string]*Row
	next int64 // the sequence the page that follows is read from
}

// feedMemoFor is how long one read's rows answer the next pages.
const feedMemoFor = 2 * time.Second

// FeedPath is the journal.
func FeedPath() string { return filepath.Join(filepath.Dir(provider.Path()), "usage-feed.jsonl") }

// ResetFeedForTest forgets what is loaded, as a restart does.
func ResetFeedForTest() {
	feed.Lock()
	defer feed.Unlock()
	feed.loaded, feed.entries, feed.memo = false, nil, nil
}

// feedAgent is the agent that made a row's call, as its record had it
// before the user counted a client as another agent.
func feedAgent(r *Row) string {
	if r.Client != "" {
		return r.Client
	}
	return r.Agent
}

// feedKey is what a row's id is made of: where the call is from and what
// its record says that nobody mends. Not the model, provider or agent names
// (renamed by the user), not a token count, status or cost.
func feedKey(r *Row) string {
	var b strings.Builder
	w := func(s ...string) {
		for _, x := range s {
			b.WriteString(x)
			b.WriteByte(0)
		}
	}
	n := func(i int64) string { return strconv.FormatInt(i, 10) }
	if r.Source == "log" {
		// read from an agent's own file: when, whose, and the ids it gave
		w("n", feedAgent(r), n(r.Time.UnixNano()), r.Session, r.Subagent, r.RequestID, r.ResponseID, r.MessageID)
	} else {
		w("g", r.Computer, r.Source, feedAgent(r), n(r.Time.UnixNano()), n(r.RouteID), r.Operation, r.Session, n(r.Millis), r.Endpoint, r.RequestID, r.ResponseID, r.Via)
	}
	return b.String()
}

func feedID(key string, again int) string {
	if again > 0 {
		key += "\x01" + strconv.Itoa(again)
	}
	sum := sha256.Sum256([]byte(key))
	return "u_" + hex.EncodeToString(sum[:10])
}

// feedVersion is what a reader upserts again when it changes: the figures
// and the outcome, not the price (a catalog update would mend every row).
func feedVersion(r *Row) uint64 {
	h := fnv.New64a()
	w := func(s ...string) {
		for _, x := range s {
			h.Write([]byte(x))
			h.Write([]byte{0})
		}
	}
	n := func(i int) string { return strconv.Itoa(i) }
	w(n(r.Input), n(r.Output), n(r.CacheRead), n(r.CacheWrite), n(r.CacheWrite1h), n(r.Reasoning), n(r.Status),
		r.Error, r.ErrType, r.Model, r.Served, r.Requested, r.Provider, r.Account(), r.Agent, r.Source, r.CallerKeyID,
		strconv.FormatInt(r.Millis, 10), strconv.FormatInt(r.TTFT, 10), n(r.Tries), strconv.FormatBool(r.HandedOn),
		strconv.FormatBool(r.Rejected), r.Effort, r.Kind, r.SessionRole, r.NativeSession)
	if r.OwnCost != nil {
		w(strconv.FormatFloat(*r.OwnCost, 'g', -1, 64))
	}
	if r.Credits != nil {
		w(fmt.Sprint(*r.Credits))
	}
	if r.Compaction != nil {
		w(strconv.FormatBool(*r.Compaction))
	}
	return h.Sum64()
}

func hexOf(v uint64) string { return strconv.FormatUint(v, 16) }

// FeedRead answers one read of the feed.
func FeedRead(req FeedRequest) (FeedPage, error) {
	feed.Lock()
	defer feed.Unlock()
	if !feed.loaded {
		feedLoad()
	}
	if feed.failure != nil {
		return FeedPage{}, feed.failure
	}
	var follows int64 = -1
	if _, seq, _, ok := feedParse(req.Cursor); ok {
		follows = seq
	}
	rows, err := feedRefresh(follows)
	if err != nil {
		return FeedPage{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	fp := feedFingerprint(req)
	var after int64
	switch req.Cursor {
	case FeedStart:
	case FeedNow:
		return FeedPage{Items: []FeedItem{}, Next: feedCursor(feed.seq, fp)}, nil
	default:
		epoch, seq, want, ok := feedParse(req.Cursor)
		switch {
		case !ok:
			return FeedPage{}, &FeedError{"cursor_invalid", "this cursor is not one magpie gave; start again with cursor=start, or cursor=now for what comes after this moment"}
		case epoch != feed.epoch:
			return FeedPage{}, &FeedError{"cursor_reset", "this cursor is of another feed (magpie's usage-feed.jsonl was lost or replaced); read the ledger again with cursor=start and drop what you hold of it"}
		case want != fp:
			return FeedPage{}, &FeedError{"cursor_filter_changed", "this cursor was given for other filters (or another key); a cursor is read with the filters it was given with, start again with cursor=start for new ones"}
		case seq < feed.floor:
			return FeedPage{}, &FeedError{"cursor_expired", "this cursor is older than the changes magpie keeps; read the ledger again with cursor=start and drop what you hold of it"}
		case seq > feed.seq:
			return FeedPage{}, &FeedError{"cursor_invalid", "this cursor is ahead of this magpie's feed; start again with cursor=start"}
		}
		after = seq
	}
	type cand struct {
		id string
		e  *feedEntry
	}
	var cands []cand
	for id, e := range feed.entries {
		if e.seq > after && (req.Key == "" || e.key == req.Key) && !(e.gone && after == 0) {
			cands = append(cands, cand{id, e})
		}
	}
	slices.SortFunc(cands, func(a, b cand) int { return int(a.e.seq - b.e.seq) })
	out := FeedPage{Items: []FeedItem{}}
	last := after
	i := 0
	for ; i < len(cands) && len(out.Items) < limit; i++ {
		c := cands[i]
		last = c.e.seq
		if c.e.gone {
			out.Items = append(out.Items, FeedItem{ID: c.id, Seq: c.e.seq, Gone: true})
			continue
		}
		row := rows[c.id]
		if row == nil || !req.Filter.keepsRow(row) {
			continue
		}
		out.Items = append(out.Items, FeedItem{ID: c.id, Seq: c.e.seq, Row: *row})
	}
	out.More = i < len(cands)
	if !out.More {
		// every change up to now was looked at, one a filter left out too
		last = feed.seq
	}
	out.Next = feedCursor(last, fp)
	if feed.memo != nil && feed.memo.rows != nil {
		feed.memo.next = -1
		if out.More {
			feed.memo.next = last
		}
	}
	return out, nil
}

func feedFingerprint(req FeedRequest) string {
	f := req.Filter
	f.Zone = nil
	b, _ := json.Marshal(struct {
		F Filter
		K string
	}{f, req.Key})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

func feedCursor(seq int64, fp string) string {
	return "c1." + base64.RawURLEncoding.EncodeToString([]byte(feed.epoch+"."+strconv.FormatInt(seq, 10)+"."+fp))
}

func feedParse(c string) (epoch string, seq int64, fp string, ok bool) {
	rest, found := strings.CutPrefix(c, "c1.")
	if !found {
		return
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return
	}
	parts := strings.Split(string(b), ".")
	if len(parts) != 3 {
		return
	}
	seq, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || seq < 0 {
		return
	}
	return parts[0], seq, parts[2], true
}

// feedRefresh reads the ledger and journals what changed in it since the
// last read, answering the rows by id.
func feedRefresh(follows int64) (map[string]*Row, error) {
	if m := feed.memo; m != nil && m.next == follows && time.Since(m.at) < feedMemoFor {
		return m.rows, nil
	}
	now := Clock()
	rows := LedgerOfAt(All, Filter{}, now).Rows
	byID := make(map[string]*Row, len(rows))
	seen := map[string]int{}
	var changes []feedLine
	next := feed.seq
	stamp := func(l feedLine) {
		next++
		l.Seq = next
		changes = append(changes, l)
	}
	for i := range rows {
		r := &rows[i]
		k := feedKey(r)
		id := feedID(k, seen[k])
		seen[k]++
		byID[id] = r
		v := feedVersion(r)
		if e := feed.entries[id]; e != nil && !e.gone && e.ver == v {
			continue
		}
		stamp(feedLine{ID: id, Ver: hexOf(v), Key: r.CallerKeyID})
	}
	at := now.Unix()
	for id, e := range feed.entries {
		if !e.gone && byID[id] == nil {
			stamp(feedLine{ID: id, Gone: true, Key: e.key, At: at})
		}
	}
	// the journal first, then the memory: a cursor never gets ahead of it
	if len(changes) > 0 {
		slices.SortFunc(changes, func(a, b feedLine) int { return int(a.Seq - b.Seq) })
		if err := feedAppend(changes); err != nil {
			return nil, err
		}
		for _, l := range changes {
			v, _ := strconv.ParseUint(l.Ver, 16, 64)
			feed.entries[l.ID] = &feedEntry{seq: l.Seq, ver: v, gone: l.Gone, key: l.Key, at: l.At}
		}
		feed.seq = next
	}
	feedPrune(now)
	feed.memo = &feedMemo{at: time.Now(), rows: byID, next: -1}
	if feed.timer != nil {
		feed.timer.Stop()
	}
	feed.timer = time.AfterFunc(2*feedMemoFor, func() {
		feed.Lock()
		defer feed.Unlock()
		if feed.memo != nil && time.Since(feed.memo.at) >= feedMemoFor {
			feed.memo = nil
		}
	})
	return byID, nil
}

// feedPrune lets go of tombstones older than FeedKeep (once an hour), and
// rewrites the journal then, or when it holds twice what it needs.
func feedPrune(now time.Time) {
	if now.Sub(feed.pruned) < time.Hour && feed.lines < 2*len(feed.entries)+10000 {
		return
	}
	feed.pruned = now
	cut := now.Add(-FeedKeep).Unix()
	for id, e := range feed.entries {
		if e.gone && e.at < cut {
			feed.floor = max(feed.floor, e.seq)
			delete(feed.entries, id)
		}
	}
	if err := feedRewrite(); err != nil {
		feed.pruned = time.Time{} // try again on the next read
	}
}

func feedOpen() (*os.File, error) {
	path := FeedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func feedAppend(ls []feedLine) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range ls {
		enc.Encode(l)
	}
	f, err := feedOpen()
	if err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		feed.lines += len(ls)
	}
	return err
}

func feedRewrite() error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.Encode(feedLine{Epoch: feed.epoch, Floor: feed.floor, Seq: feed.seq})
	for id, e := range feed.entries {
		enc.Encode(feedLine{Seq: e.seq, ID: id, Ver: hexOf(e.ver), Gone: e.gone, Key: e.key, At: e.at})
	}
	path := FeedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".usage-feed-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, err = out.Write(buf.Bytes())
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(out.Name(), path); err != nil {
		return err
	}
	feed.lines = len(feed.entries) + 1
	return nil
}

// feedLoad reads the journal. One that can't be read whole is another
// feed: a new epoch, so a client holding a cursor of the old is told.
func feedLoad() {
	feed.loaded = true
	feed.entries = map[string]*feedEntry{}
	feed.seq, feed.floor, feed.lines, feed.memo = 0, 0, 0, nil
	feed.failure = nil
	fresh := func() {
		feed.entries = map[string]*feedEntry{}
		feed.seq, feed.floor, feed.lines = 0, 0, 0
		b := make([]byte, 8)
		rand.Read(b)
		feed.epoch = hex.EncodeToString(b)
		if err := feedRewrite(); err != nil {
			feed.failure = err
		}
	}
	f, err := os.Open(FeedPath())
	if err != nil {
		fresh()
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	var torn bool
	n := 0
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		if torn { // a bad line with more after it is not a torn end
			fresh()
			return
		}
		var l feedLine
		if json.Unmarshal(raw, &l) != nil {
			torn = true
			continue
		}
		n++
		if n == 1 {
			if l.Epoch == "" {
				fresh()
				return
			}
			feed.epoch, feed.floor, feed.seq = l.Epoch, l.Floor, l.Seq
			continue
		}
		if l.ID == "" {
			fresh()
			return
		}
		v, _ := strconv.ParseUint(l.Ver, 16, 64)
		feed.entries[l.ID] = &feedEntry{seq: l.Seq, ver: v, gone: l.Gone, key: l.Key, at: l.At}
		feed.seq = max(feed.seq, l.Seq)
	}
	if sc.Err() != nil || n == 0 {
		fresh()
		return
	}
	feed.lines = n
	if torn {
		// keep what was whole, and write it without the torn end
		if err := feedRewrite(); err != nil {
			feed.failure = err
		}
	}
}
