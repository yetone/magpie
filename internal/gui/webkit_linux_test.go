package gui

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What each case sets, from the environment magpie was started with:
// compositing off without a GPU (#2827) and under Wayland with no NVIDIA
// driver loaded (myxxts's flicker); DMA-BUF off only where NVIDIA's
// driver draws, never on a hybrid laptop whose screen the iGPU drives
// (ZDegress on X, #5176, #6184), where Wails' beta.24 had set it before
// webkitDefaults ran. A variable the user set, to 0 too, is theirs and
// takes the choice over; the setting overrides magpie's pick.
func TestWebkitDecide(t *testing.T) {
	on, off := true, false
	wl := map[string]string{"XDG_SESSION_TYPE": "wayland"}
	x11 := map[string]string{"XDG_SESSION_TYPE": "x11"}
	amd := webkitHW{gpu: true}
	hybrid := webkitHW{gpu: true, nvidiaLoaded: true}
	nvidia := webkitHW{gpu: true, nvidiaLoaded: true, nvidiaRenders: true}
	none := webkitHW{}
	const comp, dma = "WEBKIT_DISABLE_COMPOSITING_MODE", "WEBKIT_DISABLE_DMABUF_RENDERER"
	for _, c := range []struct {
		name    string
		session map[string]string
		start   map[string]string
		hw      webkitHW
		pref    *bool
		want    map[string]string
		auto    bool
		gpu     bool
	}{
		{"wayland, amd", wl, nil, amd, nil, map[string]string{comp: "1"}, false, false},
		{"wayland display only", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, nil, amd, nil, map[string]string{comp: "1"}, false, false},
		{"wayland, hybrid (ZDegress, niri)", wl, nil, hybrid, nil, map[string]string{}, true, true},
		{"wayland, nvidia draws", wl, nil, nvidia, nil, map[string]string{dma: "1"}, true, true},
		{"x11, amd", x11, nil, amd, nil, map[string]string{}, true, true},
		{"x11, nvidia (#6184's RTX 4060)", x11, nil, nvidia, nil, map[string]string{dma: "1"}, true, true},
		{"x11 without a gpu (xrdp)", map[string]string{"XDG_SESSION_TYPE": "x11", "DISPLAY": ":10"}, nil, none, nil, map[string]string{comp: "1"}, false, false},
		{"x11 without a gpu, nvidia loaded", x11, nil, webkitHW{nvidiaLoaded: true, nvidiaRenders: true}, nil, map[string]string{comp: "1"}, false, false},
		{"setting on, wayland amd", wl, nil, amd, &on, map[string]string{}, false, true},
		{"setting on, nvidia draws: dma-buf still off", wl, nil, nvidia, &on, map[string]string{dma: "1"}, true, true},
		{"setting off, hybrid", wl, nil, hybrid, &off, map[string]string{comp: "1"}, true, false},
		{"setting off, x11", x11, nil, amd, &off, map[string]string{comp: "1"}, true, false},
		{"user turned compositing on again", wl, map[string]string{comp: "0"}, amd, nil, map[string]string{comp: "0"}, false, true},
		{"user turned compositing on, setting off", wl, map[string]string{comp: "0"}, amd, &off, map[string]string{comp: "0"}, false, true},
		{"user turned compositing on, nvidia draws", wl, map[string]string{comp: "0"}, nvidia, nil, map[string]string{comp: "0", dma: "1"}, true, true},
		{"user turned it on again without a gpu", x11, map[string]string{comp: "0"}, none, nil, map[string]string{comp: "0"}, false, true},
		{"user chose dma-buf off", wl, map[string]string{dma: "1"}, amd, nil, map[string]string{dma: "1"}, false, true},
		{"user chose dma-buf off without a gpu", x11, map[string]string{dma: "1"}, none, nil, map[string]string{dma: "1"}, false, true},
		{"user chose dma-buf on with nvidia (#5176's command)", wl, map[string]string{dma: "0", comp: "0"}, nvidia, nil, map[string]string{dma: "0", comp: "0"}, true, true},
		// WebKit reads any value but 0 as set, "" too
		{"user set it empty", wl, map[string]string{comp: ""}, amd, nil, map[string]string{comp: ""}, false, false},
		// a restart passes on what magpie set: still magpie's, so the
		// setting changed since applies
		{"restarted, setting turned on", wl, map[string]string{comp: "1", webkitMarker: comp + "=1"}, amd, &on, map[string]string{}, false, true},
		{"restarted after wails' value, hybrid", wl, map[string]string{dma: "1", webkitMarker: dma + "=1"}, hybrid, nil, map[string]string{}, true, true},
		{"restarted, the user's own value since", wl, map[string]string{comp: "0", webkitMarker: comp + "=1"}, amd, nil, map[string]string{comp: "0"}, false, true},
	} {
		start := map[string]string{}
		for k, v := range c.start {
			start[k] = v
		}
		env := map[string]string{}
		for k, v := range start {
			env[k] = v
		}
		for k, v := range c.session {
			env[k] = v
		}
		// what Wails beta.24's init has set by now: DMA-BUF off wherever
		// the nvidia module is loaded, not the user's
		if _, set := start[dma]; c.hw.nvidiaLoaded && !set {
			env[dma] = "1"
		}
		got := webkitDecide(start, func(k string) string { return env[k] }, c.hw, c.pref)
		if !maps.Equal(got.env, c.want) {
			t.Errorf("%s: env %v, want %v", c.name, got.env, c.want)
		}
		if got.info.Auto != c.auto || got.info.GPU != c.gpu {
			t.Errorf("%s: auto %v gpu %v, want %v %v", c.name, got.info.Auto, got.info.GPU, c.auto, c.gpu)
		}
		// the marker names what magpie set, and nothing of the user's
		for _, kv := range strings.Split(got.marker, ";") {
			k, v, _ := strings.Cut(kv, "=")
			if kv == "" {
				continue
			}
			if _, user := c.start[k]; user && !strings.Contains(c.start[webkitMarker], k) || got.env[k] != v {
				t.Errorf("%s: marker %q names %s", c.name, got.marker, kv)
			}
		}
	}
}

