package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codexAppReads has the Codex app (and a CLI) read every config when app
// is true, the CLI alone when it is false; put back when the test ends.
func codexAppReads(t *testing.T, app *bool) {
	t.Helper()
	was := codexReaders
	t.Cleanup(func() { codexReaders = was })
	codexReaders = func(place) []CodexReader {
		out := []CodexReader{{Kind: "cli", Version: "0.163.0"}}
		if *app {
			out = append(out, CodexReader{Kind: "app", Version: "0.163.0"})
		}
		return out
	}
}

// deskListIs has CodexDesktopList answer list, or not know it while list
// is nil.
func deskListIs(t *testing.T, list *string) {
	t.Helper()
	was := CodexDesktopList
	t.Cleanup(func() { CodexDesktopList = was })
	CodexDesktopList = func() ([]byte, bool) {
		if list == nil || *list == "" {
			return nil, false
		}
		return []byte(*list), true
	}
}

// laddieyao on Discord: Codex joined beside its ChatGPT sign-in, the Codex
// app's picker showed GPT alone while the CLI listed magpie's models. The
// app shows only the models of Codex's list a flag of OpenAI's names,
// unless Codex reads its list from a file: where the app is installed,
// magpie writes the list it hands Codex there and names it; the CLI alone
// keeps the list the gateway hands it.
func TestCodexDesktopListBesideSignIn(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, `model = "gpt-5.5"`+"\n")
	dir := filepath.Join(home, ".codex")
	desk := filepath.Join(dir, "magpie-desktop-models.json")
	app := true
	codexAppReads(t, &app)
	list := `{"models":[{"slug":"gpt-6-sol","base_instructions":"theirs"},{"slug":"fake/m1"}]}`
	deskListIs(t, &list)

	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	cfg := read()
	if !strings.Contains(cfg, `model_catalog_json = "`) || !strings.Contains(cfg, "magpie-desktop-models.json") {
		t.Fatalf("joined with the Codex app here, no list named for it:\n%s", cfg)
	}
	if strings.Contains(cfg, "model_provider =") {
		t.Fatalf("Codex's provider changed:\n%s", cfg)
	}
	if b, err := os.ReadFile(desk); err != nil || string(b) != list {
		t.Fatalf("the app's list: %q %v", b, err)
	}
	// connected beside the sign-in, as before: not magpie as its provider
	if !cx.Joined() || cx.AppOut() {
		t.Fatalf("joined %v, app out %v\n%s", cx.Joined(), cx.AppOut(), cfg)
	}
	if msg := cx.Check(); msg != "" {
		t.Fatalf("check: %s", msg)
	}

	// magpie's models changed: written again at the next sync
	list = `{"models":[{"slug":"gpt-6-sol","base_instructions":"theirs"},{"slug":"fake/m2"}]}`
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(desk); string(b) != list {
		t.Fatalf("not written again: %s", b)
	}

	// a list not known now (the backend not answering) is unknown: the
	// file as it was stays, never an empty one
	was := list
	list = ""
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(desk); string(b) != was || !strings.Contains(read(), "magpie-desktop-models.json") {
		t.Fatalf("unknown list: %s\n%s", b, read())
	}
	list = was

	// a magpie model picked beside the sign-in: still Codex's provider,
	// the list still the app's
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !cx.Beside() || !strings.Contains(cfg, "magpie-desktop-models.json") || strings.Contains(cfg, "model_provider =") {
		t.Fatalf("beside: %v\n%s", cx.Beside(), cfg)
	}

	// magpie API: magpie's own catalog, as magpie is Codex's provider, and
	// the app's list gone
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || strings.Contains(cfg, "magpie-desktop-models.json") {
		t.Fatalf("api:\n%s", cfg)
	}
	if _, err := os.Stat(desk); !os.IsNotExist(err) {
		t.Fatalf("api: the app's list is still there: %v", err)
	}
	if err := cx.Field("login").Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "magpie-desktop-models.json") || strings.Contains(cfg, "model_provider =") {
		t.Fatalf("back beside the sign-in:\n%s", cfg)
	}

	// disconnected: the key and the file go
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("unwired:\n%s", cfg)
	}
	if _, err := os.Stat(desk); !os.IsNotExist(err) {
		t.Fatalf("unwired: the app's list is still there: %v", err)
	}
}

