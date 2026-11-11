package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// summaryUpstream is a Responses upstream that summarises and counts the
// bodies it was sent.
func summaryUpstream(t *testing.T) (url string, count func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, summaryReply)
	}))
	t.Cleanup(up.Close)
	return up.URL + "/v1", func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// 0neB1aze on X: Codex's memories can be put on a model of its own, its
// compaction could not. A compaction magpie makes goes on the model set
// for it; unset, it stays on the session's.
func TestCodexCompactOnItsOwnModel(t *testing.T) {
	setup(t, provider.Responses, &fake{t: t})
	sessionURL, session := summaryUpstream(t)
	otherURL, other := summaryUpstream(t)
	for _, p := range []provider.Provider{
		{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: sessionURL},
		{ID: "cheap", Name: "Cheap", Key: "k", Models: []string{"m2"}, Responses: otherURL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	code, body := codexPost(t, systemCompaction)
	if code != 200 || session() != 1 || other() != 0 {
		t.Fatalf("unset: %d %s; session %d, other %d", code, body, session(), other())
	}
	if err := provider.SetCodexCompactModel("cheap/m2"); err != nil {
		t.Fatal(err)
	}
	code, body = codexPost(t, systemCompaction)
	if code != 200 || session() != 1 || other() != 1 {
		t.Fatalf("set: %d %s; session %d, other %d", code, body, session(), other())
	}
	if sum := compactedText(t, body); !strings.Contains(sum, "SUMMARY") {
		t.Errorf("summary %q", sum)
	}
}

// A compaction a ChatGPT account seals, or one put on such a model, is
// left where it is: only the account that wrote the summary reads it.
func TestCodexCompactToLeavesNative(t *testing.T) {
	fresh(t)
	if err := provider.SetCodexCompactModel("cheap/m2"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	never := func(string) bool { return false }
	if to := codexCompactTo(r, "fake/m1", true, never); to != "" {
		t.Errorf("a native compaction moved to %s", to)
	}
	if to := codexCompactTo(r, "fake/m1", false, func(string) bool { return true }); to != "" {
		t.Errorf("moved to a model that compacts natively: %s", to)
	}
}
