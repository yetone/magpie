package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// With sync not to S3 and the archive's switch on at this computer
// (CyberCaius on Discord), a call is kept in magpie's folder, the same file
// the bucket gets: its secrets taken out, its id on the response and the
// call, written whole through a temporary file, readable by this user only.
func TestArchiveOnThisComputer(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	was := ArchiveBucket
	ArchiveBucket = func() (Putter, bool) { return nil, false } // sync to WebDAV, or none
	t.Cleanup(func() { ArchiveBucket = was })
	settings.Save(settings.Settings{RequestArchiveLocal: true})
	s := New()
	rec := archivePost(t, s)
	id := rec.Header().Get(ArchiveHeader)
	c := s.Recent()[0]
	name := c.Time.UTC().Format("2006-01-02") + "/" + id
	if id == "" || c.Archive != name {
		t.Fatalf("not archived: id %q, call %q", id, c.Archive)
	}
	path := filepath.Join(settings.Dir(), "request-archive", filepath.FromSlash(name)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("not on this computer: %v", err)
	}
	for _, secret := range []string{archiveKey, "named-secret", "magpie-gateway-key", "another-secret", "cookie-secret", "abc123", "query-secret"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("%s in the archive:\n%s", secret, data)
		}
	}
	var a Archived
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != name || a.Call.Model != "fake/m1" || a.Request.Headers["Authorization"][0] != "[REDACTED]" ||
		!strings.Contains(a.Request.Body, "deploy with [REDACTED:API_KEY]") || !strings.Contains(a.Response.Body, `"content":"done"`) {
		t.Errorf("archived %+v", a)
	}
	// what went upstream for it, and the head of what came back, masked
	// as the rest (usrname1833 on Discord)
	if len(a.Upstream) != 1 || a.Upstream[0].Status != 200 || len(a.Upstream[0].Request["Authorization"]) != 1 ||
		a.Upstream[0].Request["Authorization"][0] != "[REDACTED]" || len(a.Upstream[0].Response["Set-Cookie"]) != 1 {
		t.Errorf("upstream %+v", a.Upstream)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	// read back by its name, as the Usage page asks for it
	date, aid, _ := strings.Cut(name, "/")
	file, _ := ArchiveName(date, aid)
	r, size, err := LocalArchive().Open(context.Background(), file)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("Open: %d bytes, %v", size, err)
	}
	r.Close()
	if _, _, err := LocalArchive().Open(context.Background(), "archive/2026-10-02/101010-00000000000000bb.json"); err != ErrNotArchived {
		t.Errorf("a call never kept: %v", err)
	}
}

// Request bodies are written to a disk only where the user turned the
// archive on at that computer: RequestArchive, which sync carries from a
// computer with an S3 bucket, keeps nothing on one with none, and the
// local switch sends nothing to the disk while a bucket is there.
func TestArchiveNeverOnDiskUnasked(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	was := ArchiveBucket
	t.Cleanup(func() { ArchiveBucket = was })
	dir := filepath.Join(settings.Dir(), "request-archive")
	onDisk := func() []string {
		var got []string
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				got = append(got, p)
			}
			return nil
		})
		return got
	}

	// off, as it is unless turned on
	ArchiveBucket = func() (Putter, bool) { return nil, false }
	s := New()
	if rec := archivePost(t, s); rec.Header().Get(ArchiveHeader) != "" || len(onDisk()) != 0 {
		t.Fatalf("archived while off: %v", onDisk())
	}
	// the S3 archive's switch, synced here, with no bucket here
	settings.Save(settings.Settings{RequestArchive: true})
	if rec := archivePost(t, s); rec.Header().Get(ArchiveHeader) != "" || len(onDisk()) != 0 {
		t.Fatalf("the synced switch wrote to this computer: %v", onDisk())
	}
	// the local switch with a bucket there: the bucket's switch decides
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	settings.Save(settings.Settings{RequestArchiveLocal: true})
	if rec := archivePost(t, s); rec.Header().Get(ArchiveHeader) != "" || len(onDisk()) != 0 || len(b.objs) != 0 {
		t.Fatalf("archived with the bucket's switch off: disk %v, bucket %v", onDisk(), keys(b.objs))
	}
	settings.Save(settings.Settings{RequestArchive: true, RequestArchiveLocal: true})
	if archivePost(t, s); len(b.objs) != 1 || len(onDisk()) != 0 {
		t.Fatalf("with a bucket: bucket %v, disk %v", keys(b.objs), onDisk())
	}
}

