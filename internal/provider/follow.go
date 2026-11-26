package provider

import (
	"context"
	"crypto/sha256"
	"log"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/steady"
)

type storedWrite struct {
	path string
	sum  [sha256.Size]byte
}

// ownWrite protects the write and its record so FollowEdits reads the file
// and the record on the same side of a save.
var ownWrite struct {
	sync.Mutex
	last *storedWrite
}

// writeStored records the bytes passed to store's write. Reading the path
// back here could instead identify another process's edit as our own.
func writeStored(path string, b []byte) error {
	ownWrite.Lock()
	defer ownWrite.Unlock()
	if err := writePrivate(path, b); err != nil {
		return err
	}
	ownWrite.last = &storedWrite{path, sha256.Sum256(b)}
	return nil
}

// FollowEdits has an edit of providers.json made outside this magpie (by
// hand, or another magpie) reach the agents' own files as a save in the
// window does (catalog.Touched): the gateway reads the file again on its
// next request, but the model lists Claude Code, Codex and the rest keep
// in files of their own were written only on a save of magpie's, so a
// model added by hand wasn't offered until then (sarosaka1023 on X). An
// edit that doesn't parse changes nothing: magpie runs on the last good
// generation (read), says so, and leaves the file to be fixed. It looks
// when Watch is told of a change, and every so often (every) for one it
// missed, until ctx ends. It takes the baseline before returning the loop
// to start, so an edit made before the goroutine runs is still seen.
func FollowEdits(ctx context.Context, every func() time.Duration) func() {
	path := Path()
	written := filememo.Written()
	var last [sha256.Size]byte
	b, lastWrite, err := followedFile(path)
	if err == nil {
		last = sha256.Sum256(b)
	} else if lastWrite != nil && lastWrite.path == path {
		last = lastWrite.sum
	}
	return func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-beat.Next(every()):
			case <-written:
			}
			// Subscribe before reading so a write during the check wakes us again.
			written = filememo.Written()
			b, saved, err := followedFile(path)
			if err != nil {
				continue
			}
			if saved != lastWrite {
				lastWrite = saved
				if saved != nil && saved.path == path {
					last = saved.sum // store tells agents about this write
				}
			}
			sum := sha256.Sum256(b)
			if sum == last {
				continue
			}
			last = sum
			if !validProviders(b) {
				log.Printf("%s was edited outside magpie and doesn't parse: magpie runs on its last good copy and saves nothing over it until it is fixed", path)
				continue
			}
			// The bytes changed even if filememo's time and size did not.
			filememo.DropKind("providers")
			catalog.Touched()
		}
	}
}

func followedFile(path string) ([]byte, *storedWrite, error) {
	ownWrite.Lock()
	defer ownWrite.Unlock()
	b, err := steady.ReadFile(path)
	return b, ownWrite.last, err
}
