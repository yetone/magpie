package davsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const serverFile = "/dav/magpie/magpie.magpie-backup"

func scopeServer(t *testing.T) (*fakeDAV, Config) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
}

func syncOK(t *testing.T) View {
	t.Helper()
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := Status()
	if v.Error != "" {
		t.Fatalf("sync: %s", v.Error)
	}
	return v
}

func serverBundle(t *testing.T, fake *fakeDAV) backup.Bundle {
	t.Helper()
	b, err := backup.Open(onServer(fake, serverFile), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sharedText() string {
	iv, _ := library.ReadInstructions()
	return iv.Shared
}

func saySharedText(t *testing.T, text string) {
	t.Helper()
	if _, err := library.SaveInstructions(library.InstructionsChange{Shared: &text, Agents: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
}

func serverIDs(b backup.Bundle) []string {
	var out []string
	for _, p := range b.Providers {
		out = append(out, p.ID)
	}
	slices.Sort(out)
	return out
}

// #6428 (jinpwng): a computer that syncs only the library and memory
// keeps its own providers and settings, and never sends them up: the
// library comes in, the server keeps the other computer's providers and
// settings, and a change to either here stays here.
func TestSyncOnlyLibraryKeepsEachComputersOwn(t *testing.T) {
	fake, cfg := scopeServer(t)

	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "pa", Name: "A", Chat: "https://a/v1", Key: "ka"})
	settings.Save(settings.Settings{Theme: "dark"})
	os.MkdirAll(filepath.Join(string(a), ".claude"), 0o755)
	saySharedText(t, "Be brief.")
	Configure(cfg)
	syncOK(t)

	b := newComputer(t)
	b.use(t)
	os.MkdirAll(filepath.Join(string(b), ".claude"), 0o755)
	provider.Save(provider.Provider{ID: "pb", Name: "B", Chat: "https://b/v1", Key: "kb"})
	settings.Save(settings.Settings{Theme: "light"})
	no := false
	only := cfg
	only.Providers, only.Settings, only.Profiles, only.Agents = &no, &no, &no, false
	if err := Configure(only); err != nil {
		t.Fatal(err)
	}
	v := syncOK(t)
	if v.Providers || v.Settings || v.Profiles || !v.Library || !v.Memory {
		t.Fatalf("status: %+v", v)
	}
	if got := ids(); !slices.Equal(got, []string{"pb=kb"}) {
		t.Fatalf("b's providers: %v", got)
	}
	if s := settings.Load(); s.Theme != "light" {
		t.Fatalf("b's theme: %q", s.Theme)
	}
	if got := sharedText(); got != "Be brief." {
		t.Fatalf("b's library: %q", got)
	}
	// b changes its own: the server never gets them
	provider.Save(provider.Provider{ID: "pc", Name: "C", Chat: "https://c/v1", Key: "kc"})
	settings.Save(settings.Settings{Theme: "system"})
	syncOK(t)
	srv := serverBundle(t, fake)
	if got := serverIDs(srv); !slices.Equal(got, []string{"pa"}) {
		t.Fatalf("server's providers: %v", got)
	}
	if srv.Settings == nil || srv.Settings.Theme != "dark" {
		t.Fatalf("server's settings: %+v", srv.Settings)
	}

	// a changes its own: b keeps b's
	a.use(t)
	provider.Save(provider.Provider{ID: "pz", Name: "Z", Chat: "https://z/v1", Key: "kz"})
	syncOK(t)
	b.use(t)
	syncOK(t)
	if got := ids(); !slices.Equal(got, []string{"pb=kb", "pc=kc"}) {
		t.Fatalf("b's providers after a's change: %v", got)
	}
}

// A computer whose folder is empty, syncing only the library, puts none
// of its providers or settings on the server: the next computer to join
// keeps its own (#5642's unset rule) rather than taking them.
func TestSyncOnlyLibraryFirstSendsNoProviders(t *testing.T) {
	fake, cfg := scopeServer(t)
	b := newComputer(t)
	b.use(t)
	provider.Save(provider.Provider{ID: "pb", Name: "B", Chat: "https://b/v1", Key: "kb"})
	settings.Save(settings.Settings{Theme: "light"})
	no := false
	only := cfg
	only.Providers, only.Settings, only.Profiles = &no, &no, &no
	Configure(only)
	syncOK(t)
	srv := serverBundle(t, fake)
	if len(srv.Providers) != 0 || !unset(srv, "settings") {
		t.Fatalf("server got b's: providers %v, settings %+v", serverIDs(srv), srv.Settings)
	}
}

// A part turned back on is joined as on a first sync: this computer,
// which had none, takes the server's.
func TestSyncPartTurnedBackOn(t *testing.T) {
	_, cfg := scopeServer(t)
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "pa", Name: "A", Chat: "https://a/v1", Key: "ka"})
	Configure(cfg)
	syncOK(t)

	b := newComputer(t)
	b.use(t)
	no := false
	off := cfg
	off.Providers = &no
	Configure(off)
	syncOK(t)
	if got := ids(); len(got) != 0 {
		t.Fatalf("b's providers with them off: %v", got)
	}
	Configure(cfg)
	syncOK(t)
	if got := ids(); !slices.Equal(got, []string{"pa=ka"}) {
		t.Fatalf("b's providers turned back on: %v", got)
	}
}

