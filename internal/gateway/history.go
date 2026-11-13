package gateway

// The routing history: every request's route, once it is done, kept on
// disk so the Routing view can show and replay a day long gone — the trace
// holds only the last few, and only since the gateway started. A day is a
// file of its own, one route a line; a day that is over is gzipped, the
// routes being much alike. Kept historyDays days, and historyBytes for the
// days that are over, the oldest days going first.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"hash/maphash"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/usage"
)

const (
	historyDays  = 30
	historyBytes = 200 << 20
	historyMax   = 2000 // the routes of one day served at most: its latest
)

// A day written plain was bounded by nothing: one reached 7.25 GB, and
// gzipping it took a 1.6 GB host down (tsingchao on X). Past
// historyFileBytes a day goes on in a file of its next number
// (2026-11-13.1.jsonl), and past historyDayBytes in all its oldest files go.
// Variables for the tests.
var (
	historyFileBytes int64 = 256 << 20
	historyDayBytes  int64 = 1 << 30
)

// dayAppendPath is the file a day's next route is written to: the first of
// its numbered files with room.
func dayAppendPath(dir, day string) string {
	for n := 0; ; n++ {
		p := filepath.Join(dir, day+".jsonl")
		if n > 0 {
			p = filepath.Join(dir, day+"."+strconv.Itoa(n)+".jsonl")
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() < historyFileBytes {
			return p
		}
	}
}

// capToday removes the oldest of today's numbered files while they hold more
// than historyDayBytes, the latest one staying whatever its size.
func capToday(dir, today string) {
	type part struct {
		n    int
		path string
		size int64
	}
	var parts []part
	var total int64
	for _, d := range historyFiles(dir) {
		name := filepath.Base(d.path)
		if d.day != today || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(name, today), "."), ".jsonl"))
		parts = append(parts, part{n, d.path, d.size})
		total += d.size
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].n < parts[j].n })
	for i := 0; i < len(parts)-1 && total > historyDayBytes; i++ {
		if os.Remove(parts[i].path) == nil {
			total -= parts[i].size
		}
	}
}

// HistoryDir is where the days are kept: ~/.config/magpie/routing.
func HistoryDir() string { return filepath.Join(filepath.Dir(provider.Path()), "routing") }

// keepRoutes is whether done routes are written to disk. Off in the
// package's tests: a write still going when a test ends would put the
// routing directory back into its temporary HOME as that is removed.
var keepRoutes = true

// testSandbox is testenv.Marker, which gateway can't import outside its
// tests: set in a test binary testenv isolated.
const testSandbox = "MAGPIE_TEST_SANDBOX"

// keepsRoutes says whether a done route is written to disk: never in a test
// binary testenv isolated. The write runs behind the request that ended the
// route, so one still going when its test ended put routing/ back into the
// test's config folder as that was removed, failing its TempDir cleanup
// with "directory not empty": internal/gui's TestContextTextAPI (#1543),
// and any package whose tests send a request through a gateway (gui, tui,
// agent, davsync). No test outside this package reads the history back.
func keepsRoutes() bool { return keepRoutes && os.Getenv(testSandbox) == "" }

var history struct {
	mu     sync.Mutex
	pruned time.Time
	counts map[string]counted // a day's file → its requests, as of its size
	// the live page's tallies of today (DayTallies): each file's bytes
	// folded in so far, up to the end of its last whole line, so a look
	// reads only what was added since (#3383: a busy day is 350 MB)
	talliedDay string
	talliedOff map[string]int64
	tallies    map[string]*Tally
}

type counted struct {
	size int64
	n    int
	// a day's .jsonl, still written to: the bytes tallied, up to the end
	// of its last whole line, and where each of its lines starts, so a
	// look reads only what was added since and parses only the latest
	// lines (#3383: a busy day's file is 350 MB and was read whole at every
	// look, every 15s while the Routing view was open)
	off    int64
	starts []int64
}

