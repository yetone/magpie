package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForcedHourCycle(t *testing.T) {
	for _, c := range []struct{ f24, f12, want string }{
		{"1\n", "", "h23"},
		{"", "1\n", "h12"},
		{"1\n", "1\n", "h23"},
		{"", "", ""},
		{"0\n", "0\n", ""},
		{"Error: Could not find key", "", ""},
	} {
		if got := forcedHourCycle(c.f24, c.f12); got != c.want {
			t.Errorf("forcedHourCycle(%q, %q) = %q, want %q", c.f24, c.f12, got, c.want)
		}
	}
}

func TestLocaleTag(t *testing.T) {
	for raw, want := range map[string]string{
		"en_JP\n":                 "en-JP",
		"en_US":                   "en-US",
		"en-JP":                   "en-JP",
		"en_JP.UTF-8":             "en-JP",
		"en_US@rg=jpzzzz":         "en-JP",
		"en_US@calendar=japanese": "en-US",
		"zh_Hans_CN":              "zh-CN",
		"en":                      "en",
		"C":                       "",
		"POSIX":                   "",
		"C.UTF-8":                 "",
		"":                        "",
	} {
		if got := localeTag(raw); got != want {
			t.Errorf("localeTag(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWindowsHourCycle(t *testing.T) {
	for format, want := range map[string]string{
		"HH:mm:ss":    "h23",
		"H:mm:ss":     "h23",
		"h:mm:ss tt":  "h12",
		"hh:mm:ss tt": "h12",
		"tt h:mm:ss":  "h12",
		"HH'h'mm":     "h23",
		"'h'HH:mm":    "h23", // the quoted h is text
		"'h' 'H'":     "",
		"":            "",
	} {
		if got := windowsHourCycle(format); got != want {
			t.Errorf("windowsHourCycle(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestGnomeHourCycle(t *testing.T) {
	for out, want := range map[string]string{"'24h'\n": "h23", "'12h'\n": "h12", "": "", "'other'": ""} {
		if got := gnomeHourCycle(out); got != want {
			t.Errorf("gnomeHourCycle(%q) = %q, want %q", out, got, want)
		}
	}
}

// boot.js tells the app's page the clock the system shows, before it
// paints, and a browser tab nothing: the browser formats its own times.
func TestBootSaysHourCycle(t *testing.T) {
	builtinHome(t)
	prev := systemHourCycle
	t.Cleanup(func() { systemHourCycle = prev })
	boot := func(h http.Handler) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
		return rec.Body.String()
	}
	systemHourCycle = func() string { return "h23" }
	if b := boot(Handler(nil, nil)); !strings.Contains(b, `"hourCycle":"h23"`) {
		t.Fatalf("the app, set to 24 hours: %s", b)
	}
	if b := boot(webPageHandler(t, webHost{}, nil)); strings.Contains(b, "hourCycle") {
		t.Fatalf("a browser tab keeps its own clock: %s", b)
	}
	systemHourCycle = func() string { return "" }
	if b := boot(Handler(nil, nil)); strings.Contains(b, "hourCycle") {
		t.Fatalf("the system names none: %s", b)
	}
	// a Mac set to English in Japan: no clock override, and the region is
	// the system's (iXerol on X)
	prevLoc := systemLocale
	t.Cleanup(func() { systemLocale = prevLoc })
	systemLocale = func() string { return "en-JP" }
	if b := boot(Handler(nil, nil)); !strings.Contains(b, `"locale":"en-JP"`) || strings.Contains(b, "hourCycle") {
		t.Fatalf("the app, region Japan and no override: %s", b)
	}
	if b := boot(webPageHandler(t, webHost{}, nil)); strings.Contains(b, "locale") {
		t.Fatalf("a browser tab keeps its own locale: %s", b)
	}
}
