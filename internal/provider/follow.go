package provider

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/steady"
)

// ownWrite is providers.json as this process last wrote it (store): a
// change FollowEdits finds that isn't this is another's. Held across the
// write and its stamp, so the file is never looked at between them.
var ownWrite struct {
	sync.Mutex
	stamp rootStamp
}

// writeStored is store's write, its stamp kept as this process's own.
func writeStored(path string, b []byte) error {
	ownWrite.Lock()
	defer ownWrite.Unlock()
	if err := writePrivate(path, b); err != nil {
		return err
	}
	ownWrite.stamp = stampOf(path)
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
// missed, until ctx ends.
func FollowEdits(ctx context.Context, every func() time.Duration) {
	last := stampOf(Path())
	for {
		// taken before the look, so a write made during it isn't missed
		written := filememo.Written()
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(every()):
		case <-written:
		}
		ownWrite.Lock()
		st := stampOf(Path())
		ours := st == ownWrite.stamp
		ownWrite.Unlock()
		if st == last {
			continue
		}
		last = st
		if ours || !st.ok {
			continue
		}
		b, err := steady.ReadFile(Path())
		if err != nil {
			continue
		}
		if !validProviders(b) {
			// read says so, once, and gives the last good generation
			log.Printf("%s was edited outside magpie and doesn't parse: magpie runs on its last good copy and saves nothing over it until it is fixed", Path())
			continue
		}
		catalog.Touched()
	}
}
