package provider

// A Remote magpie that is this magpie itself (hanslin1666 on Discord):
// computer B uses A through a Remote magpie provider, sync carries that
// provider to A, and on A it points at A. Each of its models is one of
// A's own, a request for it a pass more through A's gateway on its way
// there, and each one stands beside the model it is in A's lists and
// groups. The list A fetches from it says so: each model's magpie_via
// ends with the magpie that listed it (catalog.ViaHere), and that is A's
// own catalog.Self. So the fetch keeps, in remote-self.json, that this
// provider at this address is this magpie, and the provider is left out
// of routing and lists (SelfLeftOut), unless the user keeps it anyway
// (KeepSelf, the editor's "Use it anyway").
//
// The file is this computer's own: it is never in a backup or a sync, as
// B's copy of the provider points at A, which is not B. Self is made up
// again each run, so the record is what stands between runs, until the
// next fetch: one whose lister is another magpie, or one from before
// magpie_via, which names none, takes it away. An empty list or a failed
// fetch tells nothing and changes nothing. The record holds the address
// it was found at: a provider moved to another address is not this
// magpie until a fetch there says so.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// LeaveOutSelf is whether a Remote magpie found to be this magpie is left
// out. The gateway's tests serve their Remote magpie from their own
// process, and turn it off.
var LeaveOutSelf = true

func remoteSelfPath() string { return filepath.Join(settings.Dir(), "remote-self.json") }

var remoteSelfMu = filelock.Mutex{Path: func() string { return remoteSelfPath() + ".lock" }}

// remoteSelf is the file as read, by provider id the address it answered
// as this magpie at, for the settings folder it was read from.
var remoteSelf struct {
	sync.RWMutex
	path string
	ids  map[string]string
}

// readRemoteSelf reads the file. None yet is no record; one that can't be
// read or parsed is an error.
func readRemoteSelf(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// selfAt is the address provider id was found to be this magpie at, if
// it was. A file that can't be read records nothing: the provider is
// served as it was before, until a fetch writes the file again.
func selfAt(id string) (string, bool) {
	path := remoteSelfPath()
	remoteSelf.RLock()
	if remoteSelf.path == path {
		at, ok := remoteSelf.ids[id]
		remoteSelf.RUnlock()
		return at, ok
	}
	remoteSelf.RUnlock()
	remoteSelf.Lock()
	defer remoteSelf.Unlock()
	if remoteSelf.path != path {
		ids, err := readRemoteSelf(path)
		if err != nil {
			log.Printf("%s: %v", path, err)
			ids = map[string]string{}
		}
		remoteSelf.path, remoteSelf.ids = path, ids
	}
	at, ok := remoteSelf.ids[id]
	return at, ok
}

// anySelf says some provider was found to be this magpie: the fast way
// past ThroughSelf for everyone else.
func anySelf() bool {
	selfAt("") // loaded for this settings folder
	remoteSelf.RLock()
	defer remoteSelf.RUnlock()
	return len(remoteSelf.ids) > 0
}

// selfRoot is the address a record is kept by: the provider's Chat, as
// remoteMagpieEndpoints wrote it.
func (p Provider) selfRoot() string { return strings.TrimRight(strings.TrimSpace(p.Chat), "/") }

// SelfAnswered says this Remote magpie was found to be this magpie, at
// the address it has now.
func (p Provider) SelfAnswered() bool {
	if !p.IsRemoteMagpie() {
		return false
	}
	at, ok := selfAt(p.ID)
	return ok && at == p.selfRoot()
}

// SelfLeftOut is whether the provider is left out of routing and lists as
// a Remote magpie that is this magpie, which the user didn't keep.
func (p Provider) SelfLeftOut() bool {
	return LeaveOutSelf && !p.KeepSelf && p.SelfAnswered()
}

// noteSelf keeps what a Remote magpie's fetched list says of whether it is
// this magpie: the magpie that listed a model is its magpie_via's last.
func noteSelf(p Provider, ms []catalog.Model) {
	if !p.IsRemoteMagpie() || len(ms) == 0 {
		return
	}
	self := false
	for _, m := range ms {
		if len(m.Via) > 0 {
			self = m.Via[len(m.Via)-1] == catalog.Self()
			break
		}
	}
	at, was := selfAt(p.ID)
	if self == (was && at == p.selfRoot()) {
		return
	}
	remoteSelfMu.Lock()
	defer remoteSelfMu.Unlock()
	path := remoteSelfPath()
	ids, err := readRemoteSelf(path)
	if err != nil {
		// a record a fetch made, and the next fetch makes again
		log.Printf("%s: %v; written again", path, err)
		ids = map[string]string{}
	}
	if self {
		ids[p.ID] = p.selfRoot()
	} else {
		delete(ids, p.ID)
	}
	b, _ := json.MarshalIndent(ids, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("%s: %v", path, err)
		return
	}
	if err := steady.WriteFile(path, b, 0o600); err != nil {
		log.Printf("%s: %v", path, err)
		return
	}
	remoteSelf.Lock()
	remoteSelf.path, remoteSelf.ids = path, ids
	remoteSelf.Unlock()
	if self {
		log.Printf("%s: this Remote magpie is this magpie itself; left out of routing unless kept (Providers → %s → Use it anyway)", p.ID, p.Name)
	}
}

// ThroughSelf is the model id a request names with a Remote magpie that is
// this magpie, and left out, taken off the front: rem/own/m1 is own/m1
// here, as it is on the other computer, whose agents' picks sync brings
// over (hanslin1666).
func ThroughSelf(id string) string {
	if !anySelf() {
		return id
	}
	for range catalog.MaxVia {
		pid, rest, ok := strings.Cut(id, "/")
		if !ok || rest == "" {
			return id
		}
		if _, ok := selfAt(pid); !ok {
			return id
		}
		p, err := Find(pid)
		if err != nil || p.ID != pid || !p.SelfLeftOut() {
			return id
		}
		id = rest
	}
	return id
}

// SetKeepSelf keeps a Remote magpie that is this magpie in routing, or
// leaves it out again (Provider.KeepSelf).
func SetKeepSelf(id string, keep bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if !p.IsRemoteMagpie() {
		return errors.New(p.Name + " is not a Remote magpie")
	}
	p.KeepSelf = keep
	return Save(*p)
}
