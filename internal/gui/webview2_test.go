package gui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// #1806 (ufec): Wails' "WebView2" is "" when its lookup fails, the version
// otherwise; a key it doesn't set says nothing and starts the app.
func TestWebView2Missing(t *testing.T) {
	for _, c := range []struct {
		name string
		info map[string]any
		want bool
	}{
		{"failed lookup", map[string]any{"WebView2": ""}, true},
		{"a version", map[string]any{"WebView2": "141.0.3537.71"}, false},
		{"not reported", map[string]any{}, false},
		{"no info", nil, false},
	} {
		if got := webView2Missing(c.info); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The box names the runtime, gives Microsoft's page and asks to open it
// (Yes opens it), in each language the app speaks, English otherwise.
func TestWebView2Notice(t *testing.T) {
	for _, lang := range []string{"en", "zh", "zh-TW", "ja", "de", "pt"} {
		title, body := webView2Notice(lang)
		if !strings.Contains(title, "WebView2") || !strings.Contains(body, "WebView2") {
			t.Errorf("%s: doesn't name WebView2: %q / %q", lang, title, body)
		}
		if !strings.Contains(body, "https://developer.microsoft.com/microsoft-edge/webview2/") {
			t.Errorf("%s: no download page in %q", lang, body)
		}
		if !strings.Contains(body, "winget install Microsoft.EdgeWebView2Runtime") {
			t.Errorf("%s: no winget command in %q", lang, body)
		}
		if !strings.Contains(body, "magpie web") || !strings.Contains(body, "magpie tui") {
			t.Errorf("%s: doesn't say what works without it: %q", lang, body)
		}
		if strings.Contains(body, "%!") {
			t.Errorf("%s: bad format: %q", lang, body)
		}
		if !strings.HasSuffix(body, "?") && !strings.HasSuffix(body, "？") {
			t.Errorf("%s: doesn't end asking whether to open it: %q", lang, body)
		}
	}
	if _, body := webView2Notice("zh"); !strings.Contains(body, "运行时") {
		t.Errorf("zh isn't Chinese: %q", body)
	}
	if title, _ := webView2Notice("pt"); title != "magpie needs WebView2" {
		t.Errorf("pt: %q, want English", title)
	}
}

func TestWebView2Folder(t *testing.T) {
	if got := webView2Folder(`D:\p\webview2`, `C:\A`, `C:\x\magpie.exe`); got != `D:\p\webview2` {
		t.Fatal(got)
	}
	if got := webView2Folder("", "", "magpie.exe"); got != "" {
		t.Fatal(got)
	}
}

func TestWebView2Failure(t *testing.T) {
	for err, want := range map[error]bool{
		errors.New("timed out after 1m0s waiting for the WebView2 controller; x"): true,
		errors.New("error calling Webview2Loader: boom"):                          true,
		errors.New("something else"):                                              false,
		nil:                                                                       false,
	} {
		if got := webView2Failure(err); got != want {
			t.Errorf("%v: %t", err, got)
		}
	}
}

func TestWebView2Try(t *testing.T) {
	if webView2Try("") != 0 || webView2Try("2") != 2 || webView2Try("-1") != 0 || webView2Try("x") != 0 {
		t.Fatal("try")
	}
}

// the start waits for the folder to be let go, and gives up after max
func TestAwaitWebView2Folder(t *testing.T) {
	n := 0
	var slept time.Duration
	free := func() bool { n++; return n > 3 }
	sleep := func(d time.Duration) { slept += d }
	if !awaitWebView2Folder(free, time.Second, 100*time.Millisecond, sleep) || slept != 300*time.Millisecond {
		t.Fatal(n, slept)
	}
	slept = 0
	if awaitWebView2Folder(func() bool { return false }, time.Second, 100*time.Millisecond, sleep) || slept != time.Second {
		t.Fatal(slept)
	}
}
