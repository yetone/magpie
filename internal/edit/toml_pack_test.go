package edit

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// The kept table names of a Codex config.toml are one buffer, not an
// allocation a name, and hold nothing of the text they were read from.
func TestKeptNamesArePacked(t *testing.T) {
	text := new(strings.Builder)
	var names []string
	for i := range 11000 {
		fmt.Fprintf(text, "projects.\"/Users/someone/work/project-%05d\"\x00", i)
	}
	names = strings.Split(strings.TrimSuffix(text.String(), "\x00"), "\x00")
	names = append(names, "", "model_providers.magpie")
	src := text.String()
	lo, hi := uintptr(unsafe.Pointer(unsafe.StringData(src))), uintptr(unsafe.Pointer(unsafe.StringData(src)))+uintptr(len(src))

	a := &tomlAnswers{}
	a.setNames(names)
	got, ok := a.getNames()
	if !ok || len(got) != len(names) {
		t.Fatalf("got %d names (ok %v), want %d", len(got), ok, len(names))
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("name %d = %q, want %q", i, got[i], names[i])
		}
		if p := uintptr(unsafe.Pointer(unsafe.StringData(got[i]))); got[i] != "" && p >= lo && p < hi {
			t.Fatalf("name %d holds the file's text", i)
		}
	}
	if n := testing.AllocsPerRun(5, func() { packStrings(names) }); n > 2 {
		t.Fatalf("packing %d names took %v allocations, want 2", len(names), n)
	}
	var nilNames tomlAnswers
	nilNames.setNames(nil)
	if got, ok := nilNames.getNames(); !ok || got != nil {
		t.Fatalf("nil names kept as %v (ok %v)", got, ok)
	}
}