const dayForm = "2006-01-02"

// historyClock is the time saveRoute prunes the days by: which day is today,
// and which are over or too old. A variable for the tests.
var historyClock = time.Now

// saveRoute adds a done route to its day. Errors are swallowed: keeping
// the history must never break a call.
func saveRoute(r Route) {
	r.Seq = 0
	r.LastConvs = nil // a live request's, named by its reply once done
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	history.mu.Lock()
	defer history.mu.Unlock()
	dir := HistoryDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(dayAppendPath(dir, r.Time.Local().Format(dayForm)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
	if now := historyClock(); now.Sub(history.pruned) > time.Hour {
		history.pruned = now
		pruneHistory(dir, now)
	}
}

// pruneHistory gzips the days that are over, and drops the days older than
// historyDays, then the oldest until the days that are over fit
// historyBytes. A day goes whole, as it does once its late routes are in
// its .gz: a late route's file left beside the .gz (gzipFile) goes with it.
// A file that can't be removed stays.
func pruneHistory(dir string, now time.Time) {
	// a .gz.tmp left by a magpie stopped before renaming it (gzipFile) goes
	// once it is an hour old: a younger one may be another magpie's, still
	// being written
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if !strings.HasSuffix(e.Name(), ".jsonl.gz.tmp") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	today := now.Local().Format(dayForm)
	for _, d := range historyFiles(dir) {
		if d.day < today && !strings.HasSuffix(d.path, ".gz") {
			gzipFile(d.path)
		}
	}
	capToday(dir, today)
	oldest := now.Local().AddDate(0, 0, -historyDays+1).Format(dayForm)
	days := historyFiles(dir)
	// the days that are over, held to historyBytes: today's file isn't
	// counted, being kept whatever its size and written plain, at many
	// times the size it is gzipped at once it is over. Counted, a busy
	// day's plain file took the room of the days before it, and the
	// Context tab showed only today's sessions (linkaww on Discord)
	var total int64
	for _, d := range days {
		if d.day != today {
			total += d.size
		}
	}
	drop := ""               // the day being dropped, whole: historyFiles puts its files next to each other
	for _, d := range days { // oldest first
		if d.day != drop && (d.day >= oldest && total <= historyBytes || d.day == today) {
			continue
		}
		drop = d.day
		if os.Remove(d.path) == nil {
			total -= d.size
		}
	}
}

// gzipFile gzips a day's file into its .gz. A day gzipped already keeps its
// .gz, the file's routes going after its own as a gzip member of their own
// (read back as one stream): a route that began before midnight and was done
// after the first prune past it is written to its day's file again. A route
// in the .gz already, from a file added and left behind (magpie stopped
// before removing it), isn't added again. A .gz cut short, or ending in a
// route cut short (an older magpie stopped, or the disk filled, as it or its
// file was written), is written anew from its whole routes and the file's
// when the file has a route to add: after the break the file's routes
// couldn't be read, and after a route cut short the first of them would join
// it. A last route whole but for its newline is kept. The .gz is read a route
// at a time, never held whole unzipped; it is replaced whole, never seen half
// written, and the file goes once its routes are in. A .gz that can't be read
// from disk is left as it is, and the file with it, for a later prune.
func gzipFile(path string) {
	src, err := os.Open(path)
	if err != nil {
		return
	}
	defer src.Close()
	gzPath := path + ".gz"
	var old *os.File
	if f, err := os.Open(gzPath); err == nil {
		defer f.Close()
		if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
			old = f
		}
	} else if !os.IsNotExist(err) {
		return
	}
	// the file's routes, each true once the .gz is seen to have it: a set
	// of their hashes, 8 bytes a route, never the routes themselves (a
	// day's file was read whole into memory, and a 7 GB one took a 1.6 GB
	// host down, tsingchao on X)
	in := map[uint64]bool{}
	whole := true // the .gz, if there is one, can have routes go after it
	if old != nil {
		eachLine(src, func(l []byte) { in[routeKey(l)] = false })
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return
		}
		whole = gzRoutes(old, func(l []byte) {
			k := routeKey(l)
			if _, ok := in[k]; ok {
				in[k] = true
			}
		})
	}
	// the new .gz is written beside the old one and renamed over it; made
	// at the first route to add, so a day with none leaves the .gz alone
	tmp := path + ".gz.tmp"
	var out *os.File
	var z *gzip.Writer
	fail := func() {
		if out != nil {
			out.Close()
			os.Remove(tmp)
		}
	}
	nl := []byte{'\n'}
	open := func() bool {
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return false
		}
		out = f
		if old != nil {
			if _, err := old.Seek(0, io.SeekStart); err != nil {
				return false
			}
			if whole {
				if _, err := io.Copy(out, old); err != nil {
					return false
				}
			}
		}
		z = gzip.NewWriter(out)
		if !whole {
			gzRoutes(old, func(l []byte) { z.Write(l); z.Write(nl) }) // written anew, its whole routes first
		}
		return true
	}
	added, ok := false, true
	eachLine(src, func(l []byte) {
		if !ok || in[routeKey(l)] {
			return
		}
		if z == nil && !open() {
			ok = false
			return
		}
		z.Write(l)
		z.Write(nl)
		added = true
	})
	if !ok {
		fail()
		return
	}
	// closed before the rename and the remove: Windows holds an open file
	src.Close()
	if old != nil {
		old.Close()
	}
	if added {
		if z.Close() != nil || out.Sync() != nil || out.Close() != nil || steady.Rename(tmp, gzPath) != nil {
			fail()
			os.Remove(tmp)
			return
		}
	}
	os.Remove(path)
}

