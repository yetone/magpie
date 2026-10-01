package gui

import (
	"net/url"
	"testing"
)

// The panel's link to one request opens the Routing page on it: its req is
// a parameter of the window's page, not part of the view's name (it was
// escaped whole in v0.1.458, so the window opened on no tab at all); a tab
// named on the command line is only ever a name.
func TestMainURL(t *testing.T) {
	for _, c := range []struct {
		view, want string
	}{
		{mainView(url.Values{"view": {"routing"}, "req": {"42"}}), "/?view=routing&req=42&lang=en"},
		{mainView(url.Values{"view": {"settings"}}), "/?view=settings&lang=en"},
		{mainView(url.Values{"view": {"usage"}, "tab": {"requests"}, "provider": {"relay team"}, "agent": {"claude-desktop"}}), "/?view=usage&tab=requests&provider=relay+team&agent=claude-desktop&lang=en"},
		{argView("settings"), "/?view=settings&lang=en"},
		{argView("usage&tab=x"), "/?view=usage%26tab%3Dx&lang=en"},
	} {
		got := mainURL(c.view, "&lang=en")
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.view, got, c.want)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if c.view == "routing&req=42" && (u.Query().Get("view") != "routing" || u.Query().Get("req") != "42") {
			t.Errorf("%q: view %q req %q", got, u.Query().Get("view"), u.Query().Get("req"))
		}
	}
}
