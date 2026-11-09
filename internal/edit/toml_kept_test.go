package edit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

// What tomlFileOf keeps of a Codex config holds no piece of the file's
// text and no slack: a key's text sliced from it kept the whole file
// alive (1.5 MB of a real one with ~11k projects) beside the parse.
func TestTOMLFileKeepsNoFileText(t *testing.T) {
	var b strings.Builder
	b.WriteString("model = \"m\"\n\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "[projects.\"/Users/someone/project-%d\"]\ntrust_level = \"trusted\" # ok\n\n", i)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := tomlFileOf(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.tables) != 3000 || cap(doc.tables) != len(doc.tables) || cap(doc.names) != len(doc.names) {
		t.Fatalf("tables %d/%d, names %d/%d: want 3000 kept with no room past them", len(doc.tables), cap(doc.tables), len(doc.names), cap(doc.names))
	}
	// in the file, one project's value is a block's length after the one
	// before it; read out of it, each is a string of its own
	at := func(s string) uintptr { return uintptr(unsafe.Pointer(unsafe.StringData(s))) }
	sliced := 0
	for i := 1; i < len(doc.tables); i++ {
		a, z := doc.tables[i-1].keys, doc.tables[i].keys
		if len(a) != 1 || len(z) != 1 || cap(z) != 1 || z[0].text != `"trusted"` {
			t.Fatalf("table %d keys %+v", i, z)
		}
		block := len(fmt.Sprintf("[projects.\"/Users/someone/project-%d\"]\ntrust_level = \"trusted\" # ok\n\n", i-1))
		if at(z[0].text)-at(a[0].text) == uintptr(block) {
			sliced++
		}
	}
	if sliced > 0 {
		t.Fatalf("%d keys' text is still a slice of the file's", sliced)
	}
}
