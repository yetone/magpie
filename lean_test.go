package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// gRPC's request tracing (golang.org/x/net/trace) renders its /debug pages
// with html/template, and a reachable template Execute stops the linker
// dropping exported methods nothing calls (TestNothingKeepsEveryMethod).
// The Makefile's LEAN tags leave it out; this holds every build of the
// binary there to them.
func TestEveryBuildIsLean(t *testing.T) {
	b, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	mk := string(b)
	if !regexp.MustCompile(`(?m)^LEAN\s*=.*\bgrpcnotrace\b`).MatchString(mk) {
		t.Fatal("the Makefile's LEAN has no grpcnotrace")
	}
	// A line that builds or installs package main (" ." at its end, or
	// before "||" or "&&"); the launcher and go run lines build others.
	main := regexp.MustCompile(`go (build|install) .* \.( |$|;)`)
	n := 0
	for _, l := range strings.Split(mk, "\n") {
		if !main.MatchString(l) || strings.Contains(l, "-tags dev ") {
			continue
		}
		n++
		if !strings.Contains(l, "$(LEAN)") {
			t.Errorf("a build of magpie without $(LEAN):\n%s", strings.TrimSpace(l))
		}
	}
	if n < 7 {
		t.Errorf("found %d builds of magpie in the Makefile, want at least 7 (build, cli, build-release, install, release-cli, release-windows, release-linux)", n)
	}
	d, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`go build -tags \S*grpcnotrace`).Match(d) {
		t.Error("the Dockerfile's build leaves out grpcnotrace")
	}
}

// The tag only means something while gRPC still reads it: with LEAN's tags
// no release build links x/net/trace.
func TestLeanTagsKeepTracingOut(t *testing.T) {
	for _, c := range []struct{ goos, tags string }{
		// packedassets and comlauncher embed files a release makes first,
		// and change nothing gRPC links.
		{"linux", "nogui,grpcnotrace"},
		{"windows", "production,grpcnotrace"},
		{"darwin", "production,grpcnotrace"},
	} {
		cmd := exec.Command("go", "list", "-deps", "-tags", c.tags, ".")
		// go list runs no compiler, so the darwin app's cgo files are
		// listed on any system; GOPROXY off keeps it off the network.
		cgo := "0"
		if c.goos == "darwin" {
			cgo = "1"
		}
		cmd.Env = append(os.Environ(), "GOOS="+c.goos, "GOARCH=amd64", "CGO_ENABLED="+cgo, "GOPROXY=off", "GOFLAGS=-mod=readonly")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			skipOffline(t, stderr.String())
			t.Fatalf("go list %s %s: %v\n%s", c.goos, c.tags, err, stderr.String())
		}
		deps := "\n" + string(out)
		if strings.Contains(deps, "\ngolang.org/x/net/trace\n") {
			t.Errorf("%s with -tags %s links golang.org/x/net/trace", c.goos, c.tags)
		}
		if !strings.Contains(deps, "\ngoogle.golang.org/grpc\n") {
			t.Logf("%s: gRPC is no longer linked; LEAN's grpcnotrace can go", c.goos)
		}
	}
}

// The linker drops a method nothing calls unless a reachable function
// calls reflect's Method, or MethodByName with a name it can't know: then
// it keeps every exported method of every reachable type. One such call
// cost magpie about 7MB (shittosurunano on X): html/template's, through
// gRPC's tracing (LEAN above), and Go 1.26's reflect.Value.Methods,
// kept by protobuf's MethodByName("Methods") before v1.36.12. This links
// the Windows CLI as a release does and fails on any reachable one. The
// GUI builds aren't held to it: Wails binds every service's methods
// through reflect, so App.Run always reaches one.
func TestNothingKeepsEveryMethod(t *testing.T) {
	if testing.Short() {
		t.Skip("links the whole binary")
	}
	cmd := exec.Command("go", "build", "-tags", "nogui,grpcnotrace", "-ldflags=-dumpdep",
		"-o", filepath.Join(t.TempDir(), "magpie.exe"), ".")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOPROXY=off", "GOFLAGS=-mod=readonly")
	dump, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// -dumpdep writes "from -> to" for every edge the linker marks, a
	// symbol that calls reflect so flagged <ReflectMethod>.
	seen := map[string]bool{}
	var other []string
	sc := bufio.NewScanner(dump)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		from, _, ok := strings.Cut(l, " -> ")
		if !ok {
			if len(other) < 20 {
				other = append(other, l)
			}
			continue
		}
		if name, ok := strings.CutSuffix(from, " <ReflectMethod>"); ok {
			seen[name] = true
		}
	}
	if err := cmd.Wait(); err != nil {
		skipOffline(t, strings.Join(other, "\n"))
		t.Fatalf("go build: %v\n%s", err, strings.Join(other, "\n"))
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Errorf("reachable calls of reflect's Method or MethodByName keep every exported method (find who reaches each with go build -ldflags=-dumpdep):\n%s", strings.Join(names, "\n"))
	}
}

// These build for other systems with GOPROXY off, so a module only another
// system needs may be missing from this machine's cache. CI runs go mod
// download first (test.yml), which fetches every module go.mod lists.
func skipOffline(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "GOPROXY=off") || strings.Contains(out, "module lookup disabled") {
		t.Skipf("a module isn't in the cache (go mod download fetches it):\n%s", out)
	}
}
