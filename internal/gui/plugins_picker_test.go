package gui

import (
	"context"
	"testing"
)

// pickerWin is the app as a Windows: the picker is offered for it, and no
// other method is reached from a page this test doesn't run.
type pickerWin struct{ Windows }

// The plugin market's folder picker is offered only where magpie can show
// one: the app has the system's, `magpie web` serves a browser and has
// none, and a page with no host at all is offered none either.
func TestPluginsPickerWhereItCan(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, c := range []struct {
		name string
		w    Windows
		want bool
	}{
		{"the app", &pickerWin{}, true},
		{"magpie web", webHost{}, false},
		{"no host", nil, false},
	} {
		if got := pluginsState(context.Background(), c.w).Picker; got != c.want {
			t.Errorf("%s: Picker = %v, want %v", c.name, got, c.want)
		}
	}
}