// routeKey is what a route is remembered by when a day's file is set
// against its .gz.
func routeKey(l []byte) uint64 { return maphash.Bytes(routeSeed, l) }

var routeSeed = maphash.MakeSeed()

// eachLine calls f with each non-blank line of r, trimmed, one line in
// memory at a time.
func eachLine(r io.Reader, f func([]byte)) {
	br := bufio.NewReaderSize(r, 256<<10)
	var long []byte // a line longer than the buffer, kept across reads
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			long = append(long, line...)
			continue
		}
		if len(long) > 0 {
			long = append(long, line...)
			line = long
		}
		if l := bytes.TrimSpace(line); len(l) > 0 {
			f(l)
		}
		long = long[:0]
		if err != nil {
			return
		}
	}
}

// gzRoutes calls f with each route of a day's .gz in turn, a last route
// whole but for its newline among them and one cut short left out. It says
// whether the .gz read to its end, its last route ending in a newline: what
// a gzip member added after it needs to be read.
func gzRoutes(gz io.Reader, f func([]byte)) bool {
	z, err := gzip.NewReader(gz)
	if err != nil {
		return false
	}
	r := bufio.NewReader(z)
	for {
		line, err := r.ReadBytes('\n')
		l := bytes.TrimSpace(line)
		if err != nil {
			if len(l) > 0 && json.Valid(l) {
				f(l)
			}
			return err == io.EOF && len(l) == 0
		}
		if len(l) > 0 {
			f(l)
		}
	}
}

type dayFile struct {
	day, path string
	size      int64
}

// historyFiles are the kept days, oldest first.
func historyFiles(dir string) []dayFile {
	es, _ := os.ReadDir(dir)
	var out []dayFile
	for _, e := range es {
		name := e.Name()
		day, _, _ := strings.Cut(name, ".")
		if _, err := time.Parse(dayForm, day); err != nil || !(strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, dayFile{day, filepath.Join(dir, name), info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].day < out[j].day })
	return out
}

// HistoryDay is a kept day, and how many requests it had.
type HistoryDay struct {
	Day      string `json:"day"`
	Requests int    `json:"requests"`
}

