package lastgood

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A file edited by hand that doesn't parse is left as it is; one a crash
// left empty or zeroed is kept aside and written over, as Keep does.
func TestKeepEditedLeavesAnEdit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	os.WriteFile(Bak(p), []byte(`{"a":1}`), 0o600)
	for name, b := range map[string][]byte{"slip": []byte(`{"a":1,}`), "cut": []byte(`{"a":`), "zeros-and-text": append(make([]byte, 4), 'x')} {
		os.WriteFile(p, b, 0o600)
		err := KeepEdited(p, JSON, 0o600)
		if !errors.Is(err, ErrEdited) {
			t.Errorf("%s: %v", name, err)
		}
		if got, _ := os.ReadFile(p); !bytes.Equal(got, b) {
			t.Errorf("%s: changed", name)
		}
		if kept, _ := filepath.Glob(p + ".bad-*"); len(kept) != 0 {
			t.Errorf("%s: kept aside as for a write: %v", name, kept)
		}
	}
	for name, b := range map[string][]byte{"zeroed": make([]byte, 64), "blank": []byte(" \n"), "bom": []byte("\xef\xbb\xbf \n"), "good": []byte(`{"a":2}`)} {
		os.WriteFile(p, b, 0o600)
		if err := KeepEdited(p, JSON, 0o600); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	os.Remove(p)
	if err := KeepEdited(p, JSON, 0o600); err != nil {
		t.Errorf("not there: %v", err)
	}
}
