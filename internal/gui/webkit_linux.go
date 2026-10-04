package gui

import "os"

// webkitDefaults sets how WebKitGTK draws the pages before the app makes
// its first webview. Under Wayland its accelerated compositing flickers the
// window on a click or a hover (myxxts on Discord: Fedora 44, KDE, AMD
// Radeon 780M; Wails already turns DMA-BUF off for NVIDIA only), and with
// it off the pages draw steadily and, as they said, smoother. A user who
// set either variable keeps what they set; WEBKIT_DISABLE_COMPOSITING_MODE=0
// turns compositing back on.
func webkitDefaults() {
	webkitDefaultsFor(os.Getenv, os.Setenv)
}

func webkitDefaultsFor(getenv func(string) string, setenv func(string, string) error) {
	if getenv("XDG_SESSION_TYPE") != "wayland" && getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	if getenv("WEBKIT_DISABLE_COMPOSITING_MODE") != "" || getenv("WEBKIT_DISABLE_DMABUF_RENDERER") != "" {
		return
	}
	setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
}
