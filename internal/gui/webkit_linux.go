package gui

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// How WebKitGTK draws the pages, set in the environment before the app
// makes its first webview: WEBKIT_DISABLE_COMPOSITING_MODE=1 draws them
// on the CPU (no accelerated compositing), and
// WEBKIT_DISABLE_DMABUF_RENDERER=1 keeps the GPU but copies each frame
// through shared memory instead of handing the buffer over (DMA-BUF).
//
//   - No GPU this process can open (xrdp, VNC, Xvfb, a user outside the
//     render group): compositing falls back to Mesa's llvmpipe, which
//     rasterises and blends every layer on the CPU with a thread per core.
//     The Routing page took 459%–830% CPU there, and 23% with compositing
//     off, looking the same (#2827, zhang-4411). Compositing off.
//   - Wayland, no NVIDIA driver loaded: accelerated compositing flickered
//     the window on a click or a hover (myxxts on Discord: Fedora 44, KDE,
//     AMD Radeon 780M), and with it off the pages drew steadily. Off.
//   - NVIDIA's proprietary driver draws the screen: its GBM refuses the
//     buffers WebKit asks for (gbm_bo_create_with_modifiers2 with
//     GBM_BO_USE_RENDERING fails EINVAL, #6184's X11 + RTX 4060), so
//     DMA-BUF gives blank windows and Wayland's "Error 71". Debian,
//     openSUSE and Gentoo patch WebKitGTK to turn it off for NVIDIA; Arch
//     and upstream (bug 262607, WONTFIX) don't. DMA-BUF off, compositing
//     kept: it still draws on the GPU.
//   - A hybrid laptop whose screen an Intel or AMD GPU drives, with the
//     nvidia module loaded for the other (#5176, #6184, ZDegress on X):
//     nothing. Wails beta.24 turned DMA-BUF off whenever the module was
//     loaded (its init, which runs before this), and the copy through
//     shared memory made every tab switch lag (perf: 21% of the UI
//     process in pixman; the longest stall 950ms against 317ms with
//     DMA-BUF on). Compositing stayed on there all along, as Wails'
//     variable stopped magpie's Wayland rule, and those reports found
//     compositing with DMA-BUF the smoothest.
//   - X11 with a GPU and no NVIDIA: nothing, as before.
//
// No WebKitGTK version is told apart: the NVIDIA failure is in its GBM,
// still there with 2.50, and the Wayland flicker was on 2.52.
//
// Settings › General › Advanced › GPU rendering (settings.GPURendering)
// overrides the choice at the next start: on, nothing is turned off but
// DMA-BUF where NVIDIA draws (a blank window there could not be turned
// back from the window); off, compositing off. A variable the user set
// themselves, to 0 too, is never changed, and takes the choice over from
// magpie and the setting: magpie then only turns DMA-BUF off where
// NVIDIA draws, if the user left that one unset.

// webkitVars are the two variables, in the order they're decided.
var webkitVars = [...]string{"WEBKIT_DISABLE_COMPOSITING_MODE", "WEBKIT_DISABLE_DMABUF_RENDERER"}

// webkitMarker names what magpie set of webkitVars, NAME=VALUE joined by
// ";", for a magpie it starts again (a restart or an update passes on
// os.Environ()) to tell its own values from the user's.
const webkitMarker = "MAGPIE_WEBKIT_SET"

// webkitHW is what decides the default.
type webkitHW struct {
	gpu           bool // a render node this process can open (gpuReachable)
	nvidiaLoaded  bool // /sys/module/nvidia
	nvidiaRenders bool // NVIDIA's driver draws what the pages are drawn on
}

