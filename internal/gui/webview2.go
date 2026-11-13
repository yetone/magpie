package gui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// magpie's windows are WebView2 on Windows, a runtime Windows 10 doesn't
// always have. Without it Wails hands the failure to ErrorHandler and exits,
// and the desktop build has no console for that to land in: a double-click
// came up to nothing (#1806, ufec). Run asks before any window is made and,
// missing, says so in a box of Windows' own.

// webView2Page is Microsoft's download page for the runtime.
const webView2Page = "https://developer.microsoft.com/microsoft-edge/webview2/"

// webView2Missing reads Wails' own answer, Env.Info().PlatformInfo's
// "WebView2": the runtime's version, "" when its lookup failed, the lookup
// a window's WebView2 environment makes. A key Wails doesn't set at all
// says nothing, and is read as there: refusing to start a magpie that would
// have worked is worse than the silent exit.
func webView2Missing(info map[string]any) bool {
	v, ok := info["WebView2"]
	return ok && fmt.Sprint(v) == ""
}

// webView2Notice is the box's title and text in lang (trayLang's), English
// where it has none. It names the page and winget's package (#1806: ufec
// asked for the winget way too), and asks whether to open the page; Yes
// opens it.
func webView2Notice(lang string) (title, body string) {
	t, ok := webView2Words[lang]
	if !ok {
		t = webView2Words["en"]
	}
	return t[0], fmt.Sprintf(t[1], webView2Page)
}

var webView2Words = map[string][2]string{
	"en": {"magpie needs WebView2",
		"magpie's windows need Microsoft Edge WebView2 Runtime, which isn't installed on this computer.\r\n\r\n" +
			"Install it from:\r\n%s\r\n\r\nor in a terminal:\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"Until then, \"magpie web\" opens magpie in your browser and \"magpie tui\" in a terminal.\r\n\r\n" +
			"Open the download page now?"},
	"zh": {"magpie 需要 WebView2",
		"magpie 的窗口需要 Microsoft Edge WebView2 运行时，这台电脑上没有安装。\r\n\r\n" +
			"请从这里安装：\r\n%s\r\n\r\n或在终端里运行：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"装好之前，可以用 \"magpie web\" 在浏览器里打开 magpie，或用 \"magpie tui\" 在终端里打开。\r\n\r\n" +
			"现在打开下载页面吗？"},
	"zh-TW": {"magpie 需要 WebView2",
		"magpie 的視窗需要 Microsoft Edge WebView2 執行階段，這台電腦上沒有安裝。\r\n\r\n" +
			"請從這裡安裝：\r\n%s\r\n\r\n或在終端機裡執行：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"裝好之前，可以用 \"magpie web\" 在瀏覽器裡開啟 magpie，或用 \"magpie tui\" 在終端機裡開啟。\r\n\r\n" +
			"現在開啟下載頁面嗎？"},
	"ja": {"magpie には WebView2 が必要です",
		"magpie のウィンドウには Microsoft Edge WebView2 ランタイムが必要ですが、このコンピューターにはインストールされていません。\r\n\r\n" +
			"こちらからインストールしてください：\r\n%s\r\n\r\nまたはターミナルで：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"それまでは \"magpie web\" でブラウザーから、\"magpie tui\" でターミナルから magpie を使えます。\r\n\r\n" +
			"今すぐダウンロードページを開きますか？"},
	"de": {"magpie braucht WebView2",
		"Die Fenster von magpie brauchen die Microsoft Edge WebView2 Runtime, die auf diesem Computer nicht installiert ist.\r\n\r\n" +
			"Installieren Sie sie von:\r\n%s\r\n\r\noder im Terminal:\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"Bis dahin öffnet \"magpie web\" magpie im Browser und \"magpie tui\" im Terminal.\r\n\r\n" +
			"Die Download-Seite jetzt öffnen?"},
}

// webViewOcclusion is Chromium's tracking of whether a window is covered,
// turned off for magpie's windows (windowsOptions): when it is wrong, as
// for a window shown while the screen is locked or the display off, the
// page stays white (screenback_windows.go). A hidden or minimised window is
// told so by Wails (put_IsVisible) without it.
const webViewOcclusion = "CalculateNativeWinOcclusion"

// webView2Args sorts what the user put in WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS
// (webview2env.Args) into Wails' three options, which it passes as the
// app's own arguments: the features to disable, the ones to enable, and
// every other argument as written. Wails' loader blanks the variable
// itself, so without this a user's --disable-features=RendererCodeIntegrity
// never reached magpie's renderer (#2753, LuYquan: an injected DLL got it
// killed and the window went black). Wails puts its own
// msSmartScreenProtection first in DisabledFeatures and writes the list as
// one --disable-features (and EnabledFeatures as one --enable-features):
// Chromium keeps only the last of a repeated switch, so two would drop one
// of them. A list given more than once is merged, each feature once, in
// the order given. Arguments are split as Windows splits a command line on
// spaces, a double-quoted run kept whole.
func webView2Args(env string) (disabled, enabled, other []string) {
	for _, a := range splitArgs(env) {
		switch k, v, _ := strings.Cut(a, "="); k {
		case "--disable-features":
			disabled = addFeatures(disabled, v)
		case "--enable-features":
			enabled = addFeatures(enabled, v)
		default:
			other = append(other, a)
		}
	}
	return disabled, enabled, other
}

