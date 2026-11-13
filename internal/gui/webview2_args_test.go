//go:build !nogui

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/webview2env"
)

// wailsBrowserArgs is the command line Wails v3.0.0-beta.24 makes from the
// app's Windows options (setupChromium, webview_window_windows.go): its own
// msSmartScreenProtection first among the disabled features, each list
// once as one switch, then the other arguments as given.
func wailsBrowserArgs(o application.WindowsOptions) []string {
	var disabled []string
	for _, f := range append([]string{"msSmartScreenProtection"}, o.DisabledFeatures...) {
		if !slices.Contains(disabled, f) {
			disabled = append(disabled, f)
		}
	}
	args := []string{"--disable-features=" + strings.Join(disabled, ",")}
	if len(o.EnabledFeatures) > 0 {
		args = append(args, "--enable-features="+strings.Join(o.EnabledFeatures, ","))
	}
	return append(args, o.AdditionalBrowserArgs...)
}

// #2753 (LuYquan): magpie's own --disable-features made WebView2 skip the
// user's WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS, so RendererCodeIntegrity
// stayed on and an injected DLL got the renderer killed. Now the user's
// arguments are merged into magpie's: one --disable-features holding both
// lists (Chromium keeps only the last of two), one --enable-features, and
// every other argument as written.
func TestWebView2ArgsMergeTheUsers(t *testing.T) {
	for _, c := range []struct {
		name, env string
		want      []string
	}{
		{"none set", "", []string{"--disable-features=msSmartScreenProtection,IsolateSandboxedIframes,CalculateNativeWinOcclusion"}},
		{"the reporter's", "--disable-features=RendererCodeIntegrity",
			[]string{"--disable-features=msSmartScreenProtection,IsolateSandboxedIframes,RendererCodeIntegrity,CalculateNativeWinOcclusion"}},
		{"twice, with magpie's own and a repeat", `--disable-features=A,msSmartScreenProtection --disable-features="B, A"`,
			[]string{"--disable-features=msSmartScreenProtection,IsolateSandboxedIframes,A,B,CalculateNativeWinOcclusion"}},
		{"enable and others kept", "  --enable-features=X,Y\t--remote-debugging-port=9333 --proxy-server=\"http://h:1 x\" --enable-features=Y,Z --lang=de ",
			[]string{"--disable-features=msSmartScreenProtection,IsolateSandboxedIframes,CalculateNativeWinOcclusion", "--enable-features=X,Y,Z", "--remote-debugging-port=9333", `--proxy-server="http://h:1 x"`, "--lang=de"}},
		{"a bare switch", "--disable-features", []string{"--disable-features=msSmartScreenProtection,IsolateSandboxedIframes,CalculateNativeWinOcclusion"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			was := webview2env.Args
			t.Cleanup(func() { webview2env.Args = was })
			webview2env.Args = c.env
			got := wailsBrowserArgs(windowsOptions())
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("WebView2 is started with %q, want %q", got, c.want)
			}
			n := 0
			for _, a := range got {
				if strings.HasPrefix(a, "--disable-features") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d --disable-features in %q, Chromium keeps only the last", n, got)
			}
		})
	}
}

