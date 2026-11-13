package gui

import (
	"testing"
	"time"
)

// aquaarboraurum on X: a main window gone white came back only when closed
// to the tray and opened again. Each WebView2 process failure now gets the
// mend that brings its window back.
func TestWebViewMendForEachFailure(t *testing.T) {
	for kind, want := range map[uint32]webViewMend{
		wv2BrowserExited:       mendRemake,
		wv2RenderExited:        mendReload,
		wv2RenderUnresponsive:  mendReload,
		wv2FrameRenderExited:   mendReload,
		wv2UtilityExited:       mendNothing,
		wv2SandboxHelperExited: mendNothing,
		wv2GPUExited:           mendRedraw,
		wv2PPAPIPluginExited:   mendNothing,
		wv2PPAPIBrokerExited:   mendNothing,
		wv2UnknownExited:       mendRedraw,
		42:                     mendRedraw, // a kind of a later runtime
		1<<32 - 1:              mendRedraw, // the kind couldn't be read
	} {
		what, got := webViewMendFor(kind)
		if got != want || what == "" {
			t.Errorf("kind %d: %q %v, want %v", kind, what, got, want)
		}
		if mendWords[got] == "" {
			t.Errorf("kind %d: mend %v has no words for the log", kind, got)
		}
	}
}

// A page that takes its renderer down again as it loads is not reloaded
// without end; a redraw is never held back.
func TestMendGapHoldsOnlyReloadAndRemake(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		m    webViewMend
		last time.Time
		want bool
	}{
		{mendReload, time.Time{}, true},
		{mendReload, now.Add(-mendGap + time.Second), false},
		{mendReload, now.Add(-mendGap), true},
		{mendRemake, now.Add(-time.Second), false},
		{mendRemake, now.Add(-time.Hour), true},
		{mendRedraw, now.Add(-time.Second), true},
		{mendNothing, now, true},
	} {
		if got := mendAllowed(c.m, c.last, now); got != c.want {
			t.Errorf("%v last %s ago: %v, want %v", c.m, now.Sub(c.last), got, c.want)
		}
	}
}
