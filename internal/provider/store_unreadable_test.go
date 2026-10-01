package provider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeProviderFile(t *testing.T, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// What lists the providers learns that providers.json can't be read
// (FileError), rather than taking it for none; start-up and the gateway
// still go on with it as empty (#415's review).
func TestFileErrorSaysUnreadable(t *testing.T) {
	providerFileHome(t)
	if err := FileError(); err != nil {
		t.Fatalf("no file is a first use: %v", err)
	}
	writeProviderFile(t, originalProviderFile)
	if err := FileError(); err != nil {
		t.Fatalf("a file that reads: %v", err)
	}
	writeProviderFile(t, originalProviderFile[:40])
	err := FileError()
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("a file cut short: %v", err)
	}
	for _, want := range []string{Path(), "can't be read", "left it unchanged", "move it aside"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q doesn't say %q", err, want)
		}
	}
	if ps := load().Providers; len(ps) != 0 {
		t.Fatalf("load of a broken file: %+v", ps)
	}
	_ = All()
	_ = Catalog()
}

// An edit refused over a broken file says it was left unchanged and is to
// be fixed or moved aside, not only the decoder's complaint.
func TestWriteErrorsSayLeftUnchanged(t *testing.T) {
	p := Provider{ID: "new", Name: "New", Chat: "https://new.example.invalid/v1", Key: "synthetic-new"}
	for name, run := range map[string]func() error{
		"save":           func() error { return Save(p) },
		"restore":        func() error { _, _, err := Restore([]Provider{p}); return err },
		"restore-groups": func() error { return RestoreGroups([]Group{{ID: "g", Members: []string{"new/m"}}}) },
		"mirror":         func() error { return Mirror([]Provider{p}, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			providerFileHome(t)
			writeProviderFile(t, "{")
			err := run()
			if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "left it unchanged") || !strings.Contains(err.Error(), "fix it or move it aside") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// Stored and StoredGroups, which backups and sync carry, never give a
// broken file as no providers: another computer mirroring that would lose
// all of its own.
func TestStoredRefusesUnreadable(t *testing.T) {
	providerFileHome(t)
	if ps, err := Stored(); err != nil || len(ps) != 0 {
		t.Fatalf("no file: %v %v", ps, err)
	}
	writeProviderFile(t, originalProviderFile)
	if ps, err := Stored(); err != nil || len(ps) != 1 {
		t.Fatalf("a file that reads: %v %v", ps, err)
	}
	if gs, err := StoredGroups(); err != nil || len(gs) != 1 {
		t.Fatalf("a file that reads: %v %v", gs, err)
	}
	writeProviderFile(t, originalProviderFile[:len(originalProviderFile)-2])
	if _, err := Stored(); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Stored of a broken file: %v", err)
	}
	if _, err := StoredGroups(); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("StoredGroups of a broken file: %v", err)
	}
}

// providers.json is replaced through a file renamed over it, never cut
// short and written again in place: a read at that moment sees one whole
// catalog or the other. The file stays the user's alone.
func TestStoreReplacesAtomically(t *testing.T) {
	providerFileHome(t)
	writeProviderFile(t, originalProviderFile)
	before, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	// Windows reads a file's identity by its path when first compared: read
	// it now, while the path is still the old file
	os.SameFile(before, before)
	if err := Save(Provider{ID: "new", Name: "New", Chat: "https://new.example.invalid/v1", Key: "synthetic-new"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("providers.json was written in place, not renamed over")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o600 {
		t.Fatalf("providers.json is %v", after.Mode().Perm())
	}
	if ps, err := Stored(); err != nil || len(ps) != 2 {
		t.Fatalf("after the save: %v %v", ps, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(Path()), ".providers.json.*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}