// isolateSandboxedFrames is the Chromium feature that runs a sandboxed
// frame in a process of its own. WebView2 157 has it on, and Wails
// (v3.0.0-beta.24) registers its WebResourceRequested filter with
// AddWebResourceRequestedFilter, which isn't raised for the requests
// such a frame makes: a plugin's page or panel (pluginpages.js, sandbox
// allow-scripts) got its index.html, but its bridge.js, scripts and
// stylesheet went out to the network as http://wails.localhost/... and
// failed with ERR_CONNECTION_REFUSED, so the page never said hello and
// showed "This page stopped working" (Mute06162 on X, 0.1.4066). Wails'
// request cancellation took the frame for a worker too, and logged
// "Worker request cancellation setup: The parameter is incorrect."
const isolateSandboxedFrames = "IsolateSandboxedIframes"

// sharedFrameProcess is disabled with isolateSandboxedFrames first, so a
// sandboxed frame stays in its window's process, where Wails serves its
// requests. The frame keeps its sandbox: an opaque origin, its own policy
// (pagePolicy), no way to the window but messages. A user who enables
// the feature in WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS keeps it on.
func sharedFrameProcess(disabled, enabled []string) []string {
	if slices.Contains(enabled, isolateSandboxedFrames) {
		return disabled
	}
	return addFeatures([]string{isolateSandboxedFrames}, strings.Join(disabled, ","))
}

// occlusionOff adds webViewOcclusion to the disabled features, after the
// user's own; a user who enables it in WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS
// keeps it on.
func occlusionOff(disabled, enabled []string) []string {
	if slices.Contains(enabled, webViewOcclusion) {
		return disabled
	}
	return addFeatures(disabled, webViewOcclusion)
}

// addFeatures adds a comma list's features to have, each once.
func addFeatures(have []string, list string) []string {
	for _, f := range strings.Split(strings.Trim(list, `"`), ",") {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(have, f) {
			have = append(have, f)
		}
	}
	return have
}

// splitArgs splits a command line on spaces and tabs outside double quotes,
// keeping each argument as written, its quotes too.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// WebView2 that does not come up. A magpie started while the one before it
// is still going down finds the profile folder held by the old
// msedgewebview2, and Wails waits a minute for a controller that never
// arrives, then exits (@ljlwlcz on X, 0.1.3688: a restart by a script left
// no window). Two things answer it: the start waits for the folder to be
// free, and a start that still fails is tried again, a few times.

// webView2TryEnv counts the starts that ended in a WebView2 failure; each
// relaunch passes it on.
const webView2TryEnv = "MAGPIE_WV2_TRY"

// webView2Tries is how many starts are made before the failure is shown.
const webView2Tries = 3

// webView2Folder is the folder WebView2 keeps its profile in: the one magpie
// passes Wails (webViewUserDataPath), else Wails' own default,
// %AppData%\<exe name>.
func webView2Folder(userDataPath, appData, exe string) string {
	if userDataPath != "" {
		return userDataPath
	}
	if appData == "" || exe == "" {
		return ""
	}
	return filepath.Join(appData, filepath.Base(exe))
}

// webView2Failure says whether err is the webview not coming up, the
// errors Wails' Embed reports: the controller timeout and the loader's.
func webView2Failure(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "webview2")
}

// webView2Try is the number of starts already made, from the env value.
func webView2Try(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// webView2Backoff is how long a start waits before the n-th try (n >= 1).
func webView2Backoff(n int) time.Duration {
	return time.Duration(n) * 2 * time.Second
}

// awaitWebView2Folder polls free until it says so or max passes, and says
// whether it was free. sleep is a var for tests.
func awaitWebView2Folder(free func() bool, max, step time.Duration, sleep func(time.Duration)) bool {
	for waited := time.Duration(0); ; waited += step {
		if free() {
			return true
		}
		if waited >= max {
			return false
		}
		sleep(step)
	}
}

// webView2FailedNotice is the box shown when the webview would not start
// after every try: what failed and what to do, in lang (English where it
// has none of its own).
func webView2FailedNotice(lang, reason, version string) (title, body string) {
	if strings.HasPrefix(lang, "zh") {
		return "magpie 的窗口没能打开",
			fmt.Sprintf("WebView2 在 %d 次启动里都没有就绪（运行时 %s）。\r\n\r\n%s\r\n\r\n请结束残留的 msedgewebview2.exe 后重新打开 magpie；也可以用 \"magpie web\" 在浏览器里打开。详情见 magpie 的日志。", webView2Tries, version, reason)
	}
	return "magpie's window didn't open",
		fmt.Sprintf("WebView2 was not ready in %d starts (runtime %s).\r\n\r\n%s\r\n\r\nEnd any leftover msedgewebview2.exe and open magpie again, or use \"magpie web\" in your browser. The details are in magpie's log.", webView2Tries, version, reason)
}
