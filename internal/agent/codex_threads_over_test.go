package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// notnico64 on Discord: with Codex connected to magpie, `codex resume`
// said "No sessions yet". Codex's resume picker (and the app's thread
// list) shows only the threads of the provider Codex is on, and with no
// ChatGPT sign-in magpie is Codex's provider, so every thread started on
// OpenAI before was out of the list. Tried with Codex 0.162.1's real
// picker: model_provider = "magpie" listed the magpie thread alone.
//
// Now the threads Codex started on OpenAI move to magpie while it is
// Codex's provider, in place (the id is one length), the file's time
// kept, and back at the Disconnect, with no copy of each. Another
// provider's threads stay, and OpenAI's stay when Codex was on another
// provider before magpie, whose threads move instead (hxy920830 on X).
//
// Threads Codex has packed (.zst) stayed on OpenAI, which Codex 0.166's
// app refuses to resume on magpie ("This chat uses openai, but your
// current provider is magpie", 毅 on Discord). A packed one with a turn
// in the last 30 days moves too, rewritten with a copy kept, and back.
func TestCodexThreadsFollowMagpieIn(t *testing.T) {
	const (
		plain  = "019a0000-0000-7000-8000-0000000000c1"
		packed = "019a0000-0000-7000-8000-0000000000c2"
		theirs = "019a0000-0000-7000-8000-0000000000c3"
	)
	t.Run("on OpenAI before", func(t *testing.T) {
		home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n")
		dir := filepath.Join(home, ".codex")
		plainPath := codexThreadOn(t, dir, plain, "openai")
		packedPath := codexThreadOn(t, dir, packed, "openai")
		packThread(t, packedPath)
		codexThreadOn(t, dir, theirs, "custom")
		before, _ := os.Stat(plainPath)
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "magpie" {
			t.Fatalf("provider %q, want magpie as Codex's provider with no sign-in", p)
		}
		// listed where Codex is now, at the switch itself
		for id, want := range map[string]string{plain: "magpie", packed: "magpie", theirs: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("connected: thread %s on %q, want %q", id, p, want)
			}
		}
		if p := zstMetaProvider(t, packedPath+".zst"); p != "magpie" {
			t.Fatalf("packed rollout on %q, want magpie", p)
		}
		if l, _ := filepath.Glob(filepath.Join(settings.Dir(), "trash", "codex-provider", "*"+packed, "move.json")); len(l) != 1 {
			t.Fatalf("packed thread moved with no copy kept: %v", l)
		}
		b, _ := os.ReadFile(plainPath)
		if !strings.Contains(string(b), `"model_provider":"magpie"`) {
			t.Fatalf("rollout still on OpenAI:\n%s", b)
		}
		after, _ := os.Stat(plainPath)
		if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
			t.Fatalf("file changed beyond its provider: %v %d, was %v %d", after.ModTime(), after.Size(), before.ModTime(), before.Size())
		}
		// a sync leaves them so
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if p := codexRowOn(t, dir, plain); p != "magpie" {
			t.Fatalf("after a sync: %q", p)
		}
		if err := cx.Unwire(); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{plain: "openai", packed: "openai", theirs: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("disconnected: thread %s on %q, want %q", id, p, want)
			}
		}
		if b2, _ := os.ReadFile(plainPath); string(b2) != strings.Replace(string(b), `"model_provider":"magpie"`, `"model_provider":"openai"`, 1) {
			t.Fatalf("rollout not as it was:\n%s", b2)
		}
		if p := zstMetaProvider(t, packedPath+".zst"); p != "openai" {
			t.Fatalf("packed rollout back on %q, want openai", p)
		}
		// moved in place both ways: no copy of the history in the trash
		if l, _ := filepath.Glob(filepath.Join(settings.Dir(), "trash", "codex-provider", "*"+plain)); len(l) > 0 {
			t.Fatalf("copied: %v", l)
		}
	})
	// hxy920830 on X: Codex had been on a relay's table of the user's
	// own, and with magpie its provider the Codex app listed those threads
	// and refused to open them ("This chat uses custom, but your current
	// provider is magpie": "原来的会话可以看到，但是加载不出来模型了").
	// The threads of the provider Codex was on before magpie move as
	// OpenAI's do, and back to it at the Disconnect; OpenAI's stay, as
	// bringBack would give them to that provider.
	t.Run("on a provider of the user's before", func(t *testing.T) {
		cfg := "model = \"m\"\nmodel_provider = \"custom\"\n\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\n"
		home, _ := codexHome(t, "", cfg)
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, plain, "openai")
		theirsPath := codexThreadOn(t, dir, theirs, "custom")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "magpie" {
			t.Fatalf("provider %q, want magpie", p)
		}
		cx.Sync()
		for id, want := range map[string]string{plain: "openai", theirs: "magpie"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("connected: thread %s on %q, want %q", id, p, want)
			}
		}
		// "custom" is magpie's length: in place, no copy
		if l, _ := filepath.Glob(filepath.Join(settings.Dir(), "trash", "codex-provider", "*"+theirs)); len(l) > 0 {
			t.Fatalf("copied: %v", l)
		}
		if err := cx.Unwire(); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "custom" {
			t.Fatalf("provider %q after the Disconnect, want custom back", p)
		}
		for id, want := range map[string]string{plain: "openai", theirs: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("disconnected: thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(theirsPath); !strings.Contains(string(b), `"model_provider":"custom"`) {
			t.Fatalf("rollout not back on custom:\n%s", b)
		}
	})
	// a relay's id of another length is rewritten, a copy kept, the last
	// 30 days of it; an older thread stays for Sessions to move
	t.Run("on a relay's table of another length", func(t *testing.T) {
		const old = "019a0000-0000-7000-8000-0000000000c4"
		cfg := "model = \"m\"\nmodel_provider = \"myrelay\"\n\n[model_providers.myrelay]\nname = \"My relay\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\n"
		home, _ := codexHome(t, "", cfg)
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, plain, "openai")
		theirsPath := codexThreadOn(t, dir, theirs, "myrelay")
		packedPath := codexThreadOn(t, dir, packed, "myrelay")
		packThread(t, packedPath)
		codexThreadOn(t, dir, old, "myrelay")
		db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE threads SET updated_at = ? WHERE id = ?`, time.Now().Add(-40*24*time.Hour).Unix(), old); err != nil {
			t.Fatal(err)
		}
		db.Close()
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{plain: "openai", theirs: "magpie", packed: "magpie", old: "myrelay"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("connected: thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(theirsPath); !strings.Contains(string(b), `"model_provider":"magpie"`) {
			t.Fatalf("rollout still on myrelay:\n%s", b)
		}
		if p := zstMetaProvider(t, packedPath+".zst"); p != "magpie" {
			t.Fatalf("packed rollout on %q, want magpie", p)
		}
		if l, _ := filepath.Glob(filepath.Join(settings.Dir(), "trash", "codex-provider", "*"+theirs, "move.json")); len(l) != 1 {
			t.Fatalf("rewritten with no copy kept: %v", l)
		}
		if err := cx.Unwire(); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{plain: "openai", theirs: "myrelay", packed: "myrelay", old: "myrelay"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("disconnected: thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(theirsPath); !strings.Contains(string(b), `"model_provider":"myrelay"`) {
			t.Fatalf("rollout not back on myrelay:\n%s", b)
		}
	})
	// a provider a profile names is the user's to go on with there
	t.Run("on a provider a profile names", func(t *testing.T) {
		cfg := "model = \"m\"\nmodel_provider = \"custom\"\n\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\n\n[profiles.relay]\nmodel_provider = \"custom\"\n"
		home, _ := codexHome(t, "", cfg)
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, theirs, "custom")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		cx.Sync()
		if p := codexRowOn(t, dir, theirs); p != "custom" {
			t.Fatalf("thread on %q, want custom, where `codex -p relay` lists it", p)
		}
	})
}

// packThread packs a rollout as Codex does a week after its last turn:
// the .zst beside it, the plain file gone, its row still naming the plain
// path.
func packThread(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	z, _ := zstd.NewWriter(nil)
	if err := os.WriteFile(path+".zst", z.EncodeAll(b, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	z.Close()
	os.Chtimes(path+".zst", fi.ModTime(), fi.ModTime())
	os.Remove(path)
}

// zstMetaProvider is the model_provider of a packed rollout's session_meta.
func zstMetaProvider(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	z, _ := zstd.NewReader(nil)
	defer z.Close()
	out, err := z.DecodeAll(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	_, rest, ok := strings.Cut(line, `"model_provider":"`)
	if !ok {
		t.Fatalf("no provider in %s", line)
	}
	p, _, _ := strings.Cut(rest, `"`)
	return p
}
