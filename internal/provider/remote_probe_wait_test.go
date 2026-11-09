package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A model test through a remote magpie waits as long as a plugin's test
// does here (pluginTestWait), since the remote serves plugins' models and
// retries for them, and tells the remote how long it waits (WaitHeader)
// so the remote answers with its error inside it (plugins#827: the test
// gave the remote 20s, the remote held its 502 for 45s and more).
func TestRemoteMagpieProbeWait(t *testing.T) {
	defer func(w, pw time.Duration) { testWait, pluginTestWait = w, pw }(testWait, pluginTestWait)
	testWait, pluginTestWait = 1*time.Second, 4*time.Second
	var mu sync.Mutex
	var said []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		said = append(said, r.Header.Get(WaitHeader))
		mu.Unlock()
		time.Sleep(1500 * time.Millisecond) // past testWait, within pluginTestWait
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	p := Provider{ID: "office", Preset: RemoteMagpiePreset, Key: "sk-magpie-k", Chat: srv.URL + "/v1"}
	r := probe(context.Background(), p, Chat, srv.URL+"/v1/chat/completions", []byte(`{"model":"m","stream":true}`), "m", testWait)
	if !r.OK {
		t.Errorf("remote magpie's test given testWait only: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(said) == 0 || said[0] != "4" {
		t.Errorf("%s said %q, want the probe's own wait, 4", WaitHeader, said)
	}
	// a provider that isn't a remote magpie is told nothing
	said = nil
	mu.Unlock()
	q := Provider{ID: "relay", Key: "k", Chat: srv.URL + "/v1"}
	probe(context.Background(), q, Chat, srv.URL+"/v1/chat/completions", []byte(`{"model":"m","stream":true}`), "m", 3*time.Second)
	mu.Lock()
	if len(said) != 1 || said[0] != "" {
		t.Errorf("a relay was told %s %q", WaitHeader, said)
	}
}
