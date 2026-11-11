package davsync

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/memory"
)

// Memory in sync: what one computer's agents learned is known on the
// other, merged memory by memory, a memory deleted on one goes from the
// other and doesn't come back, and with the switch off none goes.
func TestSyncMemory(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		if v := Status(); v.Error != "" {
			t.Fatal(v.Error)
		}
	}
	save := func(t *testing.T, text string) *memory.Memory {
		t.Helper()
		k, s := memory.Preference, memory.Global
		m, _, err := memory.Save(memory.Input{Text: &text, Kind: &k, Scope: &s}, memory.FromAgent, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	have := func(t *testing.T) []string {
		t.Helper()
		all, _, err := memory.All()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range all {
			out = append(out, m.Text)
		}
		slices.Sort(out)
		return out
	}
	want := func(t *testing.T, w ...string) {
		t.Helper()
		if got := have(t); !slices.Equal(got, w) {
			t.Fatalf("memory here: %q, want %q", got, w)
		}
	}

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	m := save(t, "Replies in Chinese")
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	fake.mu.Lock()
	sealed := string(fake.files["/dav/magpie/magpie.magpie-backup"])
	fake.mu.Unlock()
	if strings.Contains(sealed, "Replies in Chinese") {
		t.Fatal("a memory on the server in the clear")
	}
	if v := Status(); !v.Memory {
		t.Fatal("memory doesn't go by default")
	}

	b.use(t)
	save(t, "Builds run on mote")
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	want(t, "Builds run on mote", "Replies in Chinese") // joining: both, neither lost

	a.use(t)
	now(t)
	want(t, "Builds run on mote", "Replies in Chinese")

	// deleted on a: gone from b, and not brought back to a by b
	if err := memory.Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	now(t)
	b.use(t)
	now(t)
	want(t, "Builds run on mote")
	a.use(t)
	now(t)
	want(t, "Builds run on mote")

	// switched off on b: what b learns next stays on b
	b.use(t)
	off := false
	c := cfg
	c.Memory = &off
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	save(t, "Only on b")
	now(t)
	a.use(t)
	now(t)
	want(t, "Builds run on mote")
	// and a's memory still reaches the server through a
	save(t, "Learned on a")
	now(t)
	b.use(t)
	now(t)
	want(t, "Builds run on mote", "Only on b") // off: nothing comes either
}
