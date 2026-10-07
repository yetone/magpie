package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

type devinEntry struct {
	name, body, link string
	mode             int64
	typ              byte
}

// devinBundle is a .tar.gz laid out as the Devin CLI's: bin/devin and share/.
func devinBundle(t *testing.T, extra ...devinEntry) []byte {
	t.Helper()
	entries := append([]devinEntry{
		{name: "bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "bin/devin", body: "#!/bin/sh\necho 'devin 9.9.9 (fake)'\n", typ: tar.TypeReg, mode: 0o755},
		{name: "share/devin/docs/index.mdx", body: "docs", typ: tar.TypeReg, mode: 0o644},
	}, extra...)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body)), Linkname: e.link}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fakeDevinCDN serves a manifest naming bundle for this platform, with sum as
// its checksum (the bundle's own when sum is empty).
func fakeDevinCDN(t *testing.T, bundle []byte, sum string) *httptest.Server {
	t.Helper()
	target, err := devinTarget(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	if sum == "" {
		s := sha256.Sum256(bundle)
		sum = hex.EncodeToString(s[:])
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cli/current/manifest.json":
			fmt.Fprintf(w, `{"version":"9.9.9","platforms":{%q:{"url":%q,"sha256":%q}}}`, target, srv.URL+"/cli/9.9.9/devin.tar.gz", sum)
		case "/cli/9.9.9/devin.tar.gz":
			w.Write(bundle)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	old := devinCLIBase
	devinCLIBase = srv.URL + "/cli"
	t.Cleanup(func() { devinCLIBase = old })
	return srv
}

// devinHome is a home with no devin anywhere, whose find looks only there.
func devinHome(t *testing.T) (home, link string) {
	t.Helper()
	if os.PathSeparator != '/' {
		t.Skip("the fake devin is a shell script")
	}
	home = claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("PATH", t.TempDir())
	link = filepath.Join(home, ".local", "bin", "devin")
	oldExe := DevinExecutable
	DevinExecutable = func() string {
		if isFile(link) {
			return link
		}
		return ""
	}
	t.Cleanup(func() { DevinExecutable = oldExe })
	return home, link
}

// magpie in Docker: its image has no curl or wget, so the Devin CLI's
// installer couldn't run and Devin's sign-in never began. The CLI is then
// installed as its installer installs it, from the same place, without a
// shell.
func TestDevinInstallsWithoutShell(t *testing.T) {
	home, link := devinHome(t)
	fakeDevinCDN(t, devinBundle(t), "")
	oldShell := shellInstallerRuns
	shellInstallerRuns = func() bool { return false }
	t.Cleanup(func() { shellInstallerRuns = oldShell })

	c, ok := missingCLI("devin")
	if !ok {
		t.Fatal("devin is installed already")
	}
	if err := installCLI(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if DevinExecutable() != link {
		t.Fatalf("devin at %q, want %q", DevinExecutable(), link)
	}
	versions := filepath.Join(home, "data", "devin", "cli", "_versions")
	if l, err := os.Readlink(filepath.Join(versions, "current")); err != nil || l != "9.9.9" {
		t.Fatalf("current links to %q, %v", l, err)
	}
	if l, err := os.Readlink(link); err != nil || l != filepath.Join(versions, "current", "bin", "devin") {
		t.Fatalf("devin links to %q, %v", l, err)
	}
	if out, err := exec.Command(link, "--version").Output(); err != nil || !strings.Contains(string(out), "devin 9.9.9") {
		t.Fatalf("devin --version: %q, %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(versions, "9.9.9", "distribution")); string(b) != "curl-bash\n" {
		t.Fatalf("distribution %q", b)
	}
	if _, err := os.Stat(filepath.Join(versions, "9.9.9", "share", "devin", "docs", "index.mdx")); err != nil {
		t.Fatal(err)
	}
	for _, left := range []string{"_download/9.9.9.tar.gz", "_9.9.9.tmp"} {
		if _, err := os.Lstat(filepath.Join(versions, left)); !os.IsNotExist(err) {
			t.Fatalf("%s was left: %v", left, err)
		}
	}
	// where the shell installer runs, it is still the one used
	if c, _ := cliFor("devin"); c.native == nil || !strings.Contains(c.sh, "cli.devin.ai/install.sh") {
		t.Fatalf("devin's installers %+v", c)
	}
}

// a bundle that isn't the one the manifest names installs nothing, and a
// CLI that was there stays
func TestDevinInstallRejectsWhatDoesNotCheck(t *testing.T) {
	cases := []struct {
		name, sum, want string
		extra           []devinEntry
	}{
		{"checksum", strings.Repeat("0", 64), "checksum mismatch", nil},
		{"escaping file", "", "leaves the folder", []devinEntry{{name: "../evil", body: "x", typ: tar.TypeReg, mode: 0o644}}},
		{"escaping link", "", "leaves the folder", []devinEntry{{name: "bin/out", link: "../../../x", typ: tar.TypeSymlink}}},
		{"device", "", "kind of file", []devinEntry{{name: "dev", typ: tar.TypeChar}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, link := devinHome(t)
			fakeDevinCDN(t, devinBundle(t, tc.extra...), tc.sum)
			err := installDevinCLI(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Fatalf("a devin was left: %v", err)
			}
			versions := filepath.Join(home, "data", "devin", "cli", "_versions")
			for _, left := range []string{"9.9.9", "current", "_9.9.9.tmp", "_download/9.9.9.tar.gz"} {
				if _, err := os.Lstat(filepath.Join(versions, left)); !os.IsNotExist(err) {
					t.Fatalf("%s was left: %v", left, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(versions, "evil")); !os.IsNotExist(err) {
				t.Fatalf("a file was written outside: %v", err)
			}
		})
	}
}

// a devin the user put there themselves isn't replaced
func TestDevinInstallKeepsOwnFile(t *testing.T) {
	_, link := devinHome(t)
	fakeDevinCDN(t, devinBundle(t), "")
	testenv.Program(t, link, "#!/bin/sh\necho mine\n")
	err := installDevinCLI(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(link); !strings.Contains(string(b), "mine") {
		t.Fatalf("the file became %q", b)
	}
}

func TestDevinTarget(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "x86_64-unknown-linux"},
		{"linux", "arm64", "aarch64-unknown-linux"},
		{"darwin", "arm64", "aarch64-apple-darwin"},
		{"darwin", "amd64", "x86_64-apple-darwin"},
	} {
		if got, err := devinTarget(c.goos, c.goarch); err != nil || got != c.want {
			t.Errorf("%s/%s: %q, %v", c.goos, c.goarch, got, err)
		}
	}
	for _, c := range [][2]string{{"windows", "amd64"}, {"linux", "386"}} {
		if got, err := devinTarget(c[0], c[1]); err == nil {
			t.Errorf("%s/%s: %q, want an error", c[0], c[1], got)
		}
	}
}