// History is the days kept, newest first, and the routes of day, oldest
// first — its latest historyMax, and whether there were more.
func History(day string) (days []HistoryDay, routes []Route, cut bool) {
	history.mu.Lock()
	defer history.mu.Unlock()
	routes = []Route{}
	if history.counts == nil {
		history.counts = map[string]counted{}
	}
	total := 0
	for _, d := range historyFiles(HistoryDir()) {
		// a .gz is read only when it is the one asked for, or it has
		// changed since it was counted: thirty days gzipped are not read
		// again at every look; a .jsonl only past what was tallied
		c, ok := history.counts[d.path]
		if !strings.HasSuffix(d.path, ".gz") {
			c = tally(d, c)
			if d.day == day {
				// the lines before the latest historyMax aren't parsed: a
				// route is written as it is done, so the file is nearly in
				// time order, and historySlack more cover one done late
				from := int64(0)
				if k := len(c.starts) - historyMax - historySlack; k > 0 {
					from = c.starts[k]
				}
				readDayFrom(d.path, from, func(line []byte) bool {
					routes = appendRoute(routes, line)
					return true
				})
			}
		} else if d.day == day || !ok || c.size != d.size {
			if d.day == day && (!ok || c.size != d.size) {
				// counted first, routes left as lines: a busy day's every
				// route parsed at once, to keep its latest historyMax, was
				// hundreds of MB, and a small server running the Context
				// tab went down (Yayoi_no_yume on X)
				n := 0
				readDay(d.path, func([]byte) bool { n++; return true })
				c, ok = counted{size: d.size, n: n}, true
			}
			n, skip := 0, -1
			if d.day == day && ok && c.size == d.size {
				skip = c.n - historyMax - historySlack
			}
			readDay(d.path, func(line []byte) bool {
				if n++; d.day == day && n > skip {
					routes = appendRoute(routes, line)
				}
				return true
			})
			c = counted{size: d.size, n: n}
		}
		history.counts[d.path] = c
		if d.day == day {
			total += c.n
		}
		if n := c.n; n > 0 {
			if len(days) > 0 && days[0].Day == d.day {
				days[0].Requests += n // a late route's file beside the day's .gz
			} else {
				days = append([]HistoryDay{{d.day, n}}, days...)
			}
		}
	}
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Time.Before(routes[j].Time) })
	if len(routes) > historyMax {
		routes = routes[len(routes)-historyMax:]
	}
	return days, routes, cut || total > historyMax
}

// historySlack: the lines read past a day's latest historyMax, for a
// route done after the later ones began.
const historySlack = 200

// historyParsed counts the routes History has parsed, for the tests.
var historyParsed atomic.Int64

func appendRoute(routes []Route, line []byte) []Route {
	historyParsed.Add(1)
	var r Route
	if json.Unmarshal(line, &r) != nil {
		return routes
	}
	r.routedAgain()
	r.Prompt.skillsApart()
	r.Canceled = usage.CanceledBy(r.Status, r.Error) // kept before Canceled was
	return append(routes, r)
}

// tally counts a day's .jsonl past what c has tallied: only its whole
// lines, a last one without its newline yet maybe half written. A file
// smaller than what was tallied was written over, and is counted again
// from its start.
func tally(d dayFile, c counted) counted {
	if d.size < c.off || c.size > d.size {
		c = counted{}
	}
	if d.size == c.size {
		return c
	}
	f, err := os.Open(d.path)
	if err != nil {
		return counted{size: d.size}
	}
	defer f.Close()
	if _, err := f.Seek(c.off, io.SeekStart); err != nil {
		return counted{size: d.size}
	}
	buf := make([]byte, 256<<10)
	at, start, full := c.off, c.off, false
	tail := false
	for {
		k, err := f.Read(buf)
		for i, b := range buf[:k] {
			switch b {
			case '\n':
				if full {
					c.starts = append(c.starts, start)
				}
				start, full = at+int64(i)+1, false
				c.off = start
			case ' ', '\t', '\r':
			default:
				full = true
			}
		}
		at += int64(k)
		if err != nil {
			tail = full
			break
		}
	}
	c.size, c.n = d.size, len(c.starts)
	if tail {
		c.n++ // counted as a line, as readDay reads it, but tallied again
	}
	return c
}

