package gui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
)

// A browser magpie's folders are out of reach of downloads the extension,
// pointed at the page's own address and given a gateway key of its own,
// the same key each time while it is on.
func TestBrowserExtensionDownload(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	browserRoutes(mux, folderOnly{})
	download := func(body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/library/browser/extension", strings.NewReader(body)))
		if w.Code != 200 {
			return w.Code, ""
		}
		if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="Magpie Browser Extension.zip"` {
			t.Errorf("Content-Disposition %q", got)
		}
		zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if f.Name == "Magpie Browser Extension/config.js" {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				return 200, string(b)
			}
		}
		t.Fatal("no config.js in the zip")
		return 0, ""
	}
	keyOf := func(config string) string {
		m := regexp.MustCompile(`KEY = "([^"]*)"`).FindStringSubmatch(config)
		if m == nil {
			t.Fatalf("no KEY in %q", config)
		}
		return m[1]
	}

	_, config := download(`{"at":"http://localhost:3430"}`)
	if !strings.Contains(config, `GATEWAY = "ws://localhost:3430"`) {
		t.Fatalf("config %q isn't pointed at the page's address", config)
	}
	first := keyOf(config)
	who, ok := access.Authenticate(first)
	if !ok {
		t.Fatalf("the zip's key %q isn't a gateway key", first)
	}
	if got := gateway.OwnBrowserKey(); got != who.KeyID {
		t.Fatalf("the own browser key recorded is %q, want %q", got, who.KeyID)
	}
	// it connects the extension and nothing else: the zip is in Downloads
	if !who.Browser || !who.ReadOnly || who.Issues {
		t.Fatalf("the zip's key isn't held to the extension: %+v", who)
	}
	_, config = download(`{"at":"https://magpie.example.com/"}`)
	if !strings.Contains(config, `GATEWAY = "wss://magpie.example.com"`) || keyOf(config) != first {
		t.Fatalf("a second download got %q, want the same key at wss", config)
	}
	if keys, _ := access.List(); len(keys) != 1 || keys[0].Name != browserKeyName {
		t.Fatalf("keys after two downloads: %+v", keys)
	}
	// a key turned off isn't handed out again
	if _, err := access.Update("off-key", access.Change{Key: who.KeyID}); err != nil {
		t.Fatal(err)
	}
	_, config = download(`{"at":"http://localhost:3430"}`)
	if again := keyOf(config); again == first {
		t.Fatal("a key turned off was put in the extension again")
	} else if w, ok := access.Authenticate(again); !ok {
		t.Fatal("the new key isn't on")
	} else if gateway.OwnBrowserKey() != w.KeyID {
		t.Fatal("the new key isn't recorded as the own browser's")
	}
	// one a magpie from before made with full access is held to the
	// extension before it is handed out again, the same secret
	old, err := access.Update("add-key", access.Change{Name: browserKeyName})
	if err != nil {
		t.Fatal(err)
	}
	oldWho, _ := access.Authenticate(old)
	if err := gateway.SetOwnBrowserKey(oldWho.KeyID); err != nil {
		t.Fatal(err)
	}
	_, config = download(`{"at":"http://localhost:3430"}`)
	if keyOf(config) != old {
		t.Fatal("the key a magpie from before made wasn't handed out again")
	}
	if w, ok := access.Authenticate(old); !ok || !w.Browser || !w.ReadOnly {
		t.Fatalf("the key a magpie from before made isn't held to the extension: %+v", w)
	}
	for _, at := range []string{"ftp://x", "http://u:p@x:1", "http://x/path", "http://x?k=1", "javascript:alert(1)"} {
		b, _ := json.Marshal(map[string]string{"at": at})
		if code, _ := download(string(b)); code != http.StatusBadRequest {
			t.Errorf("at %q: %d, want 400", at, code)
		}
	}
}

// Away: a page looked at from another computer downloads the extension.
func TestBrowserAwayFromAnotherComputer(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	browserRoutes(mux, folderOnly{})
	for addr, want := range map[string]bool{"127.0.0.1:5000": false, "[::1]:5000": false, "192.168.1.20:5000": true} {
		r := httptest.NewRequest("GET", "/api/library/browser", nil)
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var b browserJSON
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		// a test run inside a container is away from every page
		if b.Away != want && !(b.Away && !want && gateway.InContainer()) {
			t.Errorf("%s: away %v, want %v", addr, b.Away, want)
		}
	}
}
