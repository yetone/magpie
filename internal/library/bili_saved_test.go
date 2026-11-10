package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// biliOverviewFixture is GET /__bili/overview as bili 0.1.213 answered it
// on mote, after ten turns of a growing conversation through its /bili/
// prefix with a 20000-token window: seven folds, net 166285.
func biliOverviewFixture(t *testing.T) []byte {
	b, err := os.ReadFile(filepath.Join("testdata", "bili", "overview-0.1.213.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// biliRegister writes an instance file as bili 0.1.213 writes it.
func biliRegister(t *testing.T, h, id, origin string, startedAt int64) {
	t.Helper()
	write(t, filepath.Join(h, ".local", "state", "billion-context", "instances", id+".json"),
		fmt.Sprintf(`{"origin":%q,"instanceId":%q,"pid":86206,"startedAt":%d,"host":"127.0.0.1","port":18799,"passthrough":false,"mitmDomains":[],"modelWindows":{},"codeFingerprint":"9b3c7d02","sessionsDir":%q}`,
			origin, id, startedAt, filepath.Join(h, ".local", "share", "billion-context", "sessions")))
}

func biliServeOverview(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__bili/overview" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

// TestBiliSavedFromRunningBili: the numbers are bili's own, read from the
// newest registered bili that answers; one left behind by a crash, and
// one off this computer, are passed over; the answer is kept for when no
// bili runs.
func TestBiliSavedFromRunningBili(t *testing.T) {
	h := sandbox(t)
	now := time.Date(2026, 11, 10, 7, 21, 30, 0, time.UTC)
	was := biliNow
	biliNow = func() time.Time { return now }
	t.Cleanup(func() { biliNow = was })

	live := biliServeOverview(t, biliOverviewFixture(t))
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	biliRegister(t, h, "live", live.URL, 1794295272947)
	biliRegister(t, h, "gone", gone.URL, 1794295272999)
	biliRegister(t, h, "far", "http://192.0.2.7:8787", 1794295273999)

	fetch := biliFetch
	var asked []string
	biliFetch = func(ctx context.Context, origin string) (*biliOverview, error) {
		asked = append(asked, origin)
		return fetch(ctx, origin)
	}
	t.Cleanup(func() { biliFetch = fetch })

	s := readBiliSaved()
	if s == nil {
		t.Fatal("nothing read from a running bili")
	}
	want := BiliSaved{Net: 166285, Gross: 166390, HasFolds: true, Folds: 7, Sessions: 1, Requests: 10, Input: 175279,
		At: "2026-11-10T07:21:30Z", Live: true, Version: "0.1.213"}
	if *s != want {
		t.Fatalf("read\n%+v\nwant\n%+v", *s, want)
	}
	if strings.Join(asked, " ") != gone.URL+" "+live.URL {
		t.Fatalf("asked %q, want the crashed one then the live one, never 192.0.2.7", asked)
	}

	// no bili runs: the last answer, marked as kept
	live.Close()
	asked = nil
	s = readBiliSaved()
	want.Live = false
	if s == nil || *s != want {
		t.Fatalf("with no bili running read %+v, want %+v", s, want)
	}

	// a kept answer that doesn't parse is no answer, never zeros
	write(t, filepath.Join(settings.Dir(), "bili-saved.json"), `{"net":`)
	if s := readBiliSaved(); s != nil {
		t.Fatalf("read %+v from a half-written file", *s)
	}
}

// TestBiliSavedNoFolds: before bili has folded anything there is no net
// figure, as its dashboard shows none; the requests still count.
func TestBiliSavedNoFolds(t *testing.T) {
	h := sandbox(t)
	var o map[string]any
	if err := json.Unmarshal(biliOverviewFixture(t), &o); err != nil {
		t.Fatal(err)
	}
	ov := o["overview"].(map[string]any)
	ov["hasFoldData"], ov["netSavedTotal"], ov["grossSavedTotal"], ov["foldsTotal"], ov["tokensSaved"] = false, 0, 0, 0, 0
	ov["requests"], ov["inputTokens"] = 3, 1200
	b, _ := json.Marshal(o)
	live := biliServeOverview(t, b)
	biliRegister(t, h, "live", live.URL, 1)

	s := readBiliSaved()
	if s == nil || s.HasFolds || s.Net != 0 || s.Folds != 0 || s.Requests != 3 || s.Input != 1200 || !s.Live {
		t.Fatalf("read %+v", s)
	}
}

// TestBiliSavedNoneYet: no bili has run here, so nothing is said.
func TestBiliSavedNoneYet(t *testing.T) {
	sandbox(t)
	fetch := biliFetch
	biliFetch = func(context.Context, string) (*biliOverview, error) {
		t.Fatal("asked with no bili registered")
		return nil, nil
	}
	t.Cleanup(func() { biliFetch = fetch })
	if s := readBiliSaved(); s != nil {
		t.Fatalf("read %+v with no bili", *s)
	}
}

// TestBiliSavedOnThePage: the Library's billion-context card gets it.
func TestBiliSavedOnThePage(t *testing.T) {
	h := sandbox(t)
	newFakeBili(t, h)
	live := biliServeOverview(t, biliOverviewFixture(t))
	biliRegister(t, h, "live", live.URL, 1)
	v := readBili(nil)
	if v.Saved == nil || v.Saved.Net != 166285 {
		t.Fatalf("saved %+v", v.Saved)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"saved":{"net":166285,"gross":166390,"hasFolds":true,"folds":7,`) {
		t.Fatalf("the page gets %s", b)
	}
}

func TestBiliLoopback(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://127.0.0.1:18799":      true,
		"http://localhost:8787":       true,
		"http://[::1]:8787":           true,
		"http://127.0.0.1:18799/":     true,
		"http://192.0.2.7:8787":       false,
		"https://127.0.0.1:8787":      false,
		"http://evil.localhost:8787":  false,
		"http://127.0.0.1.nip.io:80":  false,
		"http://127.0.0.1":            false,
		"http://127.0.0.1:1/__bili/x": false,
		"":                            false,
	} {
		if got := biliLoopback(origin); got != want {
			t.Errorf("biliLoopback(%q) = %v, want %v", origin, got, want)
		}
	}
}