// Preview says what the sync then does, changing nothing itself; a sync
// asked for by its token runs only while it would still do that.
func TestPreviewThenSync(t *testing.T) {
	fake, cfg := scopeServer(t)
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "pa", Name: "A", Chat: "https://a/v1", Key: "ka"})
	Configure(cfg)
	syncOK(t)

	b := newComputer(t)
	b.use(t)
	provider.Save(provider.Provider{ID: "pb", Name: "B", Chat: "https://b/v1", Key: "kb"})
	Configure(cfg)
	puts := fake.puts
	p, err := Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.puts != puts || !slices.Equal(ids(), []string{"pb=kb"}) {
		t.Fatalf("the preview changed something: %d puts, providers %v", fake.puts-puts, ids())
	}
	var prov PartPlan
	for _, pp := range p.Parts {
		if pp.Part == "providers" {
			prov = pp
		}
	}
	want := []Item{{Kind: "provider", Name: "A", Change: "added"}, {Kind: "provider", Name: "B", Change: "removed"}}
	if prov.Do != "in" || !prov.Replaces || !slices.Equal(prov.Items, want) {
		t.Fatalf("providers' plan: %+v", prov)
	}
	if p.Token == "" {
		t.Fatal("no token")
	}

	// a changes the server meanwhile: the preview is stale, nothing runs
	a.use(t)
	provider.Save(provider.Provider{ID: "pz", Name: "Z", Chat: "https://z/v1", Key: "kz"})
	syncOK(t)
	b.use(t)
	if err := SyncPreviewed(context.Background(), p.Token); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("a stale preview: %v", err)
	}
	if got := ids(); !slices.Equal(got, []string{"pb=kb"}) {
		t.Fatalf("a stale preview synced: %v", got)
	}
	p, err = Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncPreviewed(context.Background(), p.Token); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"pa=ka", "pz=kz"}) {
		t.Fatalf("after the previewed sync: %v", got)
	}
	// nothing left to do: every part the same
	p, err = Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, pp := range p.Parts {
		if pp.Do != "same" {
			t.Fatalf("after syncing, %s: %+v", pp.Part, pp)
		}
	}
}

// UndoSync puts back what the last sync brought in, on this computer
// only; a part changed here since is written over only when asked.
func TestUndoSync(t *testing.T) {
	fake, cfg := scopeServer(t)
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "pa", Name: "A", Chat: "https://a/v1", Key: "ka"})
	Configure(cfg)
	syncOK(t)
	b := newComputer(t)
	b.use(t)
	Configure(cfg)
	syncOK(t)

	a.use(t)
	provider.Save(provider.Provider{ID: "pz", Name: "Z", Chat: "https://z/v1", Key: "kz"})
	syncOK(t)
	b.use(t)
	v := syncOK(t)
	if v.Applied == nil || !slices.Equal(v.Applied.Parts, []string{"providers"}) {
		t.Fatalf("applied: %+v", v.Applied)
	}
	if got := ids(); !slices.Equal(got, []string{"pa=ka", "pz=kz"}) {
		t.Fatalf("b brought in: %v", got)
	}
	back, err := UndoSync(false)
	if err != nil || !slices.Equal(back, []string{"providers"}) {
		t.Fatalf("undo: %v %v", back, err)
	}
	if got := ids(); !slices.Equal(got, []string{"pa=ka"}) {
		t.Fatalf("b after undo: %v", got)
	}
	if Status().Applied != nil {
		t.Fatal("still undoable")
	}
	// it stays here: the next sync sends nothing, and the server keeps pz
	puts := fake.puts
	syncOK(t)
	if fake.puts != puts {
		t.Fatalf("the undo went up (%d puts)", fake.puts-puts)
	}
	if got := ids(); !slices.Equal(got, []string{"pa=ka"}) {
		t.Fatalf("b after the next sync: %v", got)
	}
	if got := serverIDs(serverBundle(t, fake)); !slices.Equal(got, []string{"pa", "pz"}) {
		t.Fatalf("server after b's undo: %v", got)
	}

	// changed here since: asked first
	a.use(t)
	provider.Save(provider.Provider{ID: "py", Name: "Y", Chat: "https://y/v1", Key: "ky"})
	syncOK(t)
	b.use(t)
	syncOK(t)
	provider.Save(provider.Provider{ID: "pb", Name: "B", Chat: "https://b/v1", Key: "kb"})
	var since *ChangedSince
	if _, err := UndoSync(false); !errors.As(err, &since) || !slices.Equal(since.Parts, []string{"providers"}) {
		t.Fatalf("undo over a change: %v", err)
	}
	if _, err := UndoSync(true); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"pa=ka"}) {
		t.Fatalf("b after undo anyway: %v", got)
	}
}
