package settings

import (
	"strings"
	"testing"
)

// An origin is saved as the browser sends it, whichever way the user wrote
// it, and a wildcard or anything that is not an origin is refused (#1051).
func TestCleanOrigins(t *testing.T) {
	got, err := CleanOrigins([]string{" http://LocalHost:3000/ ", "https://a.example:443", "http://[::1]:8080", "http://a.example:80", "", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:3000", "https://a.example", "http://[::1]:8080", "http://a.example"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("%v, want %v", got, want)
	}
	for _, bad := range []string{"*", "https://*.example.com", "localhost:3000", "file:///x", "http://a.example/path", "http://u:p@a.example", "http://a.example?x=1", "null"} {
		if _, err := CleanOrigins([]string{bad}); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
}
