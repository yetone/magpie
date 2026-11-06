package browserext

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The extension is written pointed at the gateway it is set up from, and
// written again only where it changed.
func TestWritePointsAtTheGateway(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ext")
	if err := Write(dir, "http://127.0.0.1:3425"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.js"))
	if !strings.Contains(string(cfg), `export const GATEWAY = "ws://127.0.0.1:3425";`) || !strings.Contains(string(cfg), `export const KEY = "";`) {
		t.Fatalf("config.js: %s", cfg)
	}
	var man struct {
		Version, Key string
		Background   struct {
			ServiceWorker string `json:"service_worker"`
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := json.Unmarshal(b, &man); err != nil || man.Version != Version() || man.Key == "" || man.Background.ServiceWorker != "background.js" {
		t.Fatalf("manifest: %v %+v", err, man)
	}
	old := time.Now().Add(-time.Hour)
	bg := filepath.Join(dir, "background.js")
	os.Chtimes(bg, old, old)
	if err := Write(dir, "http://127.0.0.1:4000/"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(filepath.Join(dir, "config.js"))
	if !strings.Contains(string(cfg), `"ws://127.0.0.1:4000"`) {
		t.Fatalf("another port: %s", cfg)
	}
	if fi, _ := os.Stat(bg); !fi.ModTime().Equal(old) {
		t.Fatal("background.js was written again unchanged")
	}
	if Write(dir, "ftp://x") == nil {
		t.Fatal("took an address that isn't http")
	}
}

// The extension's every file imports what config.js exports, so a written
// config.js has each name the source's has.
func TestConfigHasEveryExport(t *testing.T) {
	src, _ := File("config.js")
	cfg, err := config("http://127.0.0.1:3425", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if name, ok := strings.CutPrefix(line, "export const "); ok {
			name, _, _ = strings.Cut(name, " ")
			if !strings.Contains(string(cfg), "export const "+name+" ") {
				t.Errorf("written config.js has no %s: %s", name, cfg)
			}
		}
	}
}

// The zip a browser outside magpie's computer or container loads holds
// the extension's folder, pointed at the address it was asked for and
// carrying the key.
func TestZipCarriesAddressAndKey(t *testing.T) {
	b, err := Zip("https://magpie.example:8443", "mk-\"x")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, f := range zr.File {
		r, _ := f.Open()
		body, _ := io.ReadAll(r)
		r.Close()
		seen[f.Name] = string(body)
	}
	cfg := seen[Folder+"/config.js"]
	if !strings.Contains(cfg, `export const GATEWAY = "wss://magpie.example:8443";`) || !strings.Contains(cfg, `export const KEY = "mk-\"x";`) {
		t.Fatalf("config.js: %s", cfg)
	}
	for _, name := range []string{"manifest.json", "background.js", "popup.js"} {
		if seen[Folder+"/"+name] == "" {
			t.Errorf("no %s in %v", name, len(seen))
		}
	}
	if _, err := Zip("ftp://x", "k"); err == nil {
		t.Fatal("took an address that isn't http")
	}
}

// An extension is told to reload only into a newer version than its own.
func TestOlder(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.1.3", "0.1.4", true}, {"0.1.9", "0.1.10", true}, {"0.1.4", "0.1.4", false}, {"0.1.5", "0.1.4", false}, {"", "0.1.4", true}, {"0.2", "0.1.9", false}} {
		if got := Older(c.a, c.b); got != c.want {
			t.Errorf("Older(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// Refresh writes over the folders an older magpie wrote, and no other.
func TestRefreshWritesOnlyWrittenFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if Refresh("http://127.0.0.1:3425") {
		t.Fatal("wrote a folder no one had")
	}
	if _, err := os.Stat(Dir()); !os.IsNotExist(err) {
		t.Fatalf("made %s: %v", Dir(), err)
	}
	os.MkdirAll(Dir(), 0o755)
	os.WriteFile(filepath.Join(Dir(), "manifest.json"), []byte(`{"version": "0.1.0"}`), 0o644)
	if !Refresh("http://127.0.0.1:3425") {
		t.Fatal("didn't write the written folder")
	}
	b, _ := os.ReadFile(filepath.Join(Dir(), "manifest.json"))
	if !strings.Contains(string(b), `"version": "`+Version()+`"`) {
		t.Fatalf("manifest: %s", b)
	}
}
