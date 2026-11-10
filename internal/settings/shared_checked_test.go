package settings

import (
	"fmt"
	"strings"
	"testing"
)

// The gateway's allow list asks SharedChecked on every request: an
// unchanged settings.json gives its settings without a copy, a change is
// read at once, and a file that doesn't parse is unknown (an error), as
// LoadChecked says, never an empty list.
func TestSharedCheckedReadsOnceAndKnowsUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var images []string
	for i := range 60 {
		images = append(images, fmt.Sprintf(`"p/m%d":true`, i))
	}
	writeRaw(t, `{"allowIPs":["10.0.0.0/8","192.168.1.5"],"redactWords":["a","b"],"pickedModels":{"codex":["p/m1"]},"modelImages":{`+strings.Join(images, ",")+`}}`)
	s, err := SharedChecked()
	if err != nil || len(s.AllowIPs) != 2 || s.AllowIPs[0] != "10.0.0.0/8" {
		t.Fatalf("read %v, %v", s.AllowIPs, err)
	}
	shared := testing.AllocsPerRun(50, func() { SharedChecked() })
	copied := testing.AllocsPerRun(50, func() { LoadChecked() })
	if shared > 6 || shared*4 > copied {
		t.Errorf("an unchanged file: %.0f allocations a read (LoadChecked's copy: %.0f), want no copy", shared, copied)
	}
	writeRaw(t, `{"allowIPs":["127.0.0.1"]}`)
	if s, err := SharedChecked(); err != nil || len(s.AllowIPs) != 1 || s.AllowIPs[0] != "127.0.0.1" {
		t.Fatalf("a change: %v, %v", s.AllowIPs, err)
	}
	writeRaw(t, `{"allowIPs":["127.0.0.1"`)
	if _, err := SharedChecked(); err == nil {
		t.Fatal("a file that doesn't parse read as known")
	}
	if _, err := LoadChecked(); err == nil {
		t.Fatal("LoadChecked disagrees")
	}
}
