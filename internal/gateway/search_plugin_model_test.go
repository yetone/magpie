package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A plugin that marks a model of its list as one that searches the web by
// itself (m.searches) is offered in Settings' searcher picker with that
// model alone, is the searcher magpie picks when nothing better is on, and
// searches when asked in plain words. Every plugin's provider was left out
// of the picker whatever its models did, so Trae's search model couldn't
// be picked (01huadalang on Discord, 10-20).
func TestPluginSearchModelIsASearcher(t *testing.T) {
	t.Setenv("FAKE_SEARCHER", "1")
	var mu sync.Mutex
	var asked []string
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q struct {
			Model string            `json:"model"`
			Tools []json.RawMessage `json:"tools"`
		}
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+q.Model)
		mu.Unlock()
		if q.Model != "fake-search" || len(q.Tools) > 0 {
			http.Error(w, `{"error":{"message":"unexpected"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"上海今天小雨，22/17℃。来源：https://www.weather.com.cn/weather/101020100.shtml"},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))

	var offered []string
	for _, c := range Searchers() {
		for _, m := range c.Models {
			offered = append(offered, c.Provider.ID+"/"+m.ID)
		}
		if c.Provider.ID == pid && (c.Small != "fake-search" || c.ManualOnly) {
			t.Fatalf("the plugin searches with %q, manual only %v", c.Small, c.ManualOnly)
		}
	}
	if !slices.Equal(offered, []string{pid + "/fake-search"}) {
		t.Fatalf("the picker offers %v, want the plugin's fake-search alone", offered)
	}
	p, m, ok := searcher()
	if !ok || p.ID != pid || m != "fake-search" {
		t.Fatalf("searcher = %s %s %v", p.ID, m, ok)
	}
	// and a Remote magpie's list says it searches natively, so a magpie
	// that has this one as its Remote magpie may pick it too (#7475),
	// and the plugin's other models are searched for
	if got := webSearchOf(provider.Entry{Provider: &p, Model: "fake-search"}, true); got != searchNative {
		t.Errorf("the plugin's search model is listed web_search %q, want %q", got, searchNative)
	}
	for _, other := range p.Available() {
		if other.ID == "fake-search" {
			continue
		}
		if got := webSearchOf(provider.Entry{Provider: &p, Model: other.ID}, true); got == searchNative {
			t.Errorf("the plugin's %s is listed web_search %q", other.ID, got)
		}
	}

	said, _, err := New().webSearch(context.Background(), "今天上海天气")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "22/17℃") {
		t.Fatalf("the search said %q", said)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{"/v1/chat/completions fake-search"}) {
		t.Fatalf("the vendor was asked %q", got)
	}
}
