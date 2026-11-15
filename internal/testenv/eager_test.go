package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A package-level pattern is listed; one inside a function, a func
// literal's, or a lazyre.New is not.
func TestEagerRegexps(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.go")
	src := `package x

import "regexp"

var a = regexp.MustCompile("a")

var (
	b, c = regexp.Compile("b")
	d    = func() *regexp.Regexp { return regexp.MustCompile("d") }
	e    = lazyre.New("e")
)

func g() { _ = regexp.MustCompile("g") }
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := EagerRegexps(f)
	if len(got) != 2 || !strings.HasSuffix(got[0], ":5:9") || !strings.HasSuffix(got[1], ":8:9") {
		t.Fatalf("got %q, want lines 5 and 8", got)
	}
}
