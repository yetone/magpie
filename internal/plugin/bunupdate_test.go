package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// bunReleases serves Bun's releases as GitHub does: each version's zip for
// this machine, holding the bun given, and its SHASUMS256.txt. It counts
// the zips asked for.
func bunReleases(t *testing.T, exe []byte) *atomic.Int32 {
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
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHASUMS256.txt"):
			fmt.Fprintf(w, "%s  %s.zip\n", hex.EncodeToString(sum[:]), target)
		case strings.HasSuffix(r.URL.Path, "/"+target+".zip"):
			asked.Add(1)
			w.Write(zb.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := bunRelease
	bunRelease = srv.URL
	t.Cleanup(func() { bunRelease = old })
	return &asked
}

// bunHome gives the test its own magpie folders, no $MAGPIE_BUN, and Bun's
// newest release as given.
func bunHome(t *testing.T, latest string, published time.Time) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local"))
	t.Setenv("MAGPIE_BUN", "")
	old := bunLatest
	bunLatest = func(context.Context) (string, time.Time, error) { return latest, published, nil }
	t.Cleanup(func() { bunLatest = old })
}

func stubTry(t *testing.T, f func(exe, v string) error) {
	old := tryBun
	tryBun = func(_ context.Context, exe, v string) error { return f(exe, v) }
	t.Cleanup(func() { tryBun = old })
}

// bunSums must be checked against Bun's official SHASUMS256.txt when
// BunVersion changes; this catches a missing or malformed value.
func TestBunDefaultSums(t *testing.T) {
	for _, target := range []string{
		"bun-darwin-aarch64",
		"bun-darwin-x64",
		"bun-linux-aarch64",
		"bun-linux-x64-baseline",
		"bun-windows-x64-baseline",
		"bun-linux-aarch64-android",
		"bun-linux-x64-android-baseline",
	} {
		got := bunChecksum(BunVersion, target)
		if len(got) != 64 {
			t.Fatalf("%s checksum = %q", target, got)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Fatalf("%s checksum = %q: %v", target, got, err)
		}
	}
	if got := bunChecksum("9.9.9", "bun-linux-x64-baseline"); got != "" {
		t.Fatalf("a newer Bun used the built-in checksum: %q", got)
	}
}

func TestBunTarget(t *testing.T) {
	want := map[string]string{
		"darwin/arm64":  "bun-darwin-aarch64",
		"darwin/amd64":  "bun-darwin-x64",
		"linux/arm64":   "bun-linux-aarch64",
		"linux/amd64":   "bun-linux-x64-baseline",
		"windows/amd64": "bun-windows-x64-baseline",
		"android/arm64": "bun-linux-aarch64-android",
		"android/amd64": "bun-linux-x64-android-baseline",
	}[runtime.GOOS+"/"+runtime.GOARCH]
	got, err := bunTarget()
	if want == "" {
		if err == nil {
			t.Fatalf("unsupported platform selected %q", got)
		}
		return
	}
	if err != nil || got != want {
		t.Fatalf("bunTarget = %q, %v; want %q", got, err, want)
	}
}