// routedAgain reads a route kept before Routed was: a try that asked
// another magpie's routing group was marked swapped for the member it
// routed to, and is routed (usage.GroupRouted). A try kept marked swapped
// for its model spelled with other separators (deepseek-v4.1-flash
// answered as deepseek-v4-1-flash) wasn't swapped (usage.SameSpelled), nor
// was one answered under Google's name for the deployment serving its
// Gemini model (gemini-3.8-flash as gemini-3.8-flash-n) or xAI's for a Grok
// one (grok-4.7 as grok-4.7-build), usage.ServingName, nor one answered
// under the name its provider's list gives it (usage.ListedAs). Its
// agent is named again as usage.AgentOf names it now, as the ledger's
// records are: Kimi Code's "kimi-code-cli", kept before AgentOf knew it,
// is Kimi Code ("kimi") on the Routing page and the Context tab too, its
// icon and name with it, not a card of its own drawn as the cube (jiabubu).
func (r *Route) routedAgain() {
	if r.Agent != "" {
		r.Agent = usage.AgentOf(r.Agent)
	}
	for i := range r.Tries {
		if tr := &r.Tries[i]; tr.Swapped && usage.GroupRouted(tr.Model, tr.Served) {
			tr.Swapped, tr.Routed = false, true
		} else if tr.Swapped && (usage.SameSpelled(tr.Model, tr.Served) || usage.ServingName(tr.Model, tr.Served) || usage.VendorAlias(tr.Model, tr.Served) || usage.RegionTail(tr.Model, tr.Served) || usage.ListedAs(tr.ID, tr.Model, tr.Served)) {
			tr.Swapped = false
		}
	}
	if n := len(r.Tries); n > 0 && r.Swapped && !r.Tries[n-1].Swapped && !r.Tries[n-1].Routed && r.Served == r.Tries[n-1].Served {
		r.Swapped = false
	}
	if n := len(r.Tries); n > 0 && r.Swapped && r.Tries[n-1].Routed && r.Served == r.Tries[n-1].Served {
		r.Swapped, r.Routed = false, true
	}
}

// readDay calls f with each line of a day's file.
func readDay(path string, f func([]byte) bool) { readDayFrom(path, 0, f) }

// readDayFrom calls f with each line of a day's file from byte off of a
// .jsonl, a line's start.
func readDayFrom(path string, off int64, f func([]byte) bool) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	if off > 0 {
		if _, err := file.Seek(off, io.SeekStart); err != nil {
			return
		}
	}
	var r io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(file)
		if err != nil {
			return
		}
		defer z.Close()
		r = z
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			if !f(line) {
				return
			}
		}
	}
}

// Tally is one account or key's day, as the day's history tells it: how
// often it was tried, answered and failed, its latest answer, the models
// that answered, and how its latest try left it. The Routing page's
// accounts and keys say these of a whole day (#3351), read from the
// same files the Usage page reads, where counting the trace would say
// only of the last few requests the gateway keeps. What the history
// cannot say — the readings its latest request was weighed by, one model
// resting alone — the page keeps from the trace.
type Tally struct {
	ID       string         `json:"id"` // what rests after a failure, as Weighed has it
	Provider string         `json:"provider"`
	Name     string         `json:"name,omitempty"`
	Icon     string         `json:"icon,omitempty"`
	Preset   string         `json:"preset,omitempty"`
	Who      string         `json:"who,omitempty"`
	Kind     string         `json:"kind"`
	Agent    string         `json:"agent,omitempty"`
	Plan     string         `json:"plan,omitempty"`
	Model    string         `json:"model,omitempty"`
	Routing  string         `json:"routing,omitempty"`
	Rank     int            `json:"rank,omitempty"`
	Tried    int            `json:"tried"`
	OK       int            `json:"ok"`
	Fails    map[string]int `json:"fails,omitempty"`
	Last     int64          `json:"last,omitempty"`   // ms, of its latest answer
	AuthAt   int64          `json:"authAt,omitempty"` // ms, of its latest try refused for its sign-in (failAuth)
	Models   []string       `json:"models,omitempty"`
	Rest     *Rest          `json:"rest,omitempty"`   // how its latest try left it
	RestAt   int64          `json:"restAt,omitempty"` // ms, of when it did
	seen     int64          // ms, of the latest request that weighed it
}

