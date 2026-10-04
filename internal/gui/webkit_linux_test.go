package gui

import "testing"

// Under Wayland the pages are drawn without accelerated compositing, which
// flickered (myxxts); X11 is left alone, and so is a user's own setting.
func TestWebkitDefaults(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"wayland", map[string]string{"XDG_SESSION_TYPE": "wayland"}, "1"},
		{"wayland display only", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "1"},
		{"x11", map[string]string{"XDG_SESSION_TYPE": "x11"}, ""},
		{"user turned it on again", map[string]string{"XDG_SESSION_TYPE": "wayland", "WEBKIT_DISABLE_COMPOSITING_MODE": "0"}, "0"},
		{"user chose dmabuf off", map[string]string{"XDG_SESSION_TYPE": "wayland", "WEBKIT_DISABLE_DMABUF_RENDERER": "1"}, ""},
	} {
		env := map[string]string{}
		for k, v := range c.env {
			env[k] = v
		}
		webkitDefaultsFor(func(k string) string { return env[k] }, func(k, v string) error { env[k] = v; return nil })
		if got := env["WEBKIT_DISABLE_COMPOSITING_MODE"]; got != c.want {
			t.Errorf("%s: WEBKIT_DISABLE_COMPOSITING_MODE=%q, want %q", c.name, got, c.want)
		}
	}
}
