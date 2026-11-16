package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// keptBucket is a bucket in memory that can be listed and removed from,
// as the S3 sync's is.
type keptBucket struct {
	memBucket
	where          string
	lists, deletes int
	refuse         string // a name Delete fails on
}

func (b *keptBucket) Each(_ context.Context, prefix string, f func(string, int64)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lists++
	for k, v := range b.objs {
		if strings.HasPrefix(k, prefix) {
			f(k, int64(len(v)))
		}
	}
	return nil
}

func (b *keptBucket) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes++
	if name == b.refuse {
		return fmt.Errorf("HTTP 403 AccessDenied")
	}
	delete(b.objs, name)
	return nil
}

func (b *keptBucket) Where() string { return b.where }

// The serving magpie brings a bucket grown before under the archive's size
// once it has started, the archive on or off: the oldest calls go, down to
// a tenth under the size, and nothing but calls of the archive's shape is
// listed as one or removed.
func TestBucketArchiveBroughtUnderItsSizeAtStart(t *testing.T) {
	fresh(t)
	settings.Save(settings.Settings{}) // the archive off
	b := &keptBucket{memBucket: memBucket{objs: map[string][]byte{}}, where: t.Name()}
	wasB, wasCap, wasFirst := ArchiveBucket, bucketCap, archiveFirstPrune
	ArchiveBucket = func() (Putter, bool) { return b, true }
	bucketCap = func() int64 { return 1000 }
	archiveFirstPrune = 0
	t.Cleanup(func() { ArchiveBucket, bucketCap, archiveFirstPrune = wasB, wasCap, wasFirst })
	call := func(day, i int) string {
		return fmt.Sprintf("archive/2026-10-%02d/%06d-%016x.json", day, 100000+i, i)
	}
	body := []byte(strings.Repeat("x", 100))
	for day := 1; day <= 3; day++ {
		for i := range 10 {
			b.objs[call(day, i)] = body // 3000 bytes
		}
	}
	b.refuse = call(1, 3)
	foreign := []string{"magpie.magpie-backup", "usage/a.magpie-usage", "archive/notes.txt",
		"archive/2026-10-01/README.md", "archive/2026-10-01/ABC.json", "archive/x/2026-10-01/000000-00.json"}
	for _, k := range foreign {
		b.objs[k] = []byte(strings.Repeat("f", 5000))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	KeepBucketArchiveBounded(ctx)

	// down to 900: the 21 oldest go, but the one the bucket refused
	// stays, and the pass ends at 1000, at the size
	var kept []string
	var total int
	for k, v := range b.objs {
		if archiveObject(k) {
			kept = append(kept, k)
			total += len(v)
		}
	}
	if total > 1000 || len(kept) != 10 {
		t.Fatalf("kept %d calls, %d bytes: %v", len(kept), total, kept)
	}
	if _, ok := b.objs[call(1, 3)]; !ok {
		t.Error("the call the bucket refused to remove is gone")
	}
	if _, ok := b.objs[call(3, 0)]; ok {
		t.Error("the 21st oldest call kept")
	}
	for i := 1; i < 10; i++ {
		if _, ok := b.objs[call(3, i)]; !ok {
			t.Errorf("a newer call removed: %s", call(3, i))
		}
	}
	for _, k := range foreign {
		if _, ok := b.objs[k]; !ok {
			t.Errorf("%s removed", k)
		}
	}
	if b.lists != 1 {
		t.Errorf("%d LISTs", b.lists)
	}

	// under the size, a start lists once and removes nothing
	b.deletes = 0
	KeepBucketArchiveBounded(ctx)
	if b.deletes != 0 || b.lists != 2 {
		t.Errorf("under the size: %d DELETEs, %d LISTs", b.deletes, b.lists)
	}
}

// On, each upload is counted: the bucket is listed again only once a
// tenth of the size more is uploaded, and the call just uploaded is never
// the one removed, though it alone be over the size.
func TestBucketArchiveListedPerTenthUploaded(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	settings.Save(settings.Settings{RequestArchive: true})
	b := &keptBucket{memBucket: memBucket{objs: map[string][]byte{}}, where: t.Name()}
	wasB, wasCap := ArchiveBucket, bucketCap
	ArchiveBucket = func() (Putter, bool) { return b, true }
	var most int64 = 1 << 30
	var mu sync.Mutex
	bucketCap = func() int64 { mu.Lock(); defer mu.Unlock(); return most }
	t.Cleanup(func() { ArchiveBucket, bucketCap = wasB, wasCap })
	s := New()
	archivePost(t, s)
	one := 0
	for _, v := range b.objs {
		one = len(v)
	}
	if b.lists != 1 {
		t.Fatalf("the first upload: %d LISTs, want 1 to count the bucket", b.lists)
	}
	// a size of ten calls: a tenth is one call, so past the size every
	// call or two lists, never more than once a call
	mu.Lock()
	most = int64(10 * one)
	mu.Unlock()
	for range 40 {
		archivePost(t, s)
	}
	n := 0
	for k := range b.objs {
		if archiveObject(k) {
			n++
		}
	}
	if n > 10 || n < 8 || b.lists > 41 {
		t.Errorf("%d calls kept, %d LISTs", n, b.lists)
	}
	// well under the size, uploads list nothing
	mu.Lock()
	most = 1 << 30
	mu.Unlock()
	archivePost(t, s) // counted, under: no LIST
	lists := b.lists
	for range 20 {
		archivePost(t, s)
	}
	if b.lists != lists {
		t.Errorf("%d LISTs for 20 calls under the size", b.lists-lists)
	}
	// one call alone over the size is kept
	mu.Lock()
	most = 1
	mu.Unlock()
	rec := archivePost(t, s)
	id := rec.Header().Get(ArchiveHeader)
	found := false
	for k := range b.objs {
		if strings.HasSuffix(k, "/"+id+".json") {
			found = true
		}
	}
	if !found {
		t.Error("the call just uploaded was removed")
	}
}

func TestArchiveObject(t *testing.T) {
	for name, want := range map[string]bool{
		"archive/2026-10-01/101010-00ff.json":    true,
		"archive/2026-10-01/101010-00FF.json":    false,
		"archive/2026-10-01/101010-00ff.json.gz": false,
		"archive/2026-13-01/101010-00ff.json":    false,
		"archive/2026-10-01/a/b.json":            false,
		"archive/notes.json":                     false,
		"other/2026-10-01/101010-00ff.json":      false,
		"archive/2026-10-01/../x.json":           false,
	} {
		if archiveObject(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}
