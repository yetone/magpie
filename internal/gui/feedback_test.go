package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

// Report a problem copies the version, the system, the architecture and
// where magpie is open, and nothing that names the user: no home folder,
// user name or host. The post it opens carries no part of it in its link
// (River_WLan on X).
func TestFeedbackBlockHoldsNoPrivateFacts(t *testing.T) {
	sandboxHome(t)
	rec := httptest.NewRecorder()
	webPageHandler(t, webHost{}, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/feedback?surface=browser", nil))
	var got struct{ Text, URL string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("feedback: %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{"magpie " + Version, "OS: ", "Arch: " + runtime.GOARCH, "Opened in: browser"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("block lacks %q:\n%s", want, got.Text)
		}
	}
	if got.URL != FeedbackURL || strings.ContainsAny(got.URL, "?#") {
		t.Errorf("post link %q carries something", got.URL)
	}
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	for _, secret := range []string{home, host, os.Getenv("USER"), os.Getenv("USERNAME")} {
		if secret != "" && strings.Contains(got.Text, secret) {
			t.Errorf("block holds %q:\n%s", secret, got.Text)
		}
	}
	if strings.Contains(osVersion(), "\n") || osVersion() == "" {
		t.Errorf("os version %q", osVersion())
	}
	if runtime.GOOS == "darwin" && !osVersionRE.MatchString(osVersion()) {
		t.Errorf("macOS without its version, as a window launched from Finder has it: %q", osVersion())
	}
	// anything but browser is the app's window
	if !strings.Contains(diagnostics("../x"), "Opened in: app") {
		t.Error("an unknown surface is not the app")
	}
}
