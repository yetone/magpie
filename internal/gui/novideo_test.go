package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Where the app's webview would crash on a <video> (Linux without
// gst-plugins-good), boot.js says so before the page draws, and the
// state the page loads first doesn't need to: the masking demo read it from
// /api/state's settings, which never carried it, set its video and the web
// process aborted on omarchy (avk458 on X). A browser tab plays video in
// the user's own browser and is never told.
func TestBootSaysNoVideo(t *testing.T) {
	builtinHome(t)
	prev := webkitPlaysVideo
	t.Cleanup(func() { webkitPlaysVideo = prev })
	boot := func(h http.Handler) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		return rec.Body.String()
	}
	webkitPlaysVideo = func() bool { return false }
	if b := boot(Handler(nil, nil)); !strings.Contains(b, `"noVideo":true`) {
		t.Fatalf("the app, with no video: %s", b)
	}
	if b := boot(webPageHandler(t, webHost{}, nil)); strings.Contains(b, "noVideo") {
		t.Fatalf("a browser tab plays its own video: %s", b)
	}
	webkitPlaysVideo = func() bool { return true }
	if b := boot(Handler(nil, nil)); strings.Contains(b, "noVideo") {
		t.Fatalf("the app, with video: %s", b)
	}
}