func webkitDefaults() {
	own, ok := startEnv("/proc/self/environ")
	if !ok {
		// unknown which values are the user's: leave them all, as before
		own = map[string]string{}
		for _, k := range webkitVars {
			if v, set := os.LookupEnv(k); set {
				own[k] = v
			}
		}
		own[webkitMarker] = ""
	}
	hw := webkitHW{gpu: gpuReachable(), nvidiaLoaded: exists("/sys/module/nvidia")}
	hw.nvidiaRenders = hw.nvidiaLoaded && nvidiaRenders("/sys", os.Getenv)
	run := webkitDecide(own, os.Getenv, hw, settings.Load().GPURendering)
	for _, k := range webkitVars {
		if v, set := run.env[k]; !set {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
	if run.marker == "" {
		os.Unsetenv(webkitMarker)
	} else {
		os.Setenv(webkitMarker, run.marker)
	}
	webkitRun = &run.info
	log.Printf("webkit: gpu %v (auto %v, nvidia loaded %v, renders %v); %s", run.info.GPU, run.info.Auto, hw.nvidiaLoaded, hw.nvidiaRenders, strings.Join(run.set, " "))
}

type webkitChoice struct {
	env    map[string]string // the variables to have, absent ones unset
	marker string            // webkitMarker's value
	set    []string          // the variables as they are, for the log
	info   webkitRunInfo
}

// webkitDecide is the choice, from the environment magpie was started
// with (start, by /proc/self/environ: Wails' init has set
// WEBKIT_DISABLE_DMABUF_RENDERER in os.Getenv's since), the session,
// the hardware and the setting (nil: magpie's own pick).
func webkitDecide(start map[string]string, getenv func(string) string, hw webkitHW, pref *bool) webkitChoice {
	// what a magpie that started this one set is not the user's
	mine := map[string]string{}
	for _, kv := range strings.Split(start[webkitMarker], ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			mine[k] = v
		}
	}
	user := map[string]string{}
	for _, k := range webkitVars {
		if v, set := start[k]; set {
			if was, ok := mine[k]; !ok || was != v {
				user[k] = v
			}
		}
	}
	wayland := getenv("XDG_SESSION_TYPE") == "wayland" || getenv("WAYLAND_DISPLAY") != ""
	auto := hw.gpu && (!wayland || hw.nvidiaLoaded)
	gpu := auto
	if pref != nil {
		gpu = *pref
	}
	// a variable of the user's takes the choice over, the setting's too;
	// only NVIDIA's blank window is still kept away
	want := map[string]string{}
	if !gpu && len(user) == 0 {
		want["WEBKIT_DISABLE_COMPOSITING_MODE"] = "1"
	} else if hw.nvidiaRenders {
		want["WEBKIT_DISABLE_DMABUF_RENDERER"] = "1"
	}
	c := webkitChoice{env: map[string]string{}, info: webkitRunInfo{Auto: auto}}
	var marks []string
	for _, k := range webkitVars {
		if v, ok := user[k]; ok {
			c.env[k] = v
			c.info.User = append(c.info.User, k+"="+v)
		} else if v, ok := want[k]; ok {
			c.env[k] = v
			marks = append(marks, k+"="+v)
		}
		if v, ok := c.env[k]; ok {
			c.set = append(c.set, k+"="+v)
		}
	}
	c.marker = strings.Join(marks, ";")
	// compositing is off only by its variable set to other than 0
	v, ok := c.env["WEBKIT_DISABLE_COMPOSITING_MODE"]
	c.info.GPU = !ok || v == "0"
	return c
}

// startEnv reads the environment this process was started with: the
// block at exec, which setenv never writes over. False when it can't be
// read.
func startEnv(path string) (map[string]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(b), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env, true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// nvidiaRenders is Wails' own from v3.0.0-beta.31 (gpu_linux.go,
// wailsapp/wails#6273, MIT), which magpie's beta.24 hasn't: whether
// NVIDIA's driver draws what WebKit draws on, given its module is
// loaded. Offloaded to it (prime-run, switcheroo), or glvnd offered NVIDIA's
// EGL alone, it does. Under Wayland WebKit draws on the compositor's GPU,
// the boot VGA device when that drives a connected screen, else the one
// driving the screens. Under X11 reverse PRIME can't be read from here,
// so it does whenever the module is loaded (#6184: Ubuntu 22.04, X11, an
// RTX 4060 driving the screen, GBM refusing WebKit's buffers).
func nvidiaRenders(root string, getenv func(string) string) bool {
	switch eglVendors(getenv("__EGL_VENDOR_LIBRARY_FILENAMES")) {
	case vendorsNone:
		return false
	case vendorsOnlyNVIDIA:
		return true
	}
	if getenv("__NV_PRIME_RENDER_OFFLOAD") == "1" {
		return true
	}
	if getenv("XDG_SESSION_TYPE") != "wayland" && getenv("WAYLAND_DISPLAY") == "" {
		return true
	}
	drm := filepath.Join(root, "class", "drm")
	driverOf := func(card string) string {
		driver, err := filepath.EvalSymlinks(filepath.Join(drm, card, "device", "driver"))
		if err != nil {
			return ""
		}
		return filepath.Base(driver)
	}
	screens := map[string]bool{}
	statuses, _ := filepath.Glob(filepath.Join(drm, "card*-*", "status"))
	for _, status := range statuses {
		if b, err := os.ReadFile(status); err == nil && strings.TrimSpace(string(b)) == "connected" {
			card, _, _ := strings.Cut(filepath.Base(filepath.Dir(status)), "-")
			screens[card] = true
		}
	}
	for card := range screens {
		if b, err := os.ReadFile(filepath.Join(drm, card, "device", "boot_vga")); err == nil && strings.TrimSpace(string(b)) == "1" {
			return driverOf(card) == "nvidia"
		}
	}
	for card := range screens {
		if driver := driverOf(card); driver != "" && driver != "nvidia" {
			return false
		}
	}
	return true
}

type vendorList int

const (
	vendorsUnset vendorList = iota
	vendorsNone
	vendorsMixed
	vendorsOnlyNVIDIA
)

// eglVendors reads a glvnd EGL vendor list: unset, naming no NVIDIA
// vendor file, mixed, or naming only NVIDIA's.
func eglVendors(files string) vendorList {
	if files == "" {
		return vendorsUnset
	}
	nvidia, other := false, false
	for _, f := range strings.Split(files, ":") {
		if strings.Contains(filepath.Base(f), "nvidia") {
			nvidia = true
		} else {
			other = true
		}
	}
	switch {
	case !nvidia:
		return vendorsNone
	case other:
		return vendorsMixed
	}
	return vendorsOnlyNVIDIA
}

// gpuReachable reports whether this process can open a DRM render node,
// which is what EGL needs to draw on a GPU: none there, or none this user
// may open (libEGL's "failed to open /dev/dri/renderD128: Permission
// denied"), and WebKit's GL is software.
func gpuReachable() bool {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	return renderNodeOpens(nodes, func(p string) error {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err == nil {
			f.Close()
		}
		return err
	})
}

func renderNodeOpens(nodes []string, open func(string) error) bool {
	for _, n := range nodes {
		if open(n) == nil {
			return true
		}
	}
	return false
}

// webkitPlaysVideo is whether WebKitGTK can play a <video> here. It gives
// every video, a muted one too, an audio sink it makes with autoaudiosink,
// from gst-plugins-good; where that isn't installed (stock Omarchy has only
// -base and -bad) it gets none, and its web process aborts a moment after
// the video starts loading, which leaves the window blank. GStreamer keeps
// the element in libgstautodetect.so. A system with no plugin folder magpie
// knows of says nothing either way, and plays.
var webkitPlaysVideo = sync.OnceValue(func() bool {
	return gstAutodetect(gstPluginDirs(os.Getenv), func(p string) bool { _, err := os.Stat(p); return err == nil })
})

// gstPluginDirs is where GStreamer looks for plugins: the folders its
// variables name, and the system's own.
func gstPluginDirs(getenv func(string) string) []string {
	var dirs []string
	for _, k := range []string{"GST_PLUGIN_SYSTEM_PATH_1_0", "GST_PLUGIN_SYSTEM_PATH", "GST_PLUGIN_PATH_1_0", "GST_PLUGIN_PATH"} {
		for _, d := range strings.Split(getenv(k), ":") {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	for _, g := range []string{"/usr/lib*/gstreamer-1.0", "/usr/lib*/*/gstreamer-1.0", "/usr/local/lib*/gstreamer-1.0", "/usr/local/lib*/*/gstreamer-1.0"} {
		m, _ := filepath.Glob(g)
		dirs = append(dirs, m...)
	}
	return dirs
}

// gstAutodetect is false only when GStreamer's core plugins are there and
// autoaudiosink is in none of the folders.
func gstAutodetect(dirs []string, exists func(string) bool) bool {
	found := false
	for _, d := range dirs {
		if exists(filepath.Join(d, "libgstautodetect.so")) {
			return true
		}
		found = found || exists(filepath.Join(d, "libgstcoreelements.so"))
	}
	return !found
}
