package davsync

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// A providers.json that can't be read is never pushed as no providers: a
// sync stops on it, the server keeps the providers, and another computer
// mirroring the server keeps them too.
func TestSyncStopsOnUnreadableProviders(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}

	a, b := newComputer(t), newComputer(t)
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("b's providers: %v", got)
	}

	// a's file is cut short
	a.use(t)
	whole, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	broken := whole[:len(whole)/2]
	if err := os.WriteFile(provider.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	puts := fake.puts
	if err := Now(ctx); !errors.Is(err, provider.ErrUnreadable) {
		t.Fatalf("a synced a broken providers.json: %v", err)
	}
	if fake.puts != puts {
		t.Fatal("a pushed a broken providers.json")
	}
	if after, _ := os.ReadFile(provider.Path()); !bytes.Equal(after, broken) {
		t.Fatal("a's broken providers.json was written over")
	}
	if _, err := backup.Collect(true, "test"); !errors.Is(err, provider.ErrUnreadable) {
		t.Fatalf("a backup of a broken providers.json: %v", err)
	}
	remote, err := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if err != nil || len(remote.Providers) != 1 {
		t.Fatalf("on the server: %v %+v", err, remote.Providers)
	}
	b.use(t)
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("b lost its providers to a's broken file: %v", got)
	}
}
