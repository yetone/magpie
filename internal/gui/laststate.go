package gui

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// The last-known state (#1519, fatkun). state() reads every agent's config
// anew, and with WSL agents on, each distro's through \\wsl.localhost, so
// the Agents page waited 0.4–0.6 s for it on the Windows test box after a
// start (fatkun: about 2 s, and seconds more with WSL on), and the
// Providers page waited behind it. The page now draws from the state
// built last, kept here and in the cache folder for the next start, and
// the fresh one replaces it the moment it is in: an agent leaves the list
// only once a fresh read says it is gone.
//
// What is kept leaves the settings out (they hold the GitHub token and the
// LAN key): the last-known state is served with the settings as they are
// now, so it never brings back one the user changed since.

var lastState struct {
	sync.Mutex
	kept []byte   // the last state built, as stateFile holds it
	read bool     // the file was looked at (or kept was set)
	sum  [32]byte // kept's keptSum
}

// keptMarshaled counts the states and lists kept marshaled, for a test.
var keptMarshaled atomic.Int64

// encodedSum is the hash of v as json.Marshal writes it, with a newline
// after it, found without a copy of those bytes: each poll of the Agents
// and Providers pages marshaled what it keeps, the same as it was nearly
// always, only to compare it, 38 MB of what twenty polls allocated.
func encodedSum(v any) ([32]byte, error) {
	h := sha256.New()
	if err := json.NewEncoder(h).Encode(v); err != nil {
		return [32]byte{}, err
	}
	return [32]byte(h.Sum(nil)), nil
}

// keptSum is encodedSum of what kept is the marshaled bytes of.
func keptSum(kept []byte) [32]byte {
	h := sha256.New()
	h.Write(kept)
	h.Write([]byte{'\n'})
	return [32]byte(h.Sum(nil))
}

// stateFile is the last-known state, with the version that wrote it: an
// older magpie's is not drawn by a newer page.
type stateFile struct {
	Version string    `json:"version"`
	State   stateJSON `json:"state"`
}

func lastStatePath() string { return filepath.Join(appdir.Cache(), "agents-state.json") }

// keepState remembers s as the last-known state. The file is written only
// when what is kept changed, and as magpie's own cache: nothing a request
// reads is built from it, so its write doesn't drop what filememo holds
// (edit.WriteAtomic's would: every poll of the Agents page rebuilt each
// agent's model list and picker, and every provider's catalog, anew).
func keepState(s stateJSON) {
	sum, err := encodedSum(keptFile(s))
	if err != nil {
		return
	}
	keepStateSummed(s, sum)
}

// keptFile is what keepState keeps of s.
func keptFile(s stateJSON) stateFile {
	// kept as it is sent, each option list once
	s = shareOptions(s)
	s.Settings = settings.Settings{}
	// and what is told once: a notice, a file recovered after a crash
	s.Notice, s.Connected, s.Recovered = "", nil, nil
	// and the magpie serving the gateway now, which the next start asks
	s.GatewayOther = nil
	return stateFile{Version: Version, State: s}
}

// keepStateSummed is keepState given sum, encodedSum(keptFile(s)), found
// while s was written (writeStateJSON).
func keepStateSummed(s stateJSON, sum [32]byte) {
	f := keptFile(s)
	lastState.Lock()
	defer lastState.Unlock()
	if !lastState.read {
		lastState.kept, _ = os.ReadFile(lastStatePath())
		lastState.read, lastState.sum = true, keptSum(lastState.kept)
	}
	if sum == lastState.sum {
		return
	}
	keptMarshaled.Add(1)
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	lastState.kept, lastState.sum = b, sum
	p := lastStatePath()
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = steady.WriteFile(p, b, 0o644)
	}
}

// lastKnown is the last state built, by this run or the one before it,
// with the settings as they are now; false when none is kept, or it was
// kept by another version.
func lastKnown() (stateJSON, bool) {
	lastState.Lock()
	if !lastState.read {
		lastState.kept, _ = os.ReadFile(lastStatePath())
		lastState.read, lastState.sum = true, keptSum(lastState.kept)
	}
	b := lastState.kept
	lastState.Unlock()
	var f stateFile
	if len(b) == 0 || json.Unmarshal(b, &f) != nil || f.Version != Version || f.State.Agents == nil {
		return stateJSON{}, false
	}
	f.State.Settings = settings.Load()
	f.State.Last = true
	return f.State, true
}