// Mute06162 on X (0.1.4066, Windows): every plugin page said "This page
// stopped working". WebView2 157 runs a sandboxed frame in a process of
// its own, and Wails' WebResourceRequested isn't raised for that
// process's requests, so the page's bridge.js and scripts were refused
// (ERR_CONNECTION_REFUSED) and it never said hello. magpie's WebView2
// starts with the frame isolation off, beside the user's own features
// and once only; a user who turns it on keeps it.
func TestWebView2KeepsPluginFramesInTheWindowProcess(t *testing.T) {
	was := webview2env.Args
	t.Cleanup(func() { webview2env.Args = was })
	for _, c := range []struct {
		name, env, keep string // keep: the user's own, still in the arguments
		off, enabled    bool
	}{
		{"default", "", "", true, false},
		{"the user's own features", "--disable-features=RendererCodeIntegrity --lang=de", "RendererCodeIntegrity", true, false},
		{"the user disables it too", "--disable-features=IsolateSandboxedIframes,A", ",A", true, false},
		{"the user turns it on", "--enable-features=IsolateSandboxedIframes", "", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			webview2env.Args = c.env
			args := wailsBrowserArgs(windowsOptions())
			var off, on int
			for _, a := range args {
				k, v, _ := strings.Cut(a, "=")
				for _, f := range strings.Split(v, ",") {
					if f != "IsolateSandboxedIframes" {
						continue
					}
					switch k {
					case "--disable-features":
						off++
					case "--enable-features":
						on++
					}
				}
			}
			if c.off && off != 1 {
				t.Fatalf("IsolateSandboxedIframes disabled %d times in %q, want once: a plugin's sandboxed frame gets a process whose requests Wails doesn't serve", off, args)
			}
			if !c.off && off != 0 {
				t.Fatalf("the user's IsolateSandboxedIframes is disabled: %q", args)
			}
			if c.enabled != (on == 1) {
				t.Fatalf("enabled %d times in %q", on, args)
			}
			if !strings.Contains(strings.Join(args, " "), c.keep) {
				t.Fatalf("the user's %s is lost: %q", c.keep, args)
			}
		})
	}
}

// aquaarboraurum on X: a window shown while the screen is locked can stay
// white once Chromium's occlusion tracking has taken it as covered.
// magpie's WebView2 starts with the tracking off, once only; a user who
// turns it on keeps it.
func TestWebView2OcclusionTrackingOff(t *testing.T) {
	was := webview2env.Args
	t.Cleanup(func() { webview2env.Args = was })
	for _, c := range []struct {
		env     string
		off, on int
	}{
		{"", 1, 0},
		{"--disable-features=CalculateNativeWinOcclusion,A", 1, 0},
		{"--enable-features=CalculateNativeWinOcclusion", 0, 1},
	} {
		webview2env.Args = c.env
		args := wailsBrowserArgs(windowsOptions())
		var off, on int
		for _, a := range args {
			k, v, _ := strings.Cut(a, "=")
			for _, f := range strings.Split(v, ",") {
				if f == "CalculateNativeWinOcclusion" && k == "--disable-features" {
					off++
				}
				if f == "CalculateNativeWinOcclusion" && k == "--enable-features" {
					on++
				}
			}
		}
		if off != c.off || on != c.on {
			t.Errorf("%q: WebView2 starts with %q: occlusion tracking disabled %d times, enabled %d, want %d and %d", c.env, args, off, on, c.off, c.on)
		}
	}
}

// Nothing in the defaults turns renderer code integrity off: only a user
// who asks for it gets it (#2753).
func TestWebView2KeepsCodeIntegrityByDefault(t *testing.T) {
	was := webview2env.Args
	t.Cleanup(func() { webview2env.Args = was })
	webview2env.Args = ""
	if got := strings.Join(wailsBrowserArgs(windowsOptions()), " "); strings.Contains(got, "RendererCodeIntegrity") {
		t.Fatalf("default arguments %q turn RendererCodeIntegrity off", got)
	}
}

// On Windows Wails' WebView2 loader blanks the variable in its init, which
// runs before magpie's code (#2753: the reporter's flag never arrived).
// The value magpie was started with still reaches the options: this test
// starts its own binary with the variable set, where the loader's init has
// run as in the app.
func TestWebView2ArgsSurviveWailsInit(t *testing.T) {
	if os.Getenv("MAGPIE_TEST_WEBVIEW2_CHILD") == "1" {
		fmt.Printf("os=%q disabled=%q\n", os.Getenv(webview2env.Name), windowsOptions().DisabledFeatures)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWebView2ArgsSurviveWailsInit$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MAGPIE_TEST_WEBVIEW2_CHILD=1", webview2env.Name+"=--disable-features=RendererCodeIntegrity")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `disabled=["IsolateSandboxedIframes" "RendererCodeIntegrity" "CalculateNativeWinOcclusion"]`) {
		t.Fatalf("the started value didn't reach the options:\n%s", out)
	}
	t.Logf("%s", strings.SplitN(string(out), "\n", 2)[0])
}
