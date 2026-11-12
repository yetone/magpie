package agent

import (
	"slices"
	satomic "sync/atomic"
	"testing"
	"time"
)

func TestSpotlightParse(t *testing.T) {
	out := "/Applications/ChatGPT.app   kMDItemCFBundleIdentifier = com.openai.codex\n" +
		"/Users/a/.Trash/ChatGPT.app   kMDItemCFBundleIdentifier = com.openai.codex\n" +
		"/Applications/My Apps/Qoder.app   kMDItemCFBundleIdentifier = com.qoder.ide\n" +
		"/Applications/Other.app   kMDItemCFBundleIdentifier = com.other\n"
	m := spotlightParse(out, []string{"com.openai.codex", "com.qoder.ide", "dev.none"})
	if got := m["com.openai.codex"]; !slices.Equal(got, []string{"/Applications/ChatGPT.app"}) {
		t.Errorf("codex = %q", got)
	}
	if got := m["com.qoder.ide"]; !slices.Equal(got, []string{"/Applications/My Apps/Qoder.app"}) {
		t.Errorf("qoder = %q", got)
	}
	if _, ok := m["dev.none"]; !ok || m["dev.none"] != nil {
		t.Errorf("an id Spotlight has none of is kept as none: %v", m)
	}
	if _, ok := m["com.other"]; ok {
		t.Errorf("an id not asked for is kept: %v", m)
	}
}

// Every known bundle id is asked in one mdfind, and once the answer is
// stale a caller gets it at once while one ask runs again behind it: a
// Sessions read after five idle minutes waited 2.3s on 13 mdfinds.
func TestSpotlightOneAskStaleNeverWaits(t *testing.T) {
	was := spotlightAsk
	spotlight.Lock()
	spotlight.m, spotlight.at, spotlight.asking, spotlight.settled = nil, time.Time{}, false, nil
	spotlight.Unlock()
	t.Cleanup(func() {
		spotlightAsk = was
		spotlight.Lock()
		spotlight.m, spotlight.at, spotlight.asking, spotlight.settled = nil, time.Time{}, false, nil
		spotlight.Unlock()
	})
	var asks satomic.Int32
	release := make(chan struct{})
	spotlightAsk = func(ids []string) map[string][]string {
		if !slices.Contains(ids, "com.openai.codex") || len(ids) < 2 {
			t.Errorf("asked for %q, want every known id", ids)
		}
		if asks.Add(1) > 1 {
			<-release
		}
		return map[string][]string{"com.openai.codex": {"/Applications/ChatGPT.app"}}
	}
	for range 5 {
		spotlightKept("com.openai.codex")
		spotlightKept("com.qoder.ide")
	}
	if n := asks.Load(); n != 1 {
		t.Fatalf("%d asks for 10 reads, want 1", n)
	}
	spotlight.Lock()
	spotlight.at = time.Now().Add(-spotlightFresh)
	spotlight.Unlock()
	done := make(chan []string)
	go func() { done <- spotlightKept("com.openai.codex") }()
	select {
	case got := <-done:
		if !slices.Equal(got, []string{"/Applications/ChatGPT.app"}) {
			t.Errorf("stale answer = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a read of a stale answer waited for Spotlight")
	}
	spotlightKept("com.openai.codex")
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		spotlight.Lock()
		asking := spotlight.asking
		spotlight.Unlock()
		if !asking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh never settled")
		}
		time.Sleep(time.Millisecond)
	}
	if n := asks.Load(); n != 2 {
		t.Errorf("%d asks, want 2: one refresh for any number of stale reads", n)
	}
}
