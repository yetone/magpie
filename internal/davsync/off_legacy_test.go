package davsync

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/settings"
)

// dateLibrary puts the library's files at at: changed("library") reads
// their times from the kernel's clock.
func dateLibrary(t *testing.T, at time.Time) {
	t.Helper()
	os.Chtimes(filepath.Join(settings.Dir(), "library.json"), at, at)
	os.Chtimes(library.Dir(), at, at)
	filepath.WalkDir(library.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		os.Chtimes(p, at, at)
		return nil
	})
}

// #3356: a state written before st.Off was recorded (a magpie upgraded
// from before #6428): the last sync left the library out but wrote no Off
// mark, so re-enabling the library must still join it like a first sync —
// the computer's older content gives way to the server's newer one, never
// the other way round.
func TestSyncPartTurnedBackOnWithoutOffMark(t *testing.T) {
	fake, cfg := scopeServer(t)

	// b has older library content and syncs with the library left out:
	// its first sync starts the folder, without the library
	b := newComputer(t)
	b.use(t)
	saySharedText(t, "older")
	dateLibrary(t, time.Now().Add(-time.Hour))
	no := false
	without := cfg
	without.Library = &no
	Configure(without)
	syncOK(t)

	// a joins with newer library content: the library unset on the server
	// lets a's go up (#5642)
	a := newComputer(t)
	a.use(t)
	saySharedText(t, "newer")
	Configure(cfg)
	syncOK(t)
	if got := serverBundle(t, fake).Library; got == nil || got.Texts["default"] != "newer" {
		t.Fatalf("a's library on the server: %+v", got)
	}

	// b syncs again, still without the library: the state records the
	// server's library and nothing of it here
	b.use(t)
	syncOK(t)

	// the state as a magpie before Off was recorded left it: no Off mark
	p := filepath.Join(settings.Dir(), "sync-state.json")
	sb, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(sb, &st); err != nil {
		t.Fatal(err)
	}
	delete(st, "off")
	ob, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, ob, 0o600); err != nil {
		t.Fatal(err)
	}

	// b enables the library and syncs: the server's newer library comes
	// in, b's older text replaced and kept aside — never up over it
	Configure(cfg)
	v := syncOK(t)
	if got := serverBundle(t, fake).Library; got == nil || got.Texts["default"] != "newer" {
		t.Fatalf("b's older library went up over the server's: %+v", got)
	}
	if got := sharedText(); got != "newer" {
		t.Fatalf("b's library after the sync: %q", got)
	}
	if v.Notice == nil || !slices.Equal(v.Notice.Here, []string{"library"}) {
		t.Fatalf("no notice about b's replaced library: %+v", v.Notice)
	}
}
