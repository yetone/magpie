// Package bridgesid names the Claude Code sessions the Claude subscription
// bridge runs, so the local-session readers can tell them from the user's
// own.
//
// A bridge run is the real Claude Code, and it saves its session where
// Claude Code saves the user's: the account's projects folder, in the
// caller's folder's project. The readers of local sessions took it for a
// session of the user's, and the request the gateway already counted was
// counted again as "Claude Code · Local session", its tokens and cost
// twice (iwinux on X: a Pi request through a Claude subscription group
// listed twice since the bridge runs the interactive mode).
//
// Every session a bridge run writes gets an id New made: a version 4 UUID
// whose last six bytes are a keyed hash of its first ten. The key is this
// computer's own (a random file in magpie's config folder), so the id is as
// random as any to anyone without it, Anthropic, which Claude Code tells
// the session id, included, while magpie's readers check it with no list
// to keep: Is.
package bridgesid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// keyFile is the key's file in magpie's config folder.
const keyFile = "claude-bridge-session-key"

// recheck is how long a missing or unreadable key is believed missing
// before its file is looked at again: Is is asked for every Claude Code
// session file a listing finds.
const recheck = 10 * time.Second

var key struct {
	sync.Mutex
	dir     string // the config folder the key was read for
	k       []byte
	missing time.Time
}

// keyFor is the key in config folder dir, made there first when create is
// set and there is none. nil when there is none, or it can't be read: no
// id is then taken for a bridge's.
func keyFor(dir string, create bool) []byte {
	key.Lock()
	defer key.Unlock()
	if key.dir != dir {
		key.dir, key.k, key.missing = dir, nil, time.Time{}
	}
	if key.k != nil {
		return key.k
	}
	if !create && !key.missing.IsZero() && time.Since(key.missing) < recheck {
		return nil
	}
	path := filepath.Join(dir, keyFile)
	k, err := readKey(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		k, err = makeKey(path)
	}
	if err != nil {
		key.missing = time.Now()
		return nil
	}
	key.k, key.missing = k, time.Time{}
	return k
}

func readKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%s: not a key", path)
	}
	return k, nil
}

// makeKey writes a new key at path, unless another process wrote one
// first: then that one is the key.
func makeKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	// written whole under a name of its own, then linked to path: a reader
	// never sees half of it, and a second writer's link fails
	tmp, err := os.CreateTemp(filepath.Dir(path), keyFile+".*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(hex.EncodeToString(k) + "\n")
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readKey(path)
		}
		return nil, err
	}
	return k, nil
}

// mark is the keyed hash of a session id's first ten bytes.
func mark(k []byte, head []byte) [sha256.Size]byte {
	var in [32 + 10]byte
	copy(in[:], k)
	copy(in[32:], head)
	return sha256.Sum256(in[:])
}

// New is a session id for a bridge run: a version 4 UUID that Is knows.
// Without a key (magpie's config folder can't be written) it is a plain
// random one, which the readers take for the user's.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	if k := keyFor(appdir.Config(), true); k != nil {
		m := mark(k, b[:10])
		copy(b[10:], m[:6])
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Is says id is a session New made on this computer.
func Is(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	var b [16]byte
	n := 0
	for i := 0; i < len(id); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		v, ok := unhex(id[i])
		if !ok {
			return false
		}
		b[n/2] |= v << (4 * (1 - n%2))
		n++
	}
	k := keyFor(appdir.Config(), false)
	if k == nil {
		return false
	}
	m := mark(k, b[:10])
	return [6]byte(m[:6]) == [6]byte(b[10:])
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
