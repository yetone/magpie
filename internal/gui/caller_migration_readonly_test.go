package gui

import (
	"net/http/httptest"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestReadOnlyLANMigrationDoesNotBlockSettingsOrKeys(t *testing.T) {
	sandboxHome(t)
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-fixture-readonly"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
	if err := settings.Save(s); err == nil {
		t.Skip("settings.json remains writable")
	}
	h := Handler(nil, nil)
	for _, path := range []string{"/api/settings", "/api/caller-keys", "/api/settings"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
}
