package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// heldBunReleases serves a Bun release whose zip (holding exe) is held
// back until gate is closed, as a download that stalls on a slow or
// blocked link is (#6735); fail answers it with a 500 instead.
func heldBunReleases(t *testing.T, exe []byte, gate <-chan struct{}, fail bool) *atomic.Int32 {
	t.Helper()
	target, err := bunTarget()
	if err != nil {
		t.Skip(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	h := &zip.FileHeader{Name: target + "/" + bunExe(), Method: zip.Deflate}
	h.SetMode(0o755)
	w, _ := zw.CreateHeader(h)
	w.Write(exe)
	zw.Close()
	sum := sha256.Sum256(zb.Bytes())
	oldSum, hadSum := bunSums[target+".zip"]
	bunSums[target+".zip"] = hex.EncodeToString(sum[:])
	t.Cleanup(func() {
		if hadSum {
			bunSums[target+".zip"] = oldSum
		} else {
			delete(bunSums, target+".zip")
		}
	})
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+target+".zip") {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		if fail {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		select {
		case <-gate:
			w.Write(zb.Bytes())
		case <-r.Context().Done():
		}
	}))
	// the download is let go before the server waits for its requests
	t.Cleanup(srv.Close)
	t.Cleanup(Settle)
	old := bunRelease
	bunRelease = srv.URL
	t.Cleanup(func() { bunRelease = old })
	return &asked
}

func readyWait(t *testing.T, d time.Duration) {
	old := bunReadyWait
	bunReadyWait = d
	t.Cleanup(func() { bunReadyWait = old })
}

func waitWithin(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	end := time.Now().Add(within)
	for !ok() {
		if time.Now().After(end) {
			t.Fatalf("no %s within %s", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// While the Bun to run plugins is still downloading, a call for the host
// ends with why instead of waiting on the download (#6735): within the
// bounded wait, or at once when the caller's own context ends first,
// another caller holding the host's lock or not. The download goes on in
// the background, once, and the host starts on it when it is done.
func TestHostWhileBunDownloadStalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the downloaded bun is a shell script")
	}
	real := testenv.Bun(t)
	bunHome(t, BunVersion, time.Now())
	gate := make(chan struct{})
	opened := false
	open := func() {
		if !opened {
			opened = true
			close(gate)
		}
	}
	t.Cleanup(open)
	asked := heldBunReleases(t, []byte("#!/bin/sh\nexec "+real+" \"$@\"\n"), gate, false)
	readyWait(t, 2*time.Second)

	ctx := context.Background()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	began := time.Now()
	go func() { _, err := Plugins(ctx); first <- err }()
	waitWithin(t, "download asked for", 10*time.Second, func() bool { return asked.Load() >= 1 })

	// a caller whose context ends while another waits on the host
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := Plugins(short); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("Plugins answered with no Bun")
		}
	case <-time.After(10 * time.Second):
		open()
		t.Fatal("a caller whose context ended still waits on the stalled Bun download")
	}

	select {
	case err := <-first:
		var nr *BunNotReadyError
		if !errors.As(err, &nr) || !strings.Contains(err.Error(), "Bun runtime not ready") || !strings.Contains(err.Error(), "downloading Bun "+BunVersion) {
			t.Fatalf("Plugins = %v, want Bun runtime not ready", err)
		}
		if d := time.Since(began); d > 10*time.Second {
			t.Fatalf("Plugins took %s", d)
		}
	case <-time.After(15 * time.Second):
		open()
		t.Fatal("Plugins still waits on the stalled Bun download after 15s")
	}

	// the download went on, once, and the host starts on it
	open()
	waitWithin(t, "Bun downloaded", 30*time.Second, HasBun)
	lctx, lcancel := context.WithTimeout(ctx, time.Minute)
	defer lcancel()
	ls, err := Plugins(lctx)
	if err != nil || len(ls) != 1 {
		t.Fatalf("Plugins = %+v, %v once Bun is here", ls, err)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("Bun downloaded %d times", n)
	}
}

// A download that failed is said at once to the next call for the host,
// not tried again by every request; an install (Bun) tries it again.
func TestHostAfterBunDownloadFailedSaysWhy(t *testing.T) {
	bunHome(t, BunVersion, time.Now())
	asked := heldBunReleases(t, []byte("bun"), nil, true)
	readyWait(t, 10*time.Second)
	ctx := context.Background()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		began := time.Now()
		_, err := Plugins(ctx)
		var nr *BunNotReadyError
		if !errors.As(err, &nr) || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "500") {
			t.Fatalf("Plugins #%d = %v, want the download's failure", i+1, err)
		}
		if d := time.Since(began); d > 5*time.Second {
			t.Fatalf("Plugins #%d took %s", i+1, d)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("the failed download was tried %d times by the host", n)
	}
	if _, err := Bun(ctx); err == nil || asked.Load() != 2 {
		t.Fatalf("Bun = %v after %d downloads, want a second try", err, asked.Load())
	}
}

// When the download fails, a Bun on PATH is taken when it is one magpie
// would run (at least BunVersion, not set aside, passing its try), copied
// in as magpie-bun; an older one is not.
func TestBunOnPathAfterDownloadFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the bun on PATH is a shell script")
	}
	for _, c := range []struct {
		says string
		take bool
	}{{BunVersion, true}, {"1.0.0", false}} {
		t.Run(c.says, func(t *testing.T) {
			bunHome(t, BunVersion, time.Now())
			heldBunReleases(t, []byte("bun"), nil, true)
			onPath := filepath.Join(t.TempDir(), "bun")
			testenv.Program(t, onPath, "#!/bin/sh\necho "+c.says+"\n")
			pathBunsAre(t, onPath)
			stubTry(t, func(exe, v string) error { return nil })
			exe, err := Bun(context.Background())
			if !c.take {
				if err == nil {
					t.Fatalf("Bun %s on PATH taken: %s", c.says, exe)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(exe)
			if exe != bunExeOf(c.says) || !strings.Contains(string(b), "echo "+c.says) {
				t.Fatalf("Bun = %s holding %q", exe, b)
			}
		})
	}
}

func pathBunsAre(t *testing.T, buns ...string) {
	old := pathBuns
	pathBuns = func() []string { return buns }
	t.Cleanup(func() { pathBuns = old })
}
