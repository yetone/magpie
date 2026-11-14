package gateway

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

// A request magpie holds too long before routing it is stopped with a 504
// (giveUp), and what held it is written to a report of its own: the GUI
// app's log goes only to a stderr its user never sees, so the log alone
// can't be sent with a bug report. The report is a small file in HeldDir,
// the last heldKept of them kept, and the Routing page's row for the
// request copies it (Route.Held).
//
// What it holds: when, magpie's version and system, the method and path,
// the model asked for and the agent, how long it was held, each kind of
// wait magpie knew it was in and for how long, the body room's state, how
// many requests were in flight, and where every goroutine stood (function
// names and lines, no values). Never the request's body or headers.

// heldKept is how many reports are kept, the newest.
const heldKept = 5

// heldStacksMost bounds the stacks in one report.
const heldStacksMost = 128 << 10

// HeldDir is the folder the reports of held requests are saved in.
func HeldDir() string { return filepath.Join(appdir.Cache(), "held") }

// heldWhere is HeldDir as the error says it: home as ~.
func heldWhere() string {
	p := HeldDir()
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// heldName is a report's file name: held-<UTC time>-<nanoseconds>.txt, so
// names sort in the order written.
var heldName = lazyre.New(`^held-\d{8}-\d{6}-\d{9}\.txt$`)

// heldMu keeps two reports' writes and prunes apart, and heldLast is
// when the last was named.
var (
	heldMu   sync.Mutex
	heldLast time.Time
)

// writeHeld saves report in HeldDir, through a temp file renamed into
// place, keeps the newest heldKept, and answers its file name.
func writeHeld(report string) (string, error) {
	heldMu.Lock()
	defer heldMu.Unlock()
	dir := HeldDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// a clock coarser than a nanosecond (Windows') names two reports
	// alike: the later one is a nanosecond on
	now := time.Now().UTC().Round(0)
	if !now.After(heldLast) {
		now = heldLast.Add(time.Nanosecond)
	}
	heldLast = now
	name := fmt.Sprintf("held-%s-%09d.txt", now.Format("20060102-150405"), now.Nanosecond())
	tmp, err := os.CreateTemp(dir, ".held-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.WriteString(report)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if es, err := os.ReadDir(dir); err == nil {
		var names []string
		for _, e := range es {
			if heldName.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		for len(names) > heldKept {
			os.Remove(filepath.Join(dir, names[0]))
			names = names[1:]
		}
	}
	return name, nil
}

// HeldReport is the saved report named name, for the Routing page; a name
// that isn't a report's is refused, so nothing else in the folder or out
// of it is read.
func HeldReport(name string) (string, error) {
	if !heldName.MatchString(name) {
		return "", os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(HeldDir(), name))
	return string(b), err
}

// heldReport is the report of u's request, held for held and stopped.
func (s *Server) heldReport(u *unroutedHold, held time.Duration, status int, who, asked, where string, own heldStack) string {
	var b strings.Builder
	fmt.Fprintf(&b, "magpie held a request before routing it, and stopped it with %d\n\n", status)
	fmt.Fprintf(&b, "time: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "magpie: %s (%s/%s, served by %s)\n", cmpOr(Version, "dev"), runtime.GOOS, runtime.GOARCH, cmpOr(Serves, "?"))
	fmt.Fprintf(&b, "request: %s %s\n", u.r.Method, u.r.URL.Path)
	fmt.Fprintf(&b, "model: %s\n", cmpOr(asked, "? (not read yet)"))
	fmt.Fprintf(&b, "agent: %s\n", who)
	fmt.Fprintf(&b, "held: %s,%s\n", held, where)
	fmt.Fprintf(&b, "known waits:%s\n", cmpOr(strings.TrimPrefix(u.waitsSaid(), ";"), " none"))
	// the step that held it, by name, though it is no wait magpie knows
	// of: the first of magpie's own functions on its handler's stack
	fmt.Fprintf(&b, "stood at: %s\n", cmpOr(own.at, "? (its goroutine not found)"))
	// a slow body from a body that stopped coming: what had come, and
	// when the last of it did
	fmt.Fprintf(&b, "body read: %s\n", u.bodySaid())
	bodyRoom.mu.Lock()
	fmt.Fprintf(&b, "body room: %d of %d bytes held (%s), %d waiting\n", bodyRoom.held, bodyRoom.room, cmpOr(bodyRoom.why, "unset"), len(bodyRoom.queue))
	bodyRoom.mu.Unlock()
	s.active.mu.Lock()
	n := len(s.active.m)
	s.active.mu.Unlock()
	fmt.Fprintf(&b, "requests in flight: %d\n", n)
	if own.stack != "" {
		fmt.Fprintf(&b, "\nwhere this request stood:\n%s\n", own.stack)
	}
	fmt.Fprintf(&b, "\nwhere every goroutine stood:\n%s", heldStacks())
	return b.String()
}

// waitsSaid is each kind of known wait the request was in and the time
// spent there, "; " before it, or "" when it was in none.
func (u *unroutedHold) waitsSaid() string {
	u.wmu.Lock()
	defer u.wmu.Unlock()
	if len(u.kinds) == 0 {
		return ""
	}
	now := time.Now()
	parts := make([]string, 0, len(u.kinds))
	for _, k := range u.kinds {
		d := k.spent
		if k.n > 0 {
			d += now.Sub(k.since)
		}
		p := fmt.Sprintf("%s %s", k.kind, d.Round(100*time.Millisecond))
		if k.n > 0 {
			p += " (under way)"
		}
		parts = append(parts, p)
	}
	return "; in " + strings.Join(parts, ", ")
}

// heldStacksAt and heldStacksLast are the last dump of the goroutines for
// a report, and when: a burst of held requests takes one dump.
var (
	heldStacksMu   sync.Mutex
	heldStacksAt   time.Time
	heldStacksLast string
)

// heldStacks is every goroutine's stack, alike ones counted once, at most
// heldStacksMost of it.
func heldStacks() string {
	heldStacksMu.Lock()
	defer heldStacksMu.Unlock()
	if heldStacksLast != "" && time.Since(heldStacksAt) < 10*time.Second {
		return heldStacksLast
	}
	var b bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
	if b.Len() > heldStacksMost {
		b.Truncate(heldStacksMost)
		b.WriteString("\n… (cut)\n")
	}
	heldStacksAt, heldStacksLast = time.Now(), b.String()
	return heldStacksLast
}

// heldStack is where a held request's handler stood: its stack, with no
// values, and at, the first of magpie's own functions on it with what it
// waited in ("gateway.(*Server).serveAgent (sync.Mutex.Lock, 2 minutes)").
type heldStack struct {
	stack string
	at    string
}

// heldOwnMost bounds the dump heldOwn looks for one goroutine in.
const heldOwnMost = 64 << 20

// heldModule is magpie's own functions' prefix.
const heldModule = "github.com/yetone/magpie/internal/"

// heldArgs is a frame's argument words, and heldPC its pc offset: values,
// which a report never holds.
var (
	heldArgs = lazyre.New(`\([^()]*\)$`)
	heldPC   = lazyre.New(` \+0x[0-9a-f]+$`)
)

// goroutineID is the calling goroutine's id, as its stack names it.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	rest, ok := strings.CutPrefix(string(buf[:n]), "goroutine ")
	if !ok {
		return 0
	}
	id, _, _ := strings.Cut(rest, " ")
	v, _ := strconv.ParseUint(id, 10, 64)
	return v
}

// heldOwn is where goroutine gid stands now; zero when it isn't found.
func heldOwn(gid uint64) heldStack {
	if gid == 0 {
		return heldStack{}
	}
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= heldOwnMost {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return heldOf(string(buf), gid)
}

// heldOf is goroutine gid's stack in dump, every goroutine's as
// runtime.Stack writes them.
func heldOf(dump string, gid uint64) heldStack {
	head := fmt.Sprintf("goroutine %d [", gid)
	i := 0
	if !strings.HasPrefix(dump, head) {
		j := strings.Index(dump, "\n"+head)
		if j < 0 {
			return heldStack{}
		}
		i = j + 1
	}
	block := dump[i:]
	if j := strings.Index(block, "\n\n"); j >= 0 {
		block = block[:j]
	}
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	state := ""
	if l := lines[0]; strings.HasSuffix(l, "]:") {
		state = l[len(head) : len(l)-2]
	}
	var out heldStack
	for k, l := range lines {
		switch {
		case k == 0:
		case strings.HasPrefix(l, "\t"):
			l = heldPC.ReplaceAllString(l, "")
		default:
			l = heldArgs.ReplaceAllString(l, "")
			if fn, ok := strings.CutPrefix(l, heldModule); ok && out.at == "" && !strings.HasPrefix(l, "created by ") {
				out.at = fn
				if state != "" {
					out.at += " (" + state + ")"
				}
			}
		}
		lines[k] = l
	}
	out.stack = strings.Join(lines, "\n")
	return out
}