// tallied is the part of a kept route the tallies read: json.Unmarshal
// leaves the rest of the line alone.
type tallied struct {
	Time  time.Time `json:"time"`
	Order []Weighed `json:"order"`
	Tries []Try     `json:"tries"`
}

// tallyKey is what the page groups an account by: its provider, its user
// and its plan (two seats of one email on different plans are two
// accounts, not one), however it is weighed into a request (its sign-in may move); a
// key or a provider by its own id.
func tallyKey(w Weighed) string {
	if w.Kind == "account" && w.Who != "" {
		k := []string{w.Provider, strings.ToLower(strings.TrimSpace(w.Who))}
		if plan := strings.ToLower(strings.TrimSpace(w.Plan)); plan != "" {
			k = append(k, plan) // a plan-less seat keeps the two-part key
		}
		b, _ := json.Marshal(k)
		return string(b)
	}
	return w.ID
}

// DayTallies counts today's history per account or key, the Usage page's
// own source. The counts ride on the history read the live page already
// asks for, every 15s while it is open, so each look folds in only the
// lines added to today's files since the last: a busy day's file is
// hundreds of MB (#3383), read whole once at most. What it returns is a
// copy, written out after the lock is let go.
func DayTallies() map[string]Tally {
	day := historyClock().Local().Format(dayForm)
	history.mu.Lock()
	defer history.mu.Unlock()
	var files []dayFile
	for _, d := range historyFiles(HistoryDir()) {
		if d.day == day {
			files = append(files, d)
		}
	}
	reset := history.talliedDay != day || history.tallies == nil
	have := map[string]bool{}
	for _, d := range files {
		have[d.path] = true
		// a file that shrank, or a .gz of today (a late route's day over
		// once), is counted again from the start: everything of the day
		if off, ok := history.talliedOff[d.path]; ok && (d.size < off || strings.HasSuffix(d.path, ".gz") && d.size != off) {
			reset = true
		}
	}
	for path := range history.talliedOff {
		if !have[path] { // a file gone, or the folder moved (a test's HOME)
			reset = true
		}
	}
	if reset {
		history.talliedDay, history.talliedOff, history.tallies = day, map[string]int64{}, map[string]*Tally{}
	}
	for _, d := range files {
		off := history.talliedOff[d.path]
		if strings.HasSuffix(d.path, ".gz") {
			if off == d.size {
				continue
			}
			readDay(d.path, func(line []byte) bool { foldTally(history.tallies, line); return true })
			history.talliedOff[d.path] = d.size
			continue
		}
		if off < d.size {
			history.talliedOff[d.path] = foldTallies(d.path, off, history.tallies)
		}
	}
	out := make(map[string]Tally, len(history.tallies))
	for k, a := range history.tallies {
		c := *a
		c.Fails, c.Models = maps.Clone(a.Fails), slices.Clone(a.Models)
		out[k] = c
	}
	return out
}

// foldTallies folds path's whole lines from off into tallies, and says
// where the first line not folded starts: a line still being written
// (another magpie's, say) waits for the next look.
func foldTallies(path string, off int64, tallies map[string]*Tally) int64 {
	f, err := os.Open(path)
	if err != nil {
		return off
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off
	}
	br := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil { // no newline yet: not a whole line
			return off
		}
		off += int64(len(line))
		if line = bytes.TrimSpace(line); len(line) > 0 {
			foldTally(tallies, line)
		}
	}
}

