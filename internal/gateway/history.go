package gateway

// The routing history: every request's route, once it is done, kept on
// disk so the Routing view can show and replay a day long gone — the trace
// holds only the last few, and only since the gateway started. A day is a
// file of its own, one route a line; a day that is over is gzipped, the
// routes being much alike. Kept historyDays days and historyBytes in all,
// the oldest days going first.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// HistoryDir is where the days are kept: ~/.config/magpie/routing.
func HistoryDir() string { return filepath.Join(filepath.Dir(provider.Path()), "routing") }

// keepRoutes is whether done routes are written to disk. Off in the
// package's tests: a write still going when a test ends would put the
// routing directory back into its temporary HOME as that is removed.
var keepRoutes = true

var history struct {
	mu     sync.Mutex
	pruned time.Time
	counts map[string]counted // a day's file → its requests, as of its size
}

type counted struct {
	size int64
	n    int
}

const dayForm = "2006-01-02"

// historyClock is the time saveRoute prunes the days by: which day is today,
// and which are over or too old. A variable for the tests.
var historyClock = time.Now

// saveRoute adds a done route to its day. Errors are swallowed: keeping
// the history must never break a call.
func saveRoute(r Route) {
	r.Seq = 0
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
	f, err := os.OpenFile(filepath.Join(dir, r.Time.Local().Format(dayForm)+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
// historyDays, then the oldest until what is left fits historyBytes.
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
	oldest := now.Local().AddDate(0, 0, -historyDays+1).Format(dayForm)
	days := historyFiles(dir)
	var total int64
	for _, d := range days {
		total += d.size
	}
	for _, d := range days { // oldest first
		if d.day >= oldest && total <= historyBytes || d.day == today {
			continue
		}
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
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	old, err := os.ReadFile(path + ".gz")
	if err != nil && !os.IsNotExist(err) {
		return
	}
	// the file's routes, each true once the .gz is seen to have it: a set
	// built from the file, a few routes, not from the whole day
	in := map[string]bool{}
	whole := true // the .gz, if there is one, can have routes go after it
	if len(old) > 0 {
		for l := range bytes.Lines(b) {
			in[string(bytes.TrimSpace(l))] = false
		}
		whole = gzRoutes(old, func(l []byte) {
			if _, ok := in[string(l)]; ok {
				in[string(l)] = true
			}
		})
	}
	buf := new(bytes.Buffer)
	if whole {
		buf = bytes.NewBuffer(old)
	}
	z := gzip.NewWriter(buf)
	nl := []byte{'\n'}
	add := func(l []byte) {
		z.Write(l)
		z.Write(nl)
	}
	if !whole {
		gzRoutes(old, add) // written anew, its whole routes first
	}
	added := false
	for l := range bytes.Lines(b) {
		if l = bytes.TrimSpace(l); len(l) > 0 && !in[string(l)] {
			add(l)
			added = true
		}
	}
	if added {
		tmp := path + ".gz.tmp"
		if z.Close() != nil || os.WriteFile(tmp, buf.Bytes(), 0o600) != nil || steady.Rename(tmp, path+".gz") != nil {
			os.Remove(tmp)
			return
		}
	}
	os.Remove(path)
}

// gzRoutes calls f with each route of a day's .gz in turn, a last route
// whole but for its newline among them and one cut short left out. It says
// whether the .gz read to its end, its last route ending in a newline: what
// a gzip member added after it needs to be read.
func gzRoutes(gz []byte, f func([]byte)) bool {
	z, err := gzip.NewReader(bytes.NewReader(gz))
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
	for _, d := range historyFiles(HistoryDir()) {
		// a day is read only when it is the one asked for, or it has
		// changed since it was counted: thirty days gzipped are not read
		// again at every look
		c, ok := history.counts[d.path]
		if d.day == day || !ok || c.size != d.size {
			n := 0
			readDay(d.path, func(line []byte) bool {
				n++
				if d.day != day {
					return true
				}
				var r Route
				if json.Unmarshal(line, &r) == nil {
					r.routedAgain()
					routes = append(routes, r)
				}
				return true
			})
			c = counted{d.size, n}
			history.counts[d.path] = c
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
		routes, cut = routes[len(routes)-historyMax:], true
	}
	return days, routes, cut
}

// routedAgain reads a route kept before Routed was: a try that asked
// another magpie's routing group was marked swapped for the member it
// routed to, and is routed (usage.GroupRouted). A try kept marked swapped
// for its model spelled with other separators (deepseek-v4.1-flash
// answered as deepseek-v4-1-flash) wasn't swapped (usage.SameSpelled), nor
// was one answered under Google's name for the deployment serving its
// Gemini model (gemini-3.8-flash as gemini-3.8-flash-n, usage.GeminiServing).
func (r *Route) routedAgain() {
	for i := range r.Tries {
		if tr := &r.Tries[i]; tr.Swapped && usage.GroupRouted(tr.Model, tr.Served) {
			tr.Swapped, tr.Routed = false, true
		} else if tr.Swapped && (usage.SameSpelled(tr.Model, tr.Served) || usage.GeminiServing(tr.Model, tr.Served)) {
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
func readDay(path string, f func([]byte) bool) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
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

// HistoryRoute searches the request's day and its neighbours (the browser and
// gateway may use different time zones), without holding up history writes.
func HistoryRoute(id int64, day time.Time) (Route, bool) {
	needle := []byte(`"id":` + strconv.FormatInt(id, 10) + `,`)
	for _, offset := range []int{0, -1, 1} {
		name := filepath.Join(HistoryDir(), day.AddDate(0, 0, offset).Format(dayForm)+".jsonl")
		for _, path := range []string{name, name + ".gz"} {
			var found Route
			readDay(path, func(line []byte) bool {
				if !bytes.Contains(line, needle) {
					return true
				}
				if json.Unmarshal(line, &found) != nil || found.ID != id {
					found = Route{}
					return true
				}
				found.routedAgain()
				return false
			})
			if found.ID != 0 {
				return found, true
			}
		}
	}
	return Route{}, false
}
