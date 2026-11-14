package update

// When the header's Update pill is hushed. A release comes every few
// minutes, so a newer one is nearly always out (users on X, 11-12: the pill
// was "always lit", and its x hid it only until the next release, minutes
// later). The x hushes it for Quiet; nothing else does. A newer version that
// was only seen, not dismissed, lights it at once (visamzh, normalcoder_ on
// X, 11-14): a quiet counted from the first newer version seen started over
// with every update, so on a version that updates within days the pill
// never came. An urgent release (a security hole, a ban risk, lost data:
// the notes' "**Urgent release:**" line, see Stable) lights it through a
// hush too. The Version row in Settings, Restart to update in the tray's
// menu and a restart or quit all work at any time.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

// Quiet is how long a dismissal of the pill holds: the Stable channel's
// soak.
const Quiet = 72 * time.Hour

// Behind is what is kept of being behind, for the version running (Base).
type Behind struct {
	Base string `json:"base"`
	// Hushed is when the pill was last dismissed: the quiet runs from it.
	// (0.1.3879 to 0.1.4181 kept "since", the first newer version seen,
	// and held the pill for it; a note of theirs is for their own version,
	// so it is never read as a dismissal.)
	Hushed time.Time `json:"hushed,omitempty"`
	// Urgent is the newest version known to be urgent, "" for none.
	Urgent string `json:"urgent,omitempty"`
}

var behindMu sync.Mutex

func behindFile() string { return filepath.Join(appdir.Config(), "update-behind.json") }

// readBehind is the kept note for current; one for another version (this
// one was updated since), or one that can't be read, is none.
func readBehind(current string) Behind {
	data, err := os.ReadFile(behindFile())
	if err != nil {
		return Behind{Base: current}
	}
	var b Behind
	if json.Unmarshal(data, &b) != nil || b.Base != current {
		return Behind{Base: current}
	}
	return b
}

func writeBehind(b Behind) {
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	f := behindFile()
	if os.MkdirAll(filepath.Dir(f), 0o755) != nil {
		return
	}
	tmp := f + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if os.Rename(tmp, f) != nil {
		os.Remove(tmp)
	}
}

// ReadBehind is the note for current.
func ReadBehind(current string) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	return readBehind(current)
}

// NoteBehind notes that latest is out while current runs: an urgent latest
// is kept until current has caught up with it. Being behind starts no
// quiet; only a dismissal does. A note that can't be read loses a
// dismissal (the pill shows), and the urgent one is read again from the
// next check's notes.
func NoteBehind(current, latest string, urgent bool) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	b := readBehind(current)
	if urgent && (b.Urgent == "" || Newer(latest, b.Urgent)) {
		b.Urgent = latest
		writeBehind(b)
	}
	return b
}

// NoteUrgent notes that version, newer than current, is urgent.
func NoteUrgent(current, version string) Behind {
	return NoteBehind(current, version, true)
}

// QuietAgain starts the quiet over: the pill was dismissed.
func QuietAgain(current string, now time.Time) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	b := readBehind(current)
	b.Hushed = now
	writeBehind(b)
	return b
}

// Unhush lifts a dismissal: Settings' Show again.
func Unhush(current string) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	b := readBehind(current)
	if !b.Hushed.IsZero() {
		b.Hushed = time.Time{}
		writeBehind(b)
	}
	return b
}

// Lit is whether the pill may light at now: it was never dismissed, the
// quiet after a dismissal has passed, or an urgent release newer than
// current is out.
func (b Behind) Lit(current string, now time.Time) bool {
	if b.Urgent != "" && Newer(b.Urgent, current) {
		return true
	}
	return b.Hushed.IsZero() || now.Sub(b.Hushed) >= Quiet
}

var urgentLine = lazyre.New(`(?im)^[ \t]*(?:>[ \t]*)?\*\*urgent release:\*\*`)

// UrgentIn is whether a release's notes mark it urgent, as the site reads
// them (site/worker.js, urgentIn).
func UrgentIn(notes string) bool { return urgentLine.MatchString(notes) }
