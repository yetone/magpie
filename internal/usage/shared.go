package usage

// Shared usage (#542): with WebDAV or S3 sync on, each computer puts the
// calls its Usage page lists on the server, a file a day, under an id of
// its own, and brings the others' down to usage-others beside usage.jsonl.
// The Usage page adds them to this computer's, each row saying which
// computer it was made on. What goes is what the ledger shows of a call —
// times, tokens, models, providers, status — never a request's content.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// ThisComputer is the Filter.Computer of the calls made here.
const ThisComputer = "this"

// SharedDays is how many days of calls a computer keeps on the server.
const SharedDays = 90

// SharedDay is one computer's calls of one day, as it puts them on the server.
type SharedDay struct {
	Computer string       `json:"computer"` // its id
	Name     string       `json:"name"`     // what it is called, its host name
	Day      string       `json:"day"`      // 2006-01-02, the day where it is
	Calls    []SharedCall `json:"calls"`
}

// SharedCall is a call as the ledger has it: Source "log" for one read from
// an agent's session file.
type SharedCall struct {
	Record
	Source string `json:"source,omitempty"`
}

func dir() string { return filepath.Dir(provider.Path()) }

// OthersDir is where the other computers' days are kept: one folder each.
func OthersDir() string { return filepath.Join(dir(), "usage-others") }

// Computer is this computer's id and name, the id made the first time and
// kept in usage-computer.json, which no backup or sync carries.
func Computer() (id, name string) {
	p := filepath.Join(dir(), "usage-computer.json")
	var c struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &c)
	}
	if !validID(c.ID) {
		b := make([]byte, 8)
		rand.Read(b)
		c.ID = hex.EncodeToString(b)
		os.MkdirAll(dir(), 0o755)
		if j, err := json.Marshal(c); err == nil {
			edit.WriteAtomic(p, j)
		}
	}
	name = c.Name
	if name == "" {
		name, _ = os.Hostname()
		name = strings.TrimSuffix(name, ".local")
	}
	if name == "" {
		name = c.ID
	}
	return c.ID, name
}

func validID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

// ValidComputer is whether id can be another computer's id: a folder name
// and a file name's first part, nothing else.
func ValidComputer(id string) bool { return validID(id) }

// OwnDays are this computer's calls since the day of since, by day, as the
// Usage page lists them: a call another magpie passed on here is left to
// that computer, which counts it itself, and where the request archive
// keeps a call is this computer's own.
func OwnDays(since time.Time) map[string][]SharedCall {
	reader := LogCalls
	if reader == nil {
		reader = sessions.Calls
	}
	rows, _, _, _ := ledgerWith(since, Filter{}, Load(since.Add(-24*time.Hour)), reader(since))
	out := map[string][]SharedCall{}
	for i := len(rows) - 1; i >= 0; i-- { // oldest first
		r := rows[i]
		if r.Via != "" {
			continue
		}
		r.Archive, r.Computer = "", ""
		if len(r.Error) > 300 {
			r.Error = r.Error[:300]
		}
		day := r.Time.Local().Format(time.DateOnly)
		out[day] = append(out[day], SharedCall{Record: r.Record, Source: r.Source})
	}
	return out
}

// sharedFile is where another computer's day is kept here.
func sharedFile(computer, day string) string {
	return filepath.Join(OthersDir(), computer, day+".json")
}

// KeepShared keeps another computer's day here, over the one kept before.
func KeepShared(d SharedDay) error {
	if !validID(d.Computer) {
		return os.ErrInvalid
	}
	if _, err := time.Parse(time.DateOnly, d.Day); err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f := sharedFile(d.Computer, d.Day)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	if err := edit.WriteAtomic(f, b); err != nil {
		return err
	}
	if d.Name != "" && d.Day >= latestDay(d.Computer) {
		edit.WriteAtomic(nameFile(d.Computer), []byte(d.Name))
	}
	return os.Chmod(f, 0o600)
}

// nameFile keeps what another computer is called, as its latest day says.
func nameFile(computer string) string { return filepath.Join(OthersDir(), computer, "name") }

func latestDay(computer string) string {
	days, _ := os.ReadDir(filepath.Join(OthersDir(), computer))
	last := ""
	for _, d := range days {
		if day, ok := strings.CutSuffix(d.Name(), ".json"); ok && day > last {
			last = day
		}
	}
	return last
}

// sharedNames are the other computers kept here, by id, and what each is called.
func sharedNames() map[string]string {
	out := map[string]string{}
	kept := SharedKept()
	if len(kept) == 0 {
		return out
	}
	self, _ := Computer()
	for _, k := range kept {
		c, _, _ := strings.Cut(k, "/")
		if c == self || out[c] != "" {
			continue
		}
		name, _ := os.ReadFile(nameFile(c))
		out[c] = strings.TrimSpace(string(name))
		if out[c] == "" {
			out[c] = c
		}
	}
	return out
}

// SharedKept are the other computers' days kept here, as "<id>/<day>".
func SharedKept() []string {
	var out []string
	cs, _ := os.ReadDir(OthersDir())
	for _, c := range cs {
		if !c.IsDir() {
			continue
		}
		days, _ := os.ReadDir(filepath.Join(OthersDir(), c.Name()))
		for _, d := range days {
			if day, ok := strings.CutSuffix(d.Name(), ".json"); ok {
				out = append(out, c.Name()+"/"+day)
			}
		}
	}
	slices.Sort(out)
	return out
}

// DropShared lets another computer's day go, and its folder with its last.
func DropShared(computer, day string) {
	if !validID(computer) {
		return
	}
	os.Remove(sharedFile(computer, day))
	if latestDay(computer) == "" {
		os.Remove(nameFile(computer))
		os.Remove(filepath.Join(OthersDir(), computer))
	}
}

// DropAllShared lets every other computer's usage go: sync, or its sharing
// of usage, was turned off.
func DropAllShared() { os.RemoveAll(OthersDir()) }

// sharedSource is one kept day, for the request page to read when it changed.
type sharedSource struct {
	path, computer, day string
	size                int64
	mod                 time.Time
}

// sharedSources are the days kept from day since on (all for zero), and
// whether any is kept at all, of any day; this computer's id is never among them.
func sharedSources(since time.Time) (out []sharedSource, some bool) {
	kept := SharedKept()
	if len(kept) == 0 {
		return nil, false
	}
	self, _ := Computer()
	from := ""
	if !since.IsZero() { // a day's calls began where it was a day: one before
		from = since.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	for _, k := range kept {
		c, day, _ := strings.Cut(k, "/")
		if c == self || !validID(c) {
			continue
		}
		some = true
		if day < from {
			continue
		}
		p := sharedFile(c, day)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, sharedSource{path: p, computer: c, day: day, size: fi.Size(), mod: fi.ModTime()})
	}
	return out, some
}

func readShared(p string) (SharedDay, bool) {
	var d SharedDay
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &d) != nil {
		return d, false
	}
	return d, true
}

// sharedRecords are the other computers' calls since since, each with its
// computer.
func sharedRecords(since time.Time) (calls []SharedCall) {
	srcs, _ := sharedSources(since)
	for _, s := range srcs {
		d, ok := readShared(s.path)
		if !ok {
			continue
		}
		for _, c := range d.Calls {
			if !since.IsZero() && c.Time.Before(since) {
				continue
			}
			c.Computer = s.computer
			calls = append(calls, c)
		}
	}
	return calls
}