// Past its size the folder loses its oldest calls first, down to a tenth
// under it; the call just kept stays, and a temporary file a write left an
// hour ago goes.
func TestLocalArchiveRemovesOldestPastItsSize(t *testing.T) {
	d := &DiskArchive{Dir: t.TempDir(), cap: func() int64 { return 1000 }}
	ctx := context.Background()
	body := []byte(strings.Repeat("x", 200))
	name := func(day, i int) string {
		return fmt.Sprintf("archive/2026-10-%02d/%06d-00000000000000aa.json", day, 100000+i)
	}
	stale := filepath.Join(d.Dir, "2026-10-01", ".old.json-1.part")
	os.MkdirAll(filepath.Dir(stale), 0o700)
	os.WriteFile(stale, body, 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, old, old)
	// four on the 1st, one on the 2nd: 1000 bytes, at the size
	for i := range 4 {
		if err := d.Put(ctx, name(1, i), body); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Put(ctx, name(2, 0), body); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("an hour-old temporary file is still there")
	}
	has := func(n string) bool {
		r, _, err := d.Open(ctx, n)
		if err == nil {
			r.Close()
		}
		return err == nil
	}
	for i := range 4 {
		if !has(name(1, i)) {
			t.Fatalf("removed at the size, not past it: %s", name(1, i))
		}
	}
	// one more is past it: the two oldest go, 800 bytes left
	if err := d.Put(ctx, name(2, 1), body); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{false, false, true, true} {
		if has(name(1, i)) != want {
			t.Errorf("%s kept %v, want %v", name(1, i), !want, want)
		}
	}
	if !has(name(2, 0)) || !has(name(2, 1)) {
		t.Error("a newer call was removed")
	}
	// one alone larger than the size is kept, and every other goes
	big := []byte(strings.Repeat("y", 1500))
	if err := d.Put(ctx, name(3, 0), big); err != nil {
		t.Fatal(err)
	}
	if !has(name(3, 0)) {
		t.Error("the call just kept was removed")
	}
	for _, n := range []string{name(1, 2), name(1, 3), name(2, 0), name(2, 1)} {
		if has(n) {
			t.Errorf("%s kept past the size", n)
		}
	}
	if days, _ := os.ReadDir(d.Dir); len(days) != 1 {
		t.Errorf("empty days left: %v", days)
	}
	r, _, _ := d.Open(ctx, name(3, 0))
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != string(big) {
		t.Errorf("read back %d bytes", len(got))
	}
	// a name not of the archive's shape is never a path
	for _, n := range []string{"archive/../../settings.json", "archive/2026-10-02/../x.json", "settings.json", "archive/2026-10-02/ABC.json"} {
		if err := d.Put(ctx, n, body); err == nil {
			t.Errorf("Put %s", n)
		}
		if _, _, err := d.Open(ctx, n); err != ErrNotArchived {
			t.Errorf("Open %s: %v", n, err)
		}
	}
}

func TestLocalArchiveCap(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := LocalArchiveCap(); got != 1<<30 {
		t.Errorf("default %d, want a GiB", got)
	}
	settings.Save(settings.Settings{RequestArchiveLocalMB: 5})
	if got := LocalArchiveCap(); got != 5<<20 {
		t.Errorf("5 MiB: %d", got)
	}
}