// A Bun out two days is taken: downloaded, its checksum checked, tried,
// and run from then on; the one before it is kept to fall back on.
func TestCheckBunTakesASettledRelease(t *testing.T) {
	bunHome(t, "9.9.9", time.Now().Add(-49*time.Hour))
	asked := bunReleases(t, []byte("new bun"))
	var tried []string
	stubTry(t, func(exe, v string) error { tried = append(tried, v); return nil })
	ctx := context.Background()
	old, err := Bun(ctx) // the first download, BunVersion
	if err != nil || BunInUse() != BunVersion {
		t.Fatalf("first Bun %s, %v, in use %s", old, err, BunInUse())
	}
	got, err := CheckBun(ctx)
	if err != nil || got != "9.9.9" {
		t.Fatalf("CheckBun = %q, %v", got, err)
	}
	if BunInUse() != "9.9.9" || len(tried) != 1 || asked.Load() != 2 {
		t.Fatalf("in use %s, tried %v, %d downloads", BunInUse(), tried, asked.Load())
	}
	exe, err := Bun(ctx)
	if err != nil || filepath.Base(filepath.Dir(exe)) != "9.9.9" {
		t.Fatalf("Bun = %s, %v", exe, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new bun" {
		t.Fatalf("the new bun is %q", b)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the Bun before wasn't kept: %v", err)
	}
	if s := readBunState(); s.Previous != BunVersion || s.Checked.IsZero() {
		t.Fatalf("state %+v", s)
	}
	// nothing newer: nothing downloaded
	if got, err := CheckBun(ctx); got != "" || err != nil || asked.Load() != 2 {
		t.Fatalf("again: %q, %v, %d downloads", got, err, asked.Load())
	}
}

// A release out less than two days, or older than the floor, isn't taken.
func TestCheckBunWaitsAndKeepsTheFloor(t *testing.T) {
	for _, c := range []struct {
		v   string
		ago time.Duration
	}{{"9.9.9", 47 * time.Hour}, {"0.0.1", 300 * time.Hour}} {
		bunHome(t, c.v, time.Now().Add(-c.ago))
		asked := bunReleases(t, []byte("bun"))
		stubTry(t, func(string, string) error { return nil })
		if got, err := CheckBun(context.Background()); got != "" || err != nil || asked.Load() != 0 || BunInUse() != BunVersion {
			t.Fatalf("%s out %s: took %q, %v, %d downloads", c.v, c.ago, got, err, asked.Load())
		}
	}
}

// A Bun that fails its try is set aside: removed, never downloaded again,
// the one in use kept.
func TestCheckBunSetsAsideOneThatFailsItsTry(t *testing.T) {
	bunHome(t, "9.9.9", time.Now().Add(-72*time.Hour))
	asked := bunReleases(t, []byte("broken bun"))
	stubTry(t, func(string, string) error { return errors.New("it says it is \"\"") })
	if _, err := CheckBun(context.Background()); err == nil {
		t.Fatal("a Bun that failed its try was taken")
	}
	if BunInUse() != BunVersion || asked.Load() != 1 {
		t.Fatalf("in use %s, %d downloads", BunInUse(), asked.Load())
	}
	if _, err := os.Stat(bunDirOf("9.9.9")); !os.IsNotExist(err) {
		t.Fatalf("the failed Bun was left: %v", err)
	}
	if got, err := CheckBun(context.Background()); got != "" || err != nil || asked.Load() != 1 {
		t.Fatalf("tried again: %q, %v, %d downloads", got, err, asked.Load())
	}
}

// The real try: the Bun on PATH passes it, as its own version.
func TestTryBunOnRealBun(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	v := bunReported(bun)
	if err := tryBun(context.Background(), bun, v); err != nil {
		t.Fatalf("bun %s: %v", v, err)
	}
	if err := tryBun(context.Background(), bun, "0.0.1"); err == nil {
		t.Fatal("a Bun saying another version passed")
	}
}

// A Bun the host dies on is left for the one before it, and set aside:
// the plugins load on the old one.
func TestHostFallsBackWhenTheNewBunDies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dying bun is a shell script")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	bunHome(t, "9.9.9", time.Now().Add(-72*time.Hour))
	t.Cleanup(Settle)
	if err := os.MkdirAll(bunDirOf(BunVersion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bun, bunExeOf(BunVersion)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bunDirOf("9.9.9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bunExeOf("9.9.9"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBunState(bunState{Current: "9.9.9", Previous: BunVersion}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	if s := readBunState(); BunInUse() != BunVersion || len(s.Bad) != 1 || s.Bad[0] != "9.9.9" {
		t.Fatalf("in use %s, state %+v", BunInUse(), s)
	}
}

func TestDownloadBunDefaultSkipsOfficialSums(t *testing.T) {
	target, err := bunTarget()
	if err != nil {
		t.Skip(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	h := &zip.FileHeader{Name: target + "/" + bunExe(), Method: zip.Deflate}
	h.SetMode(0o755)
	w, _ := zw.CreateHeader(h)
	w.Write([]byte("bun"))
	zw.Close()
	sum := sha256.Sum256(zb.Bytes())

	var sumsAsked atomic.Int32
	var zipAsked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHASUMS256.txt"):
			sumsAsked.Add(1)
			http.Error(w, "must not ask", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/"+target+".zip"):
			zipAsked.Add(1)
			w.Write(zb.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldRelease := bunRelease
	bunRelease = srv.URL
	defer func() { bunRelease = oldRelease }()

	oldSum, hadSum := bunSums[target+".zip"]
	bunSums[target+".zip"] = hex.EncodeToString(sum[:])
	defer func() {
		if hadSum {
			bunSums[target+".zip"] = oldSum
		} else {
			delete(bunSums, target+".zip")
		}
	}()

	exe := filepath.Join(t.TempDir(), bunExe())
	if err := downloadBun(context.Background(), BunVersion, exe); err != nil {
		t.Fatal(err)
	}
	if sumsAsked.Load() != 0 {
		t.Fatalf("SHASUMS256.txt asked %d times", sumsAsked.Load())
	}
	if zipAsked.Load() != 1 {
		t.Fatalf("zip asked %d times", zipAsked.Load())
	}
}
