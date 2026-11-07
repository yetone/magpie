package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A request held too long before routing leaves a report its user can
// reach: the GUI app's log goes only to stderr. The report names the wait
// it was held in, says where every goroutine stood, holds nothing of the
// request's content, and its route names it for the Routing page; the
// 504 sends its user to that report, not to a log.
func TestUnroutedHeldReportSaved(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	unroutedLimits(t, 50*time.Millisecond, time.Second)
	s, _ := slowEyed(t, 0)
	heldSight(t, s)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	// #4300: the client reads the 504, which says Connection: close, and
	// closes; its request's context ends, and the description it was
	// held in lets go. Held here until it has, the report still says
	// what held it.
	hook := func(u *unroutedHold) {
		select {
		case <-u.r.Context().Done():
		case <-time.After(10 * time.Second):
			t.Error("the request's context didn't end once its client had the 504")
			return
		}
		for deadline := time.Now().Add(10 * time.Second); strings.Contains(u.waitsSaid(), "(under way)"); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the description wait didn't end with its request's context")
				return
			}
		}
	}
	heldAnswered.Store(&hook)
	t.Cleanup(func() { heldAnswered.Store(nil) })

	const secret = "hunter2-held-report-secret"
	body := strings.Replace(pastedImage, `"text":"read"`, `"text":"`+secret+`"`, 1)
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("held request answered %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "Copy Report") || !strings.Contains(string(b), "Routing page") || strings.Contains(string(b), "magpie's log") {
		t.Fatalf("the 504 doesn't send its user to the report: %s", b)
	}

	// the 504 goes first, and the report is saved as it is answered
	var rs []Route
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		rs = routesOf(s, "probe/text")
		if len(rs) == 1 && rs[0].Held != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the held request's route names no report: %d routes, held %q", len(rs), func() string {
				if len(rs) > 0 {
					return rs[0].Held
				}
				return ""
			}())
		}
	}
	text, err := HeldReport(rs[0].Held)
	if err != nil {
		t.Fatalf("its report %q: %v", rs[0].Held, err)
	}
	for _, want := range []string{"POST /v1/chat/completions", "probe/text", "image description", "(under way)", "body room:", "goroutine profile:"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report doesn't say %q:\n%.2000s", want, text)
		}
	}
	if strings.Contains(text, secret) || strings.Contains(text, "aGVsbG8=") {
		t.Errorf("the report holds the request's content:\n%.2000s", text)
	}
	if fi, err := os.Stat(HeldDir()); err != nil || fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the reports' folder: %v %v", fi, err)
	}
}

// The newest heldKept reports are kept, and only a report's own name is
// read.
func TestHeldReportsKeptAndNamed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var names []string
	for i := range heldKept + 3 {
		n, err := writeHeld(fmt.Sprintf("report %d", i))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	es, err := os.ReadDir(HeldDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != heldKept {
		t.Fatalf("%d files kept, want %d: %v", len(es), heldKept, es)
	}
	if _, err := HeldReport(names[0]); err == nil {
		t.Error("the oldest report was kept")
	}
	if got, err := HeldReport(names[len(names)-1]); err != nil || got != fmt.Sprintf("report %d", heldKept+2) {
		t.Errorf("the newest report: %q %v", got, err)
	}
	os.WriteFile(filepath.Join(HeldDir(), "other.txt"), []byte("x"), 0o600)
	for _, bad := range []string{"other.txt", "../held/" + names[len(names)-1], "", ".held-1"} {
		if _, err := HeldReport(bad); err == nil {
			t.Errorf("HeldReport(%q) read a file that isn't a report", bad)
		}
	}
}
