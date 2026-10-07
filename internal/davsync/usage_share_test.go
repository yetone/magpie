package davsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// usageDAV is fakeDAV with what sharing usage asks of a server besides:
// a folder listed (PROPFIND, Depth 1) and a file deleted. It counts the
// requests made of the usage folder, and the writes of the backup.
type usageDAV struct {
	*fakeDAV
	finds, backupPuts int
}

func (d *usageDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case "PROPFIND":
		d.mu.Lock()
		defer d.mu.Unlock()
		d.finds++
		dir := strings.TrimSuffix(r.URL.Path, "/")
		if !d.dirs[dir] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
		fmt.Fprintf(&b, `<D:response><D:href>%s/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`, dir)
		for p, f := range d.files {
			if urlDir(p) == dir {
				fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype/><D:getetag>%s</D:getetag><D:getcontentlength>%d</D:getcontentlength></D:prop></D:propstat></D:response>`, p, d.etags[p], len(f))
			}
		}
		b.WriteString(`</D:multistatus>`)
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(b.String()))
	case http.MethodDelete:
		d.mu.Lock()
		defer d.mu.Unlock()
		if _, ok := d.files[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(d.files, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		if r.Method == http.MethodPut && r.URL.Path == "/dav/magpie/magpie.magpie-backup" {
			d.mu.Lock()
			d.backupPuts++
			d.mu.Unlock()
		}
		d.fakeDAV.ServeHTTP(w, r)
	}
}

// usageFiles are the usage files on the server, by name.
func (d *usageDAV) usageFiles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for p := range d.files {
		if name, ok := strings.CutPrefix(p, "/dav/magpie/usage/"); ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func call(at time.Time, model string, in int) usage.Record {
	return usage.Record{Time: at, Agent: "claude", Provider: "deepseek", Model: model, Input: in, Output: 10, Status: 200, Millis: 900}
}

// Two computers syncing to one WebDAV folder, both sharing usage (#542):
// each sees the other's calls beside its own, named by computer, the total
// theirs together; syncing again brings nothing twice; a new call on one
// reaches the other at its next sync; neither writes the other's files,
// nor the backup again for them.
func TestUsageSharedBetweenComputers(t *testing.T) {
	old := usageEvery
	usageEvery = 0
	t.Cleanup(func() { usageEvery = old })
	fake := &usageDAV{fakeDAV: &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true, Usage: true}
	sync := func(t *testing.T) {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		if v := Status(); v.UsageError != "" || !v.Usage {
			t.Fatalf("usage not shared: %+v", v)
		}
	}
	page := func(t *testing.T, f usage.Filter) usage.RequestPage {
		t.Helper()
		return usage.QueryPage(usage.All, f, 0, 100)
	}
	// the usage clock held at noon, so that a's calls of today are of one
	// day whenever the test runs
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	oldClock := usage.Clock
	usage.Clock = func() time.Time { return now }
	t.Cleanup(func() { usage.Clock = oldClock })

	// a: two calls today, one two days ago, and one another magpie passed
	// on to it, which that one counts itself
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	usage.Append(call(now.Add(-2*time.Minute), "deepseek-chat", 100))
	usage.Append(call(now.Add(-time.Minute), "deepseek-chat", 200))
	usage.Append(call(now.AddDate(0, 0, -2), "deepseek-reasoner", 400))
	via := call(now.Add(-30*time.Second), "deepseek-chat", 5000)
	via.Via = "laptop"
	usage.Append(via)
	sync(t)
	aID, _ := usage.Computer()
	files := fake.usageFiles()
	if len(files) != 2 || !strings.HasPrefix(files[0], aID+"-") || !strings.HasSuffix(files[0], usageExt) {
		t.Fatalf("a's days on the server: %v", files)
	}
	for _, f := range files {
		if body := string(fake.files["/dav/magpie/usage/"+f]); strings.Contains(body, "deepseek") || strings.Contains(body, `"calls"`) {
			t.Fatalf("%s isn't sealed: %.200s", f, body)
		}
	}
	// a, alone: what it shows is as it was, no computer named
	if p := page(t, usage.Filter{}); p.Total != 4 || p.Computers != nil || p.Names != nil {
		t.Fatalf("a alone: %d rows, computers %v", p.Total, p.Computers)
	}

	// b: one call of its own; it sees a's three, not the one passed on
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	usage.Append(call(now.Add(-90*time.Second), "deepseek-chat", 7))
	sync(t)
	bID, _ := usage.Computer()
	if bID == aID {
		t.Fatal("two computers, one id")
	}
	check := func(t *testing.T, total, input int) usage.RequestPage {
		t.Helper()
		p := page(t, usage.Filter{})
		if p.Total != total || p.Sum.Input != input {
			t.Fatalf("all computers: %d rows, %d in; want %d, %d", p.Total, p.Sum.Input, total, input)
		}
		return p
	}
	p := check(t, 4, 707)
	if p.Names[aID] == "" || len(p.Computers) != 2 {
		t.Fatalf("computers: %v, names %v", p.Computers, p.Names)
	}
	for _, r := range p.Rows {
		if (r.Computer == aID) != (r.Input != 7) {
			t.Fatalf("row %d in said of computer %q", r.Input, r.Computer)
		}
	}
	if p := page(t, usage.Filter{Computer: usage.ThisComputer}); p.Total != 1 || p.Sum.Input != 7 {
		t.Fatalf("this computer: %d rows, %d in", p.Total, p.Sum.Input)
	}
	if p := page(t, usage.Filter{Computer: aID}); p.Total != 3 || p.Sum.Input != 700 {
		t.Fatalf("a: %d rows, %d in", p.Total, p.Sum.Input)
	}
	if p := page(t, usage.Filter{Computer: usage.OtherComputers}); p.Total != 3 {
		t.Fatalf("other computers: %d rows", p.Total)
	}
	// again and again: nothing twice, and the backup not written for it
	puts := fake.backupPuts
	sync(t)
	sync(t)
	check(t, 4, 707)
	if fake.backupPuts != puts {
		t.Fatalf("the backup was written %d times more for usage", fake.backupPuts-puts)
	}
	// the ledger read whole (the CSV's, and a reader of session logs given)
	usage.LogCalls = func(time.Time) []sessions.Call { return nil }
	l := usage.LedgerOf(usage.All, usage.Filter{})
	q := page(t, usage.Filter{})
	usage.LogCalls = nil
	if len(l.Rows) != 4 || q.Total != 4 || len(q.Computers) != 2 {
		t.Fatalf("whole ledger: %d rows; page %d, computers %v", len(l.Rows), q.Total, q.Computers)
	}

	// a, synced: b's call is there; a new call of a's goes up
	a.use(t)
	sync(t)
	if p := page(t, usage.Filter{}); p.Total != 5 || p.Sum.Input != 5707 {
		t.Fatalf("a with b's: %d rows, %d in", p.Total, p.Sum.Input)
	}
	usage.Append(call(now, "deepseek-chat", 30))
	sync(t)
	// a day of a's past the window, as one long ago left it: a takes it away
	gone := "/dav/magpie/usage/" + aID + "-2020-01-01" + usageExt
	fake.mu.Lock()
	fake.files[gone], fake.etags[gone] = []byte("old"), `"old"`
	fake.mu.Unlock()
	sync(t)
	if _, ok := fake.files[gone]; ok {
		t.Fatal("a's day past the window is still on the server")
	}
	b.use(t)
	sync(t)
	check(t, 5, 737)
	for _, f := range fake.usageFiles() {
		if !strings.HasPrefix(f, aID+"-") && !strings.HasPrefix(f, bID+"-") {
			t.Fatalf("a file of no computer's: %s", f)
		}
	}

	// b turns its sharing off: a's calls are shown there no more
	off := cfg
	off.Usage = false
	if err := Configure(off); err != nil {
		t.Fatal(err)
	}
	if err := SyncNow(ctx); err != nil {
		t.Fatal(err)
	}
	if p := page(t, usage.Filter{}); p.Total != 1 || p.Computers != nil || len(usage.SharedKept()) != 0 {
		t.Fatalf("b, sharing off: %d rows, computers %v, kept %v", p.Total, p.Computers, usage.SharedKept())
	}
	// a turns sync off altogether: b's calls go with it
	a.use(t)
	if err := Off(); err != nil {
		t.Fatal(err)
	}
	if p := page(t, usage.Filter{}); p.Computers != nil || len(usage.SharedKept()) != 0 {
		t.Fatalf("a, sync off: computers %v, kept %v", p.Computers, usage.SharedKept())
	}
}

// Usage left off — as every setup made before it could be, and an older
// magpie's — asks nothing of the usage folder, and the Usage page names no
// computer, though another computer shares its own there.
func TestUsageOffAsksNothing(t *testing.T) {
	old := usageEvery
	usageEvery = 0
	t.Cleanup(func() { usageEvery = old })
	fake := &usageDAV{fakeDAV: &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true, Usage: true}
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	Configure(cfg)
	usage.Append(call(time.Now().Add(-time.Minute), "deepseek-chat", 100))
	if err := SyncNow(ctx); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	off := cfg
	off.Usage = false
	Configure(off)
	usage.Append(call(time.Now().Add(-time.Minute), "deepseek-chat", 9))
	finds := fake.finds
	for range 2 {
		if err := SyncNow(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fake.finds != finds || len(fake.usageFiles()) != 1 {
		t.Fatalf("usage off: %d listings, files %v", fake.finds-finds, fake.usageFiles())
	}
	if p := usage.QueryPage(usage.All, usage.Filter{}, 0, 100); p.Total != 1 || p.Computers != nil {
		t.Fatalf("usage off: %d rows, computers %v", p.Total, p.Computers)
	}
}

// The same through an S3 bucket: the days listed (ListObjectsV2, signed
// with its query), read, written and deleted under <prefix>/magpie/usage/.
func TestUsageSharedThroughS3(t *testing.T) {
	old := usageEvery
	usageEvery = 0
	t.Cleanup(func() { usageEvery = old })
	f, srv := newFakeS3(t)
	lists := 0
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" && key == "":
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.signed(r, nil) || bucket != f.bucket {
				f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
				return
			}
			lists++
			prefix := r.URL.Query().Get("prefix")
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
			for k := range f.objects {
				if strings.HasPrefix(k, prefix) {
					fmt.Fprintf(&b, `<Contents><Key>%s</Key><ETag>%s</ETag></Contents>`, k, f.etags[k])
				}
			}
			b.WriteString(`<IsTruncated>false</IsTruncated></ListBucketResult>`)
			w.Write([]byte(b.String()))
		case r.Method == http.MethodDelete:
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.signed(r, nil) {
				f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
				return
			}
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			f.ServeHTTP(w, r)
		}
	})
	ctx := context.Background()
	cfg := f.config(srv)
	cfg.Usage = true
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	usage.Append(call(time.Now().Add(-time.Minute), "deepseek-chat", 100))
	if err := SyncNow(ctx); err != nil || Status().UsageError != "" {
		t.Fatalf("a: %v %q", err, Status().UsageError)
	}
	aID, _ := usage.Computer()
	old2 := "team x+y/magpie/usage/" + aID + "-2020-01-01" + usageExt
	f.mu.Lock()
	f.store(old2, []byte("old"))
	f.mu.Unlock()
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	usage.Append(call(time.Now().Add(-time.Minute), "deepseek-chat", 9))
	for range 2 {
		if err := SyncNow(ctx); err != nil || Status().UsageError != "" {
			t.Fatalf("b: %v %q", err, Status().UsageError)
		}
	}
	if p := usage.QueryPage(usage.All, usage.Filter{}, 0, 100); p.Total != 2 || p.Sum.Input != 109 || len(p.Computers) != 2 {
		t.Fatalf("b: %d rows, %d in, computers %v", p.Total, p.Sum.Input, p.Computers)
	}
	a.use(t)
	if err := SyncNow(ctx); err != nil || Status().UsageError != "" {
		t.Fatalf("a again: %v %q", err, Status().UsageError)
	}
	if p := usage.QueryPage(usage.All, usage.Filter{}, 0, 100); p.Total != 2 {
		t.Fatalf("a: %d rows", p.Total)
	}
	f.mu.Lock()
	_, still := f.objects[old2]
	f.mu.Unlock()
	if still || lists < 4 {
		t.Fatalf("a's old day still there: %v; %d listings", still, lists)
	}
}

// Where the clocks go forward at 00:00, shareWith began the day before that
// day at 23:00 west of UTC (Santiago, Havana, the Azores), and east of it
// (Beirut) began that day at 01:00 and the day before at the 01:00 a day
// back from it. On the day and the day after, the days before it went up
// again holding only the calls from that hour on, and the other computers
// took those in place of the whole days. Here each keeps both its calls, at
// 00:30 and 23:30, and the day that leaves the window on the day leaves the
// server then, not a day later. shareWith reads time.Local, so each zone
// runs in a child process with TZ set.
func TestUsageSharedDaysPastASkippedMidnight(t *testing.T) {
	const zoneEnv = "MAGPIE_TEST_USAGE_ZONE"
	skipped := map[string]time.Time{ // the day the clocks skip 00:00, its noon in UTC
		"America/Santiago": time.Date(2026, 9, 6, 15, 0, 0, 0, time.UTC),
		"America/Havana":   time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC),
		"Atlantic/Azores":  time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
		"Asia/Beirut":      time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC),
	}
	if zone := os.Getenv(zoneEnv); zone != "" {
		day := skipped[zone].In(time.Local)
		if day.Hour() != 12 {
			t.Fatalf("TZ=%s didn't set the local zone: its day's noon is %s", zone, day)
		}
		y, m, d := day.Date()
		at := func(n, hour, min int) time.Time { return time.Date(y, m, d+n, hour, min, 0, 0, time.Local) }
		date := func(n int) string { return at(n, 12, 0).Format(time.DateOnly) }
		newComputer(t).use(t)
		_, cfg := usageShortServer(t, 0)
		dav, err := newDAV(cfg)
		if err != nil {
			t.Fatal(err)
		}
		oldClock := usage.Clock
		t.Cleanup(func() { usage.Clock = oldClock })
		ctx := context.Background()
		id, _ := usage.Computer()
		st := &usageState{}
		// share at noon n days from the day skipped
		share := func(n int) {
			t.Helper()
			now := at(n, 12, 0)
			usage.Clock = func() time.Time { return now }
			if err := shareWith(ctx, cfg, st, dav); err != nil {
				t.Fatalf("%s: sharing at %s: %v", zone, now, err)
			}
		}
		// day n's file on the server, as shared on day on, has both its calls
		whole := func(n, on int) {
			t.Helper()
			got, err := readDay(ctx, dav, id+"-"+date(n)+usageExt, cfg.Passphrase)
			if err != nil {
				t.Fatalf("%s: shared at noon on %s, %s: %v", zone, date(on), date(n), err)
			}
			if len(got.Calls) != 2 {
				t.Errorf("%s: shared at noon on %s, %s holds %d of its 2 calls (00:30 and 23:30)", zone, date(on), date(n), len(got.Calls))
			}
		}
		oldest := id + "-" + date(-usage.SharedDays) + usageExt
		there := func() bool {
			t.Helper()
			l, err := dav.list(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, ok := l[oldest]
			return ok
		}

		usage.Append(call(at(-usage.SharedDays, 23, 30), "deepseek-chat", 1))
		usage.Append(call(at(-2, 0, 30), "deepseek-chat", 1))
		usage.Append(call(at(-2, 23, 30), "deepseek-chat", 1))
		usage.Append(call(at(-1, 0, 30), "deepseek-chat", 1))
		share(-1)
		if !there() {
			t.Fatalf("%s: %s, the window's first day on %s, isn't on the server", zone, date(-usage.SharedDays), date(-1))
		}
		usage.Append(call(at(-1, 23, 30), "deepseek-chat", 1))
		share(0)
		whole(-2, 0)
		whole(-1, 0)
		if there() {
			t.Errorf("%s: %s, past the window on %s, is still on the server", zone, date(-usage.SharedDays), date(0))
		}
		share(1)
		whole(-2, 1)
		whole(-1, 1)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Go on Windows takes the local zone from the system, not TZ")
	}
	for zone := range skipped {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUsageSharedDaysPastASkippedMidnight$")
		cmd.Env = append(os.Environ(), "TZ="+zone, zoneEnv+"="+zone)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", zone, err, out)
		}
	}
}
