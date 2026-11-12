package davsync

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider kept on one computer (Provider.ThisDevice) is not sent out,
// and what another computer's sync brings neither replaces nor removes it
// (treydtw on X: a local model that exists only on this machine, synced to
// the others where it is unreachable).
func TestProviderOnThisDeviceStaysOnIt(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}
	a, b := newComputer(t), newComputer(t)
	sync := func(c computer) {
		t.Helper()
		c.use(t)
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}
	local := func(id, key string) provider.Provider {
		return provider.Provider{ID: id, Name: id, Chat: "http://127.0.0.1:11434/v1", Key: key, ThisDevice: true}
	}

	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	provider.Save(local("ollama", "ka"))
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	sync(a)
	if out, err := collect(cfg); err != nil || slices.ContainsFunc(out.Providers, func(p provider.Provider) bool { return p.ID == "ollama" }) || slices.Contains(out.Order, "ollama") {
		t.Fatalf("a sends its own model out: %v, %d providers, order %v", err, len(out.Providers), out.Order)
	}

	// b has a model of its own under the same id as a's, and another
	b.use(t)
	provider.Save(local("ollama", "kb"))
	provider.Save(local("lmstudio", "kb2"))
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	sync(b)
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "lmstudio=kb2", "ollama=kb"}) {
		t.Fatalf("b after joining: %v", got)
	}

	// a drops DeepSeek: it goes from b, and b's own stay
	a.use(t)
	if err := provider.Delete("deepseek"); err != nil {
		t.Fatal(err)
	}
	sync(a)
	sync(b)
	if got := ids(); !slices.Equal(got, []string{"lmstudio=kb2", "ollama=kb"}) {
		t.Fatalf("b after a dropped DeepSeek: %v", got)
	}
	sync(a)
	if got := ids(); !slices.Equal(got, []string{"ollama=ka"}) {
		t.Fatalf("a after b's syncs: %v", got)
	}

	// a lets its model sync: it goes out, but b keeps its own under that id
	if err := provider.SetThisDevice("ollama", false); err != nil {
		t.Fatal(err)
	}
	sync(a)
	sync(b)
	if got := ids(); !slices.Equal(got, []string{"lmstudio=kb2", "ollama=kb"}) {
		t.Fatalf("b after a's model synced: %v", got)
	}
	// and a model another computer sends out does not come to one kept here
	b.use(t)
	if ps, _ := provider.Stored(); !slices.ContainsFunc(ps, func(p provider.Provider) bool { return p.ID == "ollama" && p.ThisDevice }) {
		t.Fatal("b's own ollama lost its mark")
	}
}
