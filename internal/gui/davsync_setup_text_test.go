package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
)

// Settings › Sync's Copy setup and its paste on another computer
// (WJaunita45604 on X), through the routes the page calls: the text comes
// sealed, opens with the passphrase into the setup the form fills, says a
// wrong passphrase as such, and saves nothing on the computer it opens on.
func TestDavsyncSetupTextRoutes(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	backupRoutes(mux, folderOnly{})
	post := func(action, body string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/davsync/"+action, strings.NewReader(body)))
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v
	}
	if code, v := post("setup-text", "{}"); code != 400 || v["error"] != davsync.ErrSetupOff.Error() {
		t.Fatalf("sync off: %d %v", code, v)
	}
	if err := davsync.Configure(davsync.Config{URL: "https://dav.jianguoyun.com/dav/", User: "me@example.com", Password: "app-password-9z", Passphrase: "correct horse", Keys: true}); err != nil {
		t.Fatal(err)
	}
	code, v := post("setup-text", "{}")
	text, _ := v["text"].(string)
	if code != 200 || !strings.HasPrefix(text, davsync.SetupPrefix) || strings.Contains(text, "app-password") {
		t.Fatalf("copy: %d %v", code, v)
	}
	if err := davsync.Off(); err != nil { // the other computer: sync off there
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"text": text, "passphrase": "wrong horse"})
	if code, v := post("setup-open", string(in)); code != 400 || v["error"] != davsync.ErrSetupPassphrase.Error() {
		t.Fatalf("wrong passphrase: %d %v", code, v)
	}
	in, _ = json.Marshal(map[string]string{"text": text, "passphrase": "correct horse"})
	code, v = post("setup-open", string(in))
	if code != 200 || v["url"] != "https://dav.jianguoyun.com/dav/" || v["user"] != "me@example.com" || v["password"] != "app-password-9z" || v["keys"] != true {
		t.Fatalf("open: %d %v", code, v)
	}
	if _, on := davsync.Load(); on {
		t.Fatal("opening a pasted setup turned sync on")
	}
}
