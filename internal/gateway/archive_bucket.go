package gateway

// The request archive in the bucket is kept to a size, as the one on this
// computer is (archive_local.go): RequestArchiveMB, a GiB unless set. Past
// it the oldest calls are removed, down to a tenth under it. Before this
// the bucket was never pruned, and a month of whole bodies grew one by
// 18 GB (an R2 user on X).
//
// Only objects magpie made are ever removed: under the archive's folder,
// archive/<date>/<id>.json, of ArchiveName's shape. The backup, shared
// usage and anything else in the bucket are never listed or touched.
//
// The bucket is listed once the serving magpie starts (so a bucket grown
// before this is brought under the size whether the archive is on or not),
// and after that only once a tenth of the size more has been uploaded,
// counted as uploads go: one LIST page for a thousand calls, never one
// per call.

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// archiveKeeper is a bucket that can be listed and removed from: the S3
// sync's (davsync.Bucket).
type archiveKeeper interface {
	Each(ctx context.Context, prefix string, f func(name string, size int64)) error
	Delete(ctx context.Context, name string) error
	Where() string
}

// BucketArchiveCap is how large the archive in the bucket grows, in bytes:
// settings' RequestArchiveMB, a GiB unless set.
func BucketArchiveCap() int64 {
	mb := settings.Shared().RequestArchiveMB
	if mb <= 0 {
		mb = 1024
	}
	return int64(mb) << 20
}

// bucketKept is what the archive in the bucket is known to hold.
var bucketKept struct {
	mu      sync.Mutex
	where   string // the bucket counted
	total   int64  // its bytes, as last listed and uploaded since
	known   bool
	pruning bool
	again   bool // another pass is wanted once this one ends
}

// bucketCap is the size; a test's own when set.
var bucketCap = BucketArchiveCap

// archiveFirstPrune is how long after the gateway starts the bucket is
// first listed: once start's own work is done.
var archiveFirstPrune = 2 * time.Minute

// KeepBucketArchiveBounded brings the archive in the bucket under its size
// once the serving magpie has started, for a bucket grown before the size
// was kept: on or off, the archive's own objects past the size go.
func KeepBucketArchiveBounded(ctx context.Context) {
	t := time.NewTimer(archiveFirstPrune)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
	}
	if ArchiveBucket == nil {
		return
	}
	b, ok := ArchiveBucket()
	if !ok || b == nil {
		return
	}
	if k, ok := b.(archiveKeeper); ok {
		pruneBucketArchive(ctx, k, "")
	}
}

// bucketAdded counts n bytes just uploaded at kept, and starts a pass that
// removes the oldest calls when the count is past the size, or not known.
func bucketAdded(k archiveKeeper, kept string, n int64) {
	bucketKept.mu.Lock()
	if bucketKept.where != k.Where() {
		bucketKept.where, bucketKept.known = k.Where(), false
	}
	if bucketKept.known {
		bucketKept.total += n
		if bucketKept.total <= bucketCap() {
			bucketKept.mu.Unlock()
			return
		}
	}
	bucketKept.mu.Unlock()
	// its own goroutine: a bucket of tens of thousands of calls takes a
	// while to bring under the size, and uploads go on meanwhile
	archivePending.Add(1)
	go func() {
		defer archivePending.Done()
		pruneBucketArchive(context.Background(), k, kept)
	}()
}

// pruneBucketArchive lists the archive in k and removes its oldest calls
// past the size, down to a tenth under it; never kept, the call just
// uploaded. One pass at a time: a pass asked for during one runs after it.
func pruneBucketArchive(ctx context.Context, k archiveKeeper, kept string) {
	bucketKept.mu.Lock()
	if bucketKept.pruning {
		bucketKept.again = true
		bucketKept.mu.Unlock()
		return
	}
	bucketKept.pruning = true
	bucketKept.mu.Unlock()
	for {
		total, err := pruneBucketOnce(ctx, k, kept)
		bucketKept.mu.Lock()
		if err != nil {
			log.Printf("request archive: keeping %s under %d MiB: %v", k.Where(), bucketCap()>>20, err)
			// counted as known all the same, so a bucket that can't be
			// listed is tried again only a tenth of the size later, not
			// on every upload
		}
		bucketKept.where, bucketKept.total, bucketKept.known = k.Where(), total, true
		if !bucketKept.again || ctx.Err() != nil {
			bucketKept.pruning, bucketKept.again = false, false
			bucketKept.mu.Unlock()
			return
		}
		bucketKept.again = false
		bucketKept.mu.Unlock()
	}
}

type bucketCall struct {
	name string
	size int64
}

// pruneBucketOnce is one pass: the archive's bytes after it.
func pruneBucketOnce(ctx context.Context, k archiveKeeper, kept string) (int64, error) {
	var calls []bucketCall
	var total int64
	err := k.Each(ctx, "archive/", func(name string, size int64) {
		if !archiveObject(name) {
			return // not one magpie made: never counted, never removed
		}
		calls = append(calls, bucketCall{name, size})
		total += size
	})
	if err != nil {
		return total, err
	}
	most := bucketCap()
	if total <= most {
		return total, nil
	}
	// "archive/<date>/<HHMMSS>-…" sorts by when the call was made
	sort.Slice(calls, func(i, j int) bool { return calls[i].name < calls[j].name })
	goal := most - most/10
	var gone []bucketCall
	for _, c := range calls {
		if total <= goal {
			break
		}
		if c.name == kept {
			continue
		}
		gone = append(gone, c)
		total -= c.size
	}
	// a few at a time: a bucket grown to tens of GB has tens of thousands
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		back    int64 // the bytes of calls not removed after all
		lastErr error
	)
	jobs := make(chan bucketCall)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := k.Delete(cctx, c.name)
				cancel()
				if err != nil {
					mu.Lock()
					back += c.size
					lastErr = err
					mu.Unlock()
				}
			}
		}()
	}
	var unsent int64
	for i, c := range gone {
		if ctx.Err() != nil {
			for _, r := range gone[i:] {
				unsent += r.size
			}
			break
		}
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	back += unsent
	if ctx.Err() != nil {
		return total + back, ctx.Err()
	}
	if len(gone) > 0 && back == 0 {
		log.Printf("request archive: removed the %d oldest calls in %s, kept under %d MiB", len(gone), k.Where(), most>>20)
	}
	return total + back, lastErr
}

// archiveObject says whether name, as the bucket lists it, is a call
// magpie archived: "archive/<date>/<id>.json" as ArchiveName makes it.
func archiveObject(name string) bool {
	rest, ok := strings.CutPrefix(name, "archive/")
	date, base, ok2 := strings.Cut(rest, "/")
	id, ok3 := strings.CutSuffix(base, ".json")
	if !ok || !ok2 || !ok3 {
		return false
	}
	n, ok := ArchiveName(date, id)
	return ok && n == name
}
