package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// `magpie update` on a version newer than its channel's (realfxw on X:
// 0.1.4445 on Daily, whose pick was 0.1.4395) said it was up to date, and
// nothing came until Daily passed it. It now says it is newer than the
// channel's, and how to update now. On the channel's own version it is up
// to date as before.
func TestUpdateAheadOfChannelSaysSo(t *testing.T) {
	groupsHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := "0.1.4472"
		if r.URL.Query().Get("channel") == "daily" {
			v = "0.1.4395"
		}
		json.NewEncoder(w).Encode(update.Release{Version: v})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	if err := settings.Save(settings.Settings{UpdateChannel: settings.UpdateDaily}); err != nil {
		t.Fatal(err)
	}
	was := version
	t.Cleanup(func() { version = was })

	version = "0.1.4445"
	out, err := stdoutOf(t, func() error { return updateCmd([]string{"update", "check"}) })
	if err != nil || !strings.Contains(out, "is newer than the daily channel's 0.1.4395") || !strings.Contains(out, "magpie update channel every") || strings.Contains(out, "up to date") {
		t.Fatalf("ahead of daily: %v %q", err, out)
	}
	version = "0.1.4395"
	out, err = stdoutOf(t, func() error { return updateCmd([]string{"update", "check"}) })
	if err != nil || !strings.Contains(out, "is up to date on the daily channel") {
		t.Fatalf("daily's own version: %v %q", err, out)
	}
}