// The environment at exec, as /proc/self/environ has it: NUL-separated,
// a value with "=" in it whole, and an unreadable file unknown.
func TestStartEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "environ")
	os.WriteFile(p, []byte("HOME=/home/a\x00WEBKIT_DISABLE_COMPOSITING_MODE=0\x00X=a=b\x00"), 0o600)
	env, ok := startEnv(p)
	if !ok || env["WEBKIT_DISABLE_COMPOSITING_MODE"] != "0" || env["X"] != "a=b" || len(env) != 3 {
		t.Fatalf("startEnv = %v %v", env, ok)
	}
	if _, ok := startEnv(filepath.Join(t.TempDir(), "none")); ok {
		t.Fatal("an unreadable environ read as known")
	}
}

// Wails beta.31's rule: a hybrid laptop's screen
// on the iGPU is not NVIDIA's, an X11 session always is, and offload or
// an EGL vendor list settles it first.
func TestNvidiaRenders(t *testing.T) {
	type card struct {
		name, driver string
		bootVGA      bool
		connected    bool
	}
	tree := func(t *testing.T, cards ...card) string {
		root := t.TempDir()
		for _, c := range cards {
			dev := filepath.Join(root, "devices", c.name)
			drv := filepath.Join(root, "drivers", c.driver)
			os.MkdirAll(dev, 0o755)
			os.MkdirAll(drv, 0o755)
			card := filepath.Join(root, "class", "drm", c.name)
			os.MkdirAll(card, 0o755)
			os.Symlink(dev, filepath.Join(card, "device"))
			os.Symlink(drv, filepath.Join(dev, "driver"))
			vga := "0"
			if c.bootVGA {
				vga = "1"
			}
			os.WriteFile(filepath.Join(dev, "boot_vga"), []byte(vga+"\n"), 0o644)
			status := "disconnected"
			if c.connected {
				status = "connected"
			}
			conn := filepath.Join(root, "class", "drm", c.name+"-eDP-1")
			os.MkdirAll(conn, 0o755)
			os.WriteFile(filepath.Join(conn, "status"), []byte(status+"\n"), 0o644)
		}
		return root
	}
	wl := map[string]string{"XDG_SESSION_TYPE": "wayland"}
	with := func(m map[string]string, k, v string) map[string]string {
		n := maps.Clone(m)
		n[k] = v
		return n
	}
	igpu := card{"card1", "i915", true, true}
	dgpu := card{"card0", "nvidia", false, false}
	for _, c := range []struct {
		name  string
		cards []card
		env   map[string]string
		want  bool
	}{
		{"hybrid, wayland, screen on the iGPU (#6184)", []card{igpu, dgpu}, wl, false},
		{"hybrid, wayland, offloaded", []card{igpu, dgpu}, with(wl, "__NV_PRIME_RENDER_OFFLOAD", "1"), true},
		{"hybrid, wayland, mesa's egl alone", []card{igpu, dgpu}, with(wl, "__EGL_VENDOR_LIBRARY_FILENAMES", "/usr/share/glvnd/egl_vendor.d/50_mesa.json"), false},
		{"hybrid, wayland, nvidia's egl alone", []card{igpu, dgpu}, with(wl, "__EGL_VENDOR_LIBRARY_FILENAMES", "/usr/share/glvnd/egl_vendor.d/10_nvidia.json"), true},
		{"hybrid, x11", []card{igpu, dgpu}, map[string]string{"XDG_SESSION_TYPE": "x11"}, true},
		{"nvidia drives the screen, wayland", []card{{"card0", "nvidia", true, true}, {"card1", "amdgpu", false, false}}, wl, true},
		{"boot vga with no screen: the screen's card decides", []card{{"card0", "nvidia", true, false}, {"card1", "amdgpu", false, true}}, wl, false},
		{"nvidia alone", []card{{"card0", "nvidia", true, true}}, wl, true},
	} {
		root := tree(t, c.cards...)
		if got := nvidiaRenders(root, func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%s: nvidiaRenders = %v, want %v", c.name, got, c.want)
		}
	}
}

