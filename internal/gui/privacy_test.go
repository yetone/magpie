package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func TestPrivacyModeSharedAndPersisted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := settings.Save(settings.Settings{Lang: "zh", Currency: "cny"}); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(method, path, strings.NewReader(body)))
		return r
	}
	read := func(r *httptest.ResponseRecorder) bool {
		t.Helper()
		var s struct {
			On bool
		}
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &s) != nil {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		return s.On
	}
	if read(request("GET", "/api/settings/privacy", "")) {
		t.Fatal("privacy mode must start off")
	}
	// Two already-open webviews both wake from the same change, even if
	// their web storage is isolated. Neither waits for another page load.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest("GET", "/api/settings/privacy?wait=1&on=0", nil).WithContext(ctx))
			results <- r
		}()
	}
	if !read(request("POST", "/api/settings/privacy", `{"on":true}`)) {
		t.Fatal("privacy mode did not enable")
	}
	for range 2 {
		select {
		case r := <-results:
			if !read(r) {
				t.Fatal("privacy mode did not reach the other window")
			}
		case <-ctx.Done():
			t.Fatal("privacy change did not reach both windows")
		}
	}
	if s := settings.Load(); !s.PrivacyMode || s.Lang != "zh" || s.Currency != "cny" {
		t.Fatalf("unrelated preferences changed: %+v", s)
	}
	// An older Settings form must not turn the separate toggle off.
	if r := request("POST", "/api/settings", `{"theme":"dark","lang":"en"}`); r.Code != http.StatusOK || !settings.Load().PrivacyMode {
		t.Fatalf("settings save: %d %s", r.Code, r.Body)
	}
	if r := request("GET", "/boot.js", ""); !strings.Contains(r.Body.String(), `"privacyMode":true`) {
		t.Fatalf("startup preference missing: %s", r.Body)
	}
	// A fresh handler/process reads the persisted choice.
	r := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(r, httptest.NewRequest("GET", "/api/settings/privacy", nil))
	if !read(r) {
		t.Fatal("restart lost privacy mode")
	}
	for _, body := range []string{`{}`, `{"on":null}`, `{"on":"false"}`, `broken`} {
		if r := request("POST", "/api/settings/privacy", body); r.Code != http.StatusBadRequest || !settings.Load().PrivacyMode {
			t.Fatalf("invalid toggle changed setting: %s: %d", body, r.Code)
		}
	}
	if read(request("POST", "/api/settings/privacy", `{"on":false}`)) {
		t.Fatal("privacy mode did not disable")
	}
	stored, err := json.Marshal(settings.Load())
	if err != nil || strings.Contains(string(stored), `"privacyMode"`) {
		t.Fatalf("disabled privacy mode should be omitted from settings: %s, %v", stored, err)
	}
	if r := request("GET", "/boot.js", ""); !strings.Contains(r.Body.String(), `"privacyMode":false`) {
		t.Fatalf("boot must explicitly enable synchronization while off: %s", r.Body)
	}
	// Closing a hidden webview cancels its outstanding wait.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/api/settings/privacy?wait=1&on=0", nil).WithContext(cancelled))
	if r.Body.Len() != 0 {
		t.Fatalf("cancelled request returned a state: %s", r.Body)
	}
}