// tallyLines counts the lines foldTally has read, for the tests.
var tallyLines atomic.Int64

// foldTally counts one kept route into tallies. A route is written as it
// is done, so a day's lines are nearly, not quite, in time order: which
// rest and which name are the latest is told by their times.
func foldTally(tallies map[string]*Tally, line []byte) {
	tallyLines.Add(1)
	var r tallied
	if json.Unmarshal(line, &r) != nil {
		return
	}
	ms := r.Time.UnixMilli()
	by := map[string]*Tally{} // a try's seat → its tally
	for _, w := range r.Order {
		k := tallyKey(w)
		a := tallies[k]
		if a == nil {
			a = &Tally{}
			tallies[k] = a
		}
		if a.seen == 0 || ms >= a.seen { // how the latest request weighed it
			a.ID, a.Provider, a.Name, a.Icon, a.Preset = w.ID, w.Provider, w.Name, w.Icon, w.Preset
			a.Who, a.Kind, a.Agent, a.Plan, a.Model, a.Routing, a.Rank = w.Who, w.Kind, w.Agent, w.Plan, w.Model, w.Routing, w.Rank
			a.seen = ms
		}
		// a rest on one model alone says nothing of the account (#1960),
		// which answers its others; the page keeps that one from the trace
		if w.Rest != nil && w.Rest.Model == "" && ms >= a.RestAt {
			rest := *w.Rest
			a.Rest, a.RestAt = &rest, ms
		}
		by[w.ID] = a
	}
	for _, tr := range r.Tries {
		a := by[tr.ID]
		if a == nil || !tr.Done || tr.Fail == "canceled" { // the agent's doing, not its
			continue
		}
		end := tr.Start.UnixMilli() + tr.Millis
		a.Tried++
		if tr.Status < 400 && tr.Fail == "" {
			a.OK++
			a.Last = max(a.Last, end)
			m := tr.Model
			if m == "" {
				for _, w := range r.Order {
					if w.ID == tr.ID {
						m = w.Model
						break
					}
				}
			}
			if m != "" && !slices.Contains(a.Models, m) {
				a.Models = append(a.Models, m)
			}
		} else {
			f := tr.Fail
			if f == "" {
				f = "other"
			}
			if f == failQuota && tr.Rest != nil && tr.Rest.Rate {
				f = failRate // counted as the vendor called it
			}
			if a.Fails == nil {
				a.Fails = map[string]int{}
			}
			a.Fails[f]++
			if f == failAuth {
				a.AuthAt = max(a.AuthAt, end)
			}
		}
		if tr.Rest != nil && tr.Rest.Model == "" && end >= a.RestAt {
			rest := *tr.Rest
			a.Rest, a.RestAt = &rest, end
		}
	}
}

// HistoryRoute searches the request's day and its neighbours (the browser and
// gateway may use different time zones), without holding up history writes.
func HistoryRoute(id int64, day time.Time) (Route, bool) {
	needle := []byte(`"id":` + strconv.FormatInt(id, 10) + `,`)
	for _, offset := range []int{0, -1, 1} {
		want := day.AddDate(0, 0, offset).Format(dayForm)
		for _, d := range historyFiles(HistoryDir()) { // its numbered files too
			if d.day != want {
				continue
			}
			path := d.path
			var found Route
			readDay(path, func(line []byte) bool {
				if !bodyHasBytes(line, needle) {
					return true
				}
				if json.Unmarshal(line, &found) != nil || found.ID != id {
					found = Route{}
					return true
				}
				found.routedAgain()
				found.Prompt.skillsApart()
				found.Canceled = usage.CanceledBy(found.Status, found.Error)
				return false
			})
			if found.ID != 0 {
				return found, true
			}
		}
	}
	return Route{}, false
}
