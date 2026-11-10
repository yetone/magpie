package davsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A computer set to follow the server takes its setup and sends none of
// its own (Ramon317317 on X: one computer the others all take theirs
// from, so they never drift apart). A change made on it is replaced on
// the next sync, kept in the sync folder and said; a change on both sides
// is the server's whichever is newer. The computer followed syncs both
// ways, and one set back syncs both ways again.
func TestFollowServer(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	now := func(t *testing.T) View {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
		return Status()
	}
	main, b := newComputer(t), newComputer(t)

	// following an empty server: an error, and nothing goes up
	b.use(t)
	provider.Save(provider.Provider{ID: "mine", Name: "Mine", Chat: "https://x/v1", Key: "kb"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := SetFollowServer(true); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); !errors.Is(err, errNothingToFollow) || fake.puts != 0 {
		t.Fatalf("following an empty server: %v, %d puts", err, fake.puts)
	}

	main.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	settings.Save(settings.Settings{Theme: "dark"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if v := now(t); v.FollowServer || fake.puts != 1 {
		t.Fatalf("main: %+v, %d puts", v, fake.puts)
	}

	// b joins following: the server's setup comes in, b's kept aside
	b.use(t)
	if v := now(t); !v.FollowServer || v.Notice == nil || !slices.Contains(v.Notice.Here, "providers") {
		t.Fatalf("b joined: %+v", v)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) || settings.Load().Theme != "dark" {
		t.Fatalf("b after joining: %v %q", got, settings.Load().Theme)
	}
	Dismiss()

	// a change made on b: replaced, never sent, kept and said
	puts := fake.puts
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "kb2"})
	v := now(t)
	if fake.puts != puts {
		t.Fatalf("b, following, wrote the server %d times", fake.puts-puts)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("b's own change stayed: %v", got)
	}
	if v.Notice == nil || !v.Notice.Followed || !slices.Equal(v.Notice.Here, []string{"providers"}) {
		t.Fatalf("b's notice: %+v", v.Notice)
	}
	kept, _ := filepath.Glob(filepath.Join(settings.Dir(), "sync", "*-this-computer"+backup.Ext))
	found := false
	for _, k := range kept {
		if old, err := backup.Open(must(os.ReadFile(k)), "correct horse"); err == nil && slices.ContainsFunc(old.Providers, func(p provider.Provider) bool { return p.ID == "kimi" }) {
			found = true
		}
	}
	if !found {
		t.Fatalf("b's change isn't kept: %v", kept)
	}
	Dismiss()

	// both change the settings, b's the newer: the server's still comes in
	main.use(t)
	settings.Save(settings.Settings{Theme: "light"})
	now(t)
	b.use(t)
	settings.Save(settings.Settings{Theme: "system", Lang: "zh"})
	puts = fake.puts
	if v := now(t); fake.puts != puts || settings.Load().Theme != "light" || settings.Load().Lang == "zh" || v.Notice == nil || !v.Notice.Followed {
		t.Fatalf("both changed: %q %q, %d puts, %+v", settings.Load().Theme, settings.Load().Lang, fake.puts-puts, v.Notice)
	}
	// nothing changed since: nothing brought or written
	Dismiss()
	if v := now(t); fake.puts != puts || v.Notice != nil {
		t.Fatalf("an unchanged sync: %d puts, %+v", fake.puts-puts, v.Notice)
	}

	// saving the server's form keeps the choice; set back, b syncs both ways
	if err := Configure(cfg); err != nil || !Status().FollowServer {
		t.Fatalf("the form dropped following: %v", err)
	}
	if err := SetFollowServer(false); err != nil {
		t.Fatal(err)
	}
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "kb2"})
	now(t)
	main.use(t)
	now(t)
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "kimi=kb2"}) {
		t.Fatalf("main after b synced both ways again: %v", got)
	}
}

// With sign-ins synced, one made on a computer following the server is
// still not sent: the merge of sign-ins would put it in the file, and the
// file is the followed computer's to write.
func TestFollowServerSendsNoSignIn(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", SignIns: true}
	main, b := newComputer(t), newComputer(t)
	main.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := SetFollowServer(true); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if err := plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
		m["kiro"] = json.RawMessage(`{"type":"oauth","access":"kiro-b","refresh":"kr","expires":2000,"accountId":"k@x.com"}`)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	puts := fake.puts
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.puts != puts {
		t.Fatalf("b, following, sent its sign-in: %d puts", fake.puts-puts)
	}
}
