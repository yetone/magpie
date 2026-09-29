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
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
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
	if time.Since(history.pruned) > time.Hour {
		history.pruned = time.Now()
		pruneHistory(dir, time.Now())
	}
}

// pruneHistory gzips the days that are over, and drops the days older than
// historyDays, then the oldest until what is left fits historyBytes.
func pruneHistory(dir string, now time.Time) {
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

func gzipFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	z.Write(b)
	if z.Close() != nil || os.WriteFile(path+".gz", buf.Bytes(), 0o600) != nil {
		return
	}
	os.Remove(path)
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
			readDay(d.path, func(line []byte) {
				n++
				if d.day != day {
					return
				}
				var r Route
				if json.Unmarshal(line, &r) == nil {
					routes = append(routes, r)
				}
			})
			c = counted{d.size, n}
			history.counts[d.path] = c
		}
		if n := c.n; n > 0 {
			days = append([]HistoryDay{{d.day, n}}, days...)
		}
	}
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Time.Before(routes[j].Time) })
	if len(routes) > historyMax {
		routes, cut = routes[len(routes)-historyMax:], true
	}
	return days, routes, cut
}

// readDay calls f with each line of a day's file.
func readDay(path string, f func([]byte)) {
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
			f(line)
		}
	}
}
