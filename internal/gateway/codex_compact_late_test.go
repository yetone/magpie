package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #5275 (hayden4608): Codex Desktop compacting on a Volcengine model, its
// ~3MB history still going up a slow uplink when Codex gave up at 300s.
// magpie made its own summary only after that, for nobody, and counted
// the compaction a success; Codex sent the same history again, another
// 300s, and the conversation never got shorter.

// silentUpstream is a provider whose vendor never answers: each request
// is held until it is given up on, as one whose body is still going up.
func silentUpstream(t *testing.T) (asked chan struct{}) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	asked = make(chan struct{}, 16)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		asked <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		up.Close()
	})
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return asked
}

func withCompactWithin(t *testing.T, d time.Duration) {
	t.Helper()
	was := compactWithin
	compactWithin = d
	t.Cleanup(func() { compactWithin = was })
}

// A summary that hasn't come by compactWithin is magpie's own, sent while
// Codex still waits: the compaction completes, saying why, and the vendor
// is no longer waited on.
func TestCodexCompactLateSummaryCompactsLocallyInTime(t *testing.T) {
	asked := silentUpstream(t)
	withCompactWithin(t, 300*time.Millisecond)
	// Codex's patience, far past compactWithin here as 300s is past 4m
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(codexCompactOf("fake/m1", "thread-late"))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	began := time.Now()
	New().Handler().ServeHTTP(rec, req)
	if ctx.Err() != nil {
		t.Fatalf("Codex gave up first, after %s: %d %s", time.Since(began), rec.Code, rec.Body)
	}
	select {
	case <-asked:
	default:
		t.Fatal("the vendor was never asked")
	}
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	sum := compactedText(t, rec.Body.String())
	for _, want := range []string{"no summary came in 300ms", "fix the login bug", "now add a test"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q, want it to say %q", sum, want)
		}
	}
}

// A compaction Codex left before it was answered is no success: what was
// made reached nobody, so the conversation's failures still count and its
// compaction is held after compactFailsMost of them, rather than cleared
// for the next to wait as long again.
func TestCodexCompactLeftBeforeAnsweredStillCounts(t *testing.T) {
	asked := silentUpstream(t)
	withCompactWithin(t, time.Minute)
	s := New()
	key := compactKey([]byte(codexCompactOf("fake/m1", "thread-left")))
	for range compactFailsMost - 1 {
		s.compactFails.failed(key, 502, "the reply ended before it was complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asked
		cancel() // Codex gives up while the vendor says nothing
	}()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(codexCompactOf("fake/m1", "thread-left"))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("a compaction written to a Codex that left: %s", rec.Body)
	}
	// held, it is answered at once; asked, the vendor would hold it
	actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer acancel()
	again := httptest.NewRecorder()
	areq := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(codexCompactOf("fake/m1", "thread-left"))).WithContext(actx)
	areq.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(again, areq)
	if again.Code != 400 || !strings.Contains(again.Body.String(), "times in a row") {
		t.Fatalf("after Codex left a third time: %d %s", again.Code, again.Body)
	}
	select {
	case <-asked:
		t.Error("the vendor was asked again while the conversation is held")
	default:
	}
}
