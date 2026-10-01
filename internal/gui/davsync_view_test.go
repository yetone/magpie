package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
)

// The Settings page re-reads the WebDAV view when it is redrawn (the CLI
// can have changed the setup behind the window), so the endpoint must
// answer from the setup as it is on disk, never from a copy made when the
// page was first drawn. The view's keys/agents/library are what the page's
// description and the form's ticks are built from.
func TestDavsyncViewFollowsTheSetup(t *testing.T) {
	sandboxHome(t)
	mux := http.NewServeMux()
	backupRoutes(mux, folderOnly{})
	view := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/davsync", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}

	// off: the defaults the form's ticks would show
	if v := view(); v["on"] != false || v["keys"] != true || v["agents"] != true || v["library"] != true {
		t.Fatalf("off: %v", v)
	}

	// what `magpie webdav on … agents=no library=no` leaves (the CLI's own
	// defaults for what it isn't told: keys yes), behind the window's back
	no := false
	if err := davsync.Configure(davsync.Config{URL: "https://dav.jianguoyun.com/dav/", Passphrase: "correct horse", Keys: true, Agents: false, Library: &no}); err != nil {
		t.Fatal(err)
	}
	if v := view(); v["on"] != true || v["keys"] != true || v["agents"] != false || v["library"] != false || v["kind"] != "webdav" {
		t.Fatalf("after the CLI change: %v", v)
	}

	// what `magpie s3 on …` leaves: the same re-read, a bucket's view
	if err := davsync.Off(); err != nil {
		t.Fatal(err)
	}
	if err := davsync.Configure(davsync.Config{URL: "s3://bucket/prefix", User: "key-id", Password: "secret", Passphrase: "correct horse",
		Endpoint: "https://example.com", Region: "auto", PathStyle: true, Keys: false, Agents: true}); err != nil {
		t.Fatal(err)
	}
	if v := view(); v["on"] != true || v["kind"] != "s3" || v["endpoint"] != "https://example.com" || v["keys"] != false || v["agents"] != true {
		t.Fatalf("after an S3 change: %v", v)
	}

	// and what `magpie webdav off` leaves: off, the defaults back
	if err := davsync.Off(); err != nil {
		t.Fatal(err)
	}
	if v := view(); v["on"] != false || v["keys"] != true || v["agents"] != true || v["library"] != true {
		t.Fatalf("off again: %v", v)
	}
}
