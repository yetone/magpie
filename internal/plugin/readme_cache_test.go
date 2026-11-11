package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// npm's human check answers 200 with a page that isn't the package's entry
// (River_WLan on X: "unexpected end of JSON input", no README). The copy the
// community repo keeps comes first, and npm is not asked when it has one.
func TestReadmeFromTheKeptCopyBeforeNPM(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var npm atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/npm/"):
			npm.Add(1) // the human check: 200 and nothing a package page holds
		case r.URL.Path == "/copy/rsshub-plugin":
			_, _ = w.Write([]byte("# kept copy"))
		case r.URL.Path == "/copy/@sc/scoped":
			_, _ = w.Write([]byte("# scoped copy"))
		case r.URL.Path == "/copy/github/o/r":
			_, _ = w.Write([]byte("# repo copy"))
		case r.URL.Path == "/copy/portal":
			_, _ = w.Write([]byte("<!DOCTYPE html><title>sign in</title>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a, b := npmRegistry, readmeBases
	npmRegistry, readmeBases = srv.URL+"/npm", []string{srv.URL + "/copy/%s"}
	defer func() { npmRegistry, readmeBases = a, b }()
	ctx := context.Background()

	for name, want := range map[string]string{"rsshub-plugin": "# kept copy", "@sc/scoped": "# scoped copy", "github:o/r": "# repo copy"} {
		p, err := Readme(ctx, name)
		if err != nil || p.Readme != want {
			t.Errorf("%s: %q, %v; want %q", name, p.Readme, err, want)
		}
	}
	if n := npm.Load(); n != 0 {
		t.Errorf("npm was asked %d times though the kept copy had the README", n)
	}
	// no copy (or a portal's page in its place): npm is asked, and its
	// human check reads as one, not as "unexpected end of JSON input"
	for _, name := range []string{"portal", "nocopy"} {
		_, err := Readme(ctx, name)
		if err == nil || strings.Contains(err.Error(), "unexpected end") || !strings.Contains(err.Error(), "human check") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if npm.Load() != 2 {
		t.Errorf("npm asked %d times, want 2", npm.Load())
	}
}

// A copy that doesn't answer holds the page for readmeCacheWait, not
// longer; npm is asked after it.
func TestKeptCopyThatHangsDoesNotHoldThePage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/copy/") {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = w.Write([]byte(`{"readme":"# from npm"}`))
	}))
	defer srv.Close()
	defer close(release)
	a, b, c := npmRegistry, readmeBases, readmeCacheWait
	npmRegistry, readmeBases, readmeCacheWait = srv.URL+"/npm", []string{srv.URL + "/copy/%s"}, 100*time.Millisecond
	defer func() { npmRegistry, readmeBases, readmeCacheWait = a, b, c }()
	p, err := Readme(context.Background(), "slow-copy")
	if err != nil || p.Readme != "# from npm" {
		t.Fatalf("%q, %v", p.Readme, err)
	}
}

func TestReadmeKey(t *testing.T) {
	for spec, want := range map[string]string{
		"x": "x", "@a/b": "@a/b", "github:o/r": "github/o/r", "github:o/r#v1": "github/o/r",
		"https://github.com/o/r": "", "../x": "", "": "",
	} {
		if got := readmeKey(spec); got != want {
			t.Errorf("readmeKey(%q) = %q, want %q", spec, got, want)
		}
	}
}
