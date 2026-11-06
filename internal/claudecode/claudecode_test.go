package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/stall"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// manifest is the shape of downloads.claude.ai's manifest.json for
// 2.1.287 (fields as Anthropic serves them), with this machine's build's
// checksum and size those of body.
func manifest(platform, body string) string {
	sum := sha256.Sum256([]byte(body))
	return fmt.Sprintf(`{
  "version": "2.1.287",
  "manifestSignatureEnforcement": "flag",
  "commit": "3c446a1b98aceb99a6cdee0f84a8bea42f4a8937",
  "buildDate": "2026-10-01T16:26:16Z",
  "platforms": {
    "darwin-arm64": {"binary": "claude", "checksum": "6eab8333fe2121553100d8f40bfada384a3e989b94f947e18ba6677a6fcb41ea", "size": 227827120},
    "linux-x64-musl": {"binary": "claude", "checksum": "ba22ce4b5744c6016e3076ccbd692a059fc4e9f1592f21361859a73938f4e722", "size": 238063704},
    %q: {"binary": %q, "checksum": %q, "size": %d}
  },
  "sdkCompat": {"testedWrapperVersions": ["0.3.247"]}
}`, platform, exeName(runtime.GOOS), hex.EncodeToString(sum[:]), len(body))
}

// release serves a release as downloads.claude.ai does: latest, the
// manifest, and the build, served as served (body may differ from what the
// manifest says).
func release(t *testing.T, latest, manifestBody, served string) {
	t.Helper()
	platform, err := Platform(runtime.GOOS, runtime.GOARCH, false)
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprint(w, latest)
		case "/2.1.287/manifest.json":
			fmt.Fprint(w, manifestBody)
		case "/2.1.287/" + platform + "/" + exeName(runtime.GOOS):
			fmt.Fprint(w, served)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := Base
	Base = srv.URL
	t.Cleanup(func() { Base = old })
	t.Cleanup(func() { _ = Remove() })
}

func thisPlatform(t *testing.T) string {
	p, err := Platform(runtime.GOOS, runtime.GOARCH, false)
	if err != nil {
		t.Skip(err)
	}
	return p
}

func TestInstallKeepsTheCheckedBuild(t *testing.T) {
	const body = "#!/bin/sh\necho 2.1.287 '(Claude Code)'\n"
	release(t, "2.1.287\n", manifest(thisPlatform(t), body), body)
	// a version downloaded before goes once the new one is in
	old := filepath.Join(Root(), "2.1.200")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, exeName(runtime.GOOS)), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, v := Downloaded(); v != "2.1.200" || p == "" {
		t.Fatalf("Downloaded() = %q, %q; want 2.1.200", p, v)
	}

	r, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "2.1.287" || r.Size != int64(len(body)) {
		t.Fatalf("Latest() = %+v", r)
	}
	var told int64
	path, err := Install(context.Background(), r, func(n int64) { told = n })
	if err != nil {
		t.Fatal(err)
	}
	if told != int64(len(body)) {
		t.Errorf("progress told %d bytes, want %d", told, len(body))
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != body {
		t.Fatalf("kept %q (%v), want the build", b, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode()&0o100 == 0 {
			t.Errorf("%s isn't executable: %v", path, st.Mode())
		}
	}
	if p, v := Downloaded(); p != path || v != "2.1.287" {
		t.Errorf("Downloaded() = %q, %q; want %q, 2.1.287", p, v, path)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the older download is still there: %v", err)
	}
}

func TestInstallRefusesABuildTheManifestDoesntName(t *testing.T) {
	const body = "#!/bin/sh\necho 2.1.287\n"
	for name, served := range map[string]string{
		"other bytes":   strings.Replace(body, "2.1.287", "2.1.288", 1),
		"cut short":     body[:10],
		"longer":        body + "x",
		"an error page": "<html>Access denied</html>",
	} {
		t.Run(name, func(t *testing.T) {
			release(t, "2.1.287", manifest(thisPlatform(t), body), served)
			r, err := Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Install(context.Background(), r, nil); err == nil {
				t.Fatal("a build that isn't the manifest's was kept")
			}
			if p, v := Downloaded(); p != "" {
				t.Errorf("Downloaded() = %q, %q after a failed download", p, v)
			}
			left, _ := filepath.Glob(filepath.Join(Root(), "*", "*"))
			if len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

func TestLatestSaysWhenNoVersionCame(t *testing.T) {
	// what a region downloads.claude.ai doesn't serve gets in its place
	release(t, "<!doctype html><title>Unavailable</title>", "", "")
	_, err := Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "didn't name a version") {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestLatestWithoutThisPlatformsBuild(t *testing.T) {
	release(t, "2.1.287", `{"version":"2.1.287","platforms":{"plan9-x64":{"binary":"claude","checksum":"`+strings.Repeat("a", 64)+`","size":1}}}`, "")
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "has no") {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestPlatformNames(t *testing.T) {
	for _, c := range []struct {
		goos, arch string
		musl       bool
		want       string
	}{
		{"darwin", "arm64", false, "darwin-arm64"},
		{"darwin", "amd64", false, "darwin-x64"},
		{"linux", "amd64", false, "linux-x64"},
		{"linux", "arm64", false, "linux-arm64"},
		{"linux", "amd64", true, "linux-x64-musl"},
		{"linux", "arm64", true, "linux-arm64-musl"},
		{"windows", "amd64", false, "win32-x64"},
		{"windows", "arm64", false, "win32-arm64"},
	} {
		if got, err := Platform(c.goos, c.arch, c.musl); err != nil || got != c.want {
			t.Errorf("Platform(%s, %s, %v) = %q, %v; want %q", c.goos, c.arch, c.musl, got, err, c.want)
		}
	}
	if _, err := Platform("linux", "386", false); err == nil {
		t.Error("linux/386 named a build")
	}
}

func TestFindTakesTheMachinesOwnFirst(t *testing.T) {
	dir := filepath.Join(Root(), "2.1.287")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove() })
	got := filepath.Join(dir, exeName(runtime.GOOS))
	if err := os.WriteFile(got, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if p := Find(); p != got {
		t.Errorf("with none of its own, Find() = %q, want the download %q", p, got)
	}
	if runtime.GOOS == "windows" {
		return // a #! script isn't a program there
	}
	own := t.TempDir()
	testenv.Program(t, filepath.Join(own, "claude"), "#!/bin/sh\n")
	t.Setenv("PATH", own)
	if p := Find(); p == got || p == "" {
		t.Errorf("with its own on PATH, Find() = %q, want that one", p)
	}
}

// A build download has no bound on the whole (#5572); one whose server
// goes silent part way is ended by the stall, said as one.
func TestDownloadThatGoesSilentIsAStall(t *testing.T) {
	old := downloadStall
	downloadStall = 100 * time.Millisecond
	t.Cleanup(func() { downloadStall = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("#!/bin/sh\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := download(ctx, Release{URL: srv.URL, Size: 1000, Checksum: "00"}, filepath.Join(t.TempDir(), "claude"), nil)
	if !errors.Is(err, stall.ErrStalled) || ctx.Err() != nil {
		t.Fatalf("err = %v, want a stall", err)
	}
}