// A GPU is reachable when one render node opens: none there, or each one
// refused (the xrdp user outside the render group), is software drawing.
func TestRenderNodeOpens(t *testing.T) {
	denied := errors.New("permission denied")
	for _, c := range []struct {
		name  string
		nodes []string
		ok    map[string]bool
		want  bool
	}{
		{"no render node", nil, nil, false},
		{"every node refused", []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}, nil, false},
		{"the second node opens", []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}, map[string]bool{"/dev/dri/renderD129": true}, true},
	} {
		got := renderNodeOpens(c.nodes, func(p string) error {
			if c.ok[p] {
				return nil
			}
			return denied
		})
		if got != c.want {
			t.Errorf("%s: renderNodeOpens = %v, want %v", c.name, got, c.want)
		}
	}
}

// WebKitGTK's web process aborts on any video where autoaudiosink is
// missing (stock Omarchy: gst-plugins-base and -bad, no -good), so video is
// off only there; with GStreamer's folder not found, nothing is known and
// video stays on.
func TestGstAutodetect(t *testing.T) {
	for _, c := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"plugins-good installed", []string{"/usr/lib/gstreamer-1.0/libgstcoreelements.so", "/usr/lib/gstreamer-1.0/libgstautodetect.so"}, true},
		{"stock omarchy", []string{"/usr/lib/gstreamer-1.0/libgstcoreelements.so", "/usr/lib/gstreamer-1.0/libgsttheora.so"}, false},
		{"good in a folder the variable names", []string{"/usr/lib/gstreamer-1.0/libgstcoreelements.so", "/opt/gst/libgstautodetect.so"}, true},
		{"no gstreamer folder found", nil, true},
	} {
		have := map[string]bool{}
		for _, f := range c.files {
			have[f] = true
		}
		if got := gstAutodetect([]string{"/opt/gst", "/usr/lib/gstreamer-1.0"}, func(p string) bool { return have[p] }); got != c.want {
			t.Errorf("%s: gstAutodetect = %v, want %v", c.name, got, c.want)
		}
	}
	env := map[string]string{"GST_PLUGIN_PATH_1_0": "/opt/gst:/opt/more", "GST_PLUGIN_SYSTEM_PATH": "/sys/gst"}
	dirs := gstPluginDirs(func(k string) string { return env[k] })
	for _, d := range []string{"/opt/gst", "/opt/more", "/sys/gst"} {
		if !slices.Contains(dirs, d) {
			t.Errorf("gstPluginDirs = %v, missing %s", dirs, d)
		}
	}
}
