package agent

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/provider"
)

// The stash remembers what an agent's config said before magpie pointed it
// at the gateway, so switching back restores it instead of guessing.
//
// stash.json holds every agent's originals, so a change to it is read,
// changed and written under stashMu, and written whole (edit.WriteAtomic):
// two changes at once, or one cut short, never leave it half there, which
// the next change read as no stash and wrote back over every agent's. A
// file that doesn't parse (edited by hand, or half written by an older
// magpie) is copied aside before it is written over, so what it held can
// still be got back.
var stashMu sync.Mutex

func stashPath() string { return filepath.Join(filepath.Dir(provider.Path()), "stash.json") }

// stashLoad is the stash as a map of the caller's own, to change and write
// back.
func stashLoad() map[string]string {
	m := maps.Clone(stashRead(filememo.ReadNow[map[string]string]))
	if m == nil {
		m = map[string]string{}
	}
	return m
}

// stashShared is the stash for a look at its values: shared with every
// other look, so never changed. Each window poll looks at it hundreds of
// times, and a fresh read and parse for each was 35 MB of what twenty
// polls allocated.
func stashShared() map[string]string {
	return stashRead(filememo.Read[map[string]string])
}

func stashRead(read func(kind, path string, parse func([]byte) (map[string]string, error)) (map[string]string, error)) map[string]string {
	m, _ := read("stash", stashPath(), func(b []byte) (map[string]string, error) {
		out := map[string]string{}
		json.Unmarshal(b, &out)
		return out, nil
	})
	return m
}

// stashWrite writes m as the stash, a file there that doesn't parse kept
// aside first. The caller holds stashMu.
func stashWrite(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := stashPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// stashLoad reads a file that doesn't unmarshal into the stash (a JSON
	// array too, not only one cut short) as empty: it is copied aside, and
	// one that can't be copied isn't written over
	if err := lastgood.KeepUnparsed(p, lastgood.Into[map[string]string]); err != nil {
		log.Printf("stash.json wasn't written over: %v", err)
		return err
	}
	// made 0600 first: WriteAtomic keeps a file's mode, and the stash holds
	// keys
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	return edit.WriteAtomic(p, b)
}

func stash(kv map[string]string) {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	for k, v := range kv {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	stashWrite(m)
}

// forget drops stashed values without restoring them.
func forget(keys ...string) {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	for _, k := range keys {
		delete(m, k)
	}
	stashWrite(m)
}

func unstash(key string) string {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	v := m[key]
	if v != "" {
		delete(m, key)
		stashWrite(m)
	}
	return v
}

// peek reads stashed values without taking them out, for a change that
// writes the config first and only then takes the originals out of the
// stash (#4258): a write that fails leaves them stashed for the next try.
func peek(keys ...string) map[string]string {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := m[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

// forgetValues takes peeked values out of the stash once the write that
// replaces them has gone through, each only while it is still the one
// peeked: a value written since, here or by another magpie, stays stashed.
func forgetValues(peeked map[string]string) {
	if len(peeked) == 0 {
		return
	}
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	dropped := false
	for k, v := range peeked {
		if m[k] == v {
			delete(m, k)
			dropped = true
		}
	}
	if dropped {
		stashWrite(m)
	}
}
