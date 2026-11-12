package update

// How long this version has been behind (users on X, 11-12: the header's
// Update pill was "always lit"). A release comes every few minutes, so a
// newer one is nearly always out, downloaded and ready; lighting the pill
// for each of them is a pill that never goes out. The pill lights once this
// version has been behind for Quiet, or at once for an urgent release (a
// security hole, a ban risk, lost data: the notes' "**Urgent release:**"
// line, see Stable). The Version row in Settings, Restart to update in the
// tray's menu and a restart or quit all work at any time.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// Quiet is how long a version has to be behind before the pill lights, and
// how long a dismissal holds: the Stable channel's soak.
const Quiet = 72 * time.Hour

// Behind is what is kept of being behind, for the version running (Base).
type Behind struct {
	Base string `json:"base"`
	// Since is when a newer version was first seen, or when the pill was
	// last dismissed: the quiet runs from it.
	Since time.Time `json:"since"`
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

// NoteBehind notes that latest is out while current runs: the quiet starts
// at the first time, and an urgent latest is kept until current has
// caught up with it. A note that can't be read starts the quiet again, but
// the urgent one is read again from the next check's notes.
func NoteBehind(current, latest string, urgent bool, now time.Time) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	b := readBehind(current)
	if b.Since.IsZero() {
		b.Since = now
	}
	if urgent && (b.Urgent == "" || Newer(latest, b.Urgent)) {
		b.Urgent = latest
	}
	writeBehind(b)
	return b
}

// NoteUrgent notes that version, newer than current, is urgent.
func NoteUrgent(current, version string, now time.Time) Behind {
	return NoteBehind(current, version, true, now)
}

// QuietAgain starts the quiet over: the pill was dismissed.
func QuietAgain(current string, now time.Time) Behind {
	behindMu.Lock()
	defer behindMu.Unlock()
	b := readBehind(current)
	b.Since = now
	writeBehind(b)
	return b
}

// Lit is whether the pill may light at now: an urgent release newer than
// current is out, or the quiet has passed.
func (b Behind) Lit(current string, now time.Time) bool {
	if b.Urgent != "" && Newer(b.Urgent, current) {
		return true
	}
	return !b.Since.IsZero() && now.Sub(b.Since) >= Quiet
}

var urgentLine = regexp.MustCompile(`(?im)^[ \t]*(?:>[ \t]*)?\*\*urgent release:\*\*`)

// UrgentIn is whether a release's notes mark it urgent, as the site reads
// them (site/worker.js, urgentIn).
func UrgentIn(notes string) bool { return urgentLine.MatchString(notes) }