// The CLI alone asks the gateway for its list, which follows each change
// as it is made: no file is written for it, and one written while the
// Codex app was installed goes once it isn't.
func TestCodexDesktopListCLIOnly(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, `model = "gpt-5.5"`+"\n")
	desk := filepath.Join(home, ".codex", "magpie-desktop-models.json")
	app := false
	codexAppReads(t, &app)
	list := `{"models":[{"slug":"gpt-6-sol"}]}`
	deskListIs(t, &list)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	if cfg := read(); strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("CLI alone:\n%s", cfg)
	}
	if _, err := os.Stat(desk); !os.IsNotExist(err) {
		t.Fatalf("CLI alone: a list written: %v", err)
	}
	app = true
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "magpie-desktop-models.json") {
		t.Fatalf("the app installed:\n%s", cfg)
	}
	app = false
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("the app gone:\n%s", cfg)
	}
	if _, err := os.Stat(desk); !os.IsNotExist(err) {
		t.Fatalf("the app gone: the list stays: %v", err)
	}
}

// A model_catalog_json the user set while joined is theirs: it stands, and
// no list of magpie's is written beside it. And a key naming magpie's list
// with the file gone (removed by hand) goes, as Codex loads no config then,
// while the list isn't known to write it again.
func TestCodexDesktopListKeepsTheirs(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, `model = "gpt-5.5"`+"\n")
	dir := filepath.Join(home, ".codex")
	desk := filepath.Join(dir, "magpie-desktop-models.json")
	app := true
	codexAppReads(t, &app)
	list := `{"models":[{"slug":"gpt-6-sol"}]}`
	deskListIs(t, &list)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	os.Remove(desk)
	list = ""
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("named with the file gone:\n%s", cfg)
	}
	list = `{"models":[{"slug":"gpt-6-sol"}]}`
	cfg := strings.Replace(read(), `model = "gpt-5.5"`, `model = "gpt-5.5"`+"\n"+`model_catalog_json = "mine.json"`, 1)
	os.WriteFile(filepath.Join(dir, "mine.json"), []byte(`{"models":[]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_catalog_json = "mine.json"`) {
		t.Fatalf("theirs:\n%s", cfg)
	}
	if _, err := os.Stat(desk); !os.IsNotExist(err) {
		t.Fatalf("written beside theirs: %v", err)
	}
}

// not_4O4 on X: the Windows app listed GPT alone, in a new window and a
// new project too. Its app-server reads the list only as it starts, so
// magpie keeps when it named the list (DeskAt), for one running from
// before to be restarted: the time it was named, kept while the list is
// unknown or its file rewritten, zero once the key is gone, and new when
// it is named again.
func TestCodexDesktopListNamedAt(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, `model = "gpt-5.5"`+"\n")
	app := true
	codexAppReads(t, &app)
	// not known at the Connect: nothing named yet, and no time
	var list string
	deskListIs(t, &list)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	if strings.Contains(read(), "model_catalog_json") || !cx.DeskAt().IsZero() {
		t.Fatalf("named before the list is known: %v\n%s", cx.DeskAt(), read())
	}
	// known a sync later, after the app-server started
	list = `{"models":[{"slug":"gpt-6-sol"},{"slug":"fake/m1"}]}`
	before := time.Now()
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	at := cx.DeskAt()
	if !strings.Contains(read(), "magpie-desktop-models.json") || at.Before(before) || at.After(time.Now()) {
		t.Fatalf("named at %v, want after %v\n%s", at, before, read())
	}
	// a new list in the file is no new naming: the app's next launch reads it
	list = `{"models":[{"slug":"gpt-6-sol"},{"slug":"fake/m2"}]}`
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if !cx.DeskAt().Equal(at) {
		t.Fatalf("named at %v after a new list, want %v", cx.DeskAt(), at)
	}
	// magpie API: the list goes, and with it the time
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if !cx.DeskAt().Equal(at) {
		t.Fatalf("a magpie model picked beside the sign-in: named at %v, want %v", cx.DeskAt(), at)
	}
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if !cx.DeskAt().IsZero() {
		t.Fatalf("api: named at %v\n%s", cx.DeskAt(), read())
	}
	// back beside the sign-in: named again, at a time of its own
	if err := cx.Field("login").Set(""); err != nil {
		t.Fatal(err)
	}
	if again := cx.DeskAt(); !again.After(at) {
		t.Fatalf("named again at %v, want after %v\n%s", again, at, read())
	}
}
