package access

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/settings"
)

// The join password (EV_John_Liu on X): while magpie is shared, another
// computer on the network opens the gateway's /join page, types this
// password and gets a gateway key of its own, with the setup for its
// agents. Only a salted PBKDF2-SHA256 hash of it is kept, in join.json
// beside caller-keys.json: this computer's own, never synced or backed up
// with the settings, and never shown again once set. A file that can't be
// read or parsed means join is closed: a failed read is unknown, never an
// open door.

// JoinMinLen is the fewest characters a join password may have.
const JoinMinLen = 8

// joinIter is PBKDF2's rounds; a variable so the tests run quickly.
var joinIter = 210_000

type joinFile struct {
	Salt []byte    `json:"salt"`
	Hash []byte    `json:"hash"`
	Iter int       `json:"iter"`
	Set  time.Time `json:"set"`
}

var joinMu sync.Mutex

// JoinPath is the file the join password's hash is kept in.
func JoinPath() string { return filepath.Join(settings.Dir(), "join.json") }

func readJoin() (joinFile, bool) {
	b, err := os.ReadFile(JoinPath())
	if err != nil {
		return joinFile{}, false
	}
	var f joinFile
	if json.Unmarshal(b, &f) != nil || len(f.Salt) < 16 || len(f.Hash) != sha256.Size || f.Iter < 1 {
		return joinFile{}, false
	}
	return f, true
}

// JoinOn: a join password is set, and readable.
func JoinOn() bool {
	joinMu.Lock()
	defer joinMu.Unlock()
	_, ok := readJoin()
	return ok
}

// JoinSince is when the join password was set; zero when none is.
func JoinSince() time.Time {
	joinMu.Lock()
	defer joinMu.Unlock()
	f, _ := readJoin()
	return f.Set
}

// SetJoinPassword sets the join password, or with "" clears it, which
// closes the join page. Keys already given out stay until each is removed
// in Gateway keys.
func SetJoinPassword(pw string) error {
	joinMu.Lock()
	defer joinMu.Unlock()
	if pw == "" {
		if err := os.Remove(JoinPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if utf8.RuneCountInString(pw) < JoinMinLen {
		return errors.New("Use a password of at least 8 characters")
	}
	if len(pw) > 1024 {
		return errors.New("Use a password of at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	h, err := pbkdf2.Key(sha256.New, pw, salt, joinIter, sha256.Size)
	if err != nil {
		return err
	}
	b, err := json.Marshal(joinFile{Salt: salt, Hash: h, Iter: joinIter, Set: time.Now().UTC()})
	if err != nil {
		return err
	}
	return writePrivate(JoinPath(), b)
}

// JoinPasswordOK: pw is the join password. None set, or a file that can't
// be read, lets no password in.
func JoinPasswordOK(pw string) bool {
	joinMu.Lock()
	f, ok := readJoin()
	joinMu.Unlock()
	if !ok || pw == "" || len(pw) > 1024 {
		return false
	}
	h, err := pbkdf2.Key(sha256.New, pw, f.Salt, f.Iter, sha256.Size)
	return err == nil && subtle.ConstantTimeCompare(h, f.Hash) == 1
}

// writePrivate writes b to path through a temp file renamed over it,
// readable by this user alone.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
