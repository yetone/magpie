package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin whose vendor took the request and never answered it (the Devin
// plugin answers its head only once Devin sends the reply's first words)
// left the agent on keepalives with no error for as long as it waited
// (wu_chong4136 on X: 10 to 20 minutes). The plugin's fetch now gives the
// head up after the reply wait, and the agent gets an error it can retry,
// on a stream and on a whole reply. Before, both requests held until the
// client's own deadline.
func TestPluginHeadNeverSentEndsInAnError(t *testing.T) {
	t.Cleanup(plugin.ReplyWaitForTest(300 * time.Millisecond))
	var asked, open atomic.Int64
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		open.Add(1)
		defer open.Add(-1)
		// read whole, so the server watches the connection and sees the
		// host's abort close it
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done() // took the request, never answers
	})
	url, model, client := pluginReplyGateway(t, up)
	for _, stream := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(pluginReplyRequest(model, stream)))
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("stream=%t: %v", stream, err)
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		if err != nil {
			t.Fatalf("stream=%t: the reply was held to the client's deadline: %v", stream, err)
		}
		if !strings.Contains(string(b), "sent no reply") {
			t.Fatalf("stream=%t: status %d, body %q, want the give-up named", stream, res.StatusCode, b)
		}
		if res.StatusCode == http.StatusOK && !strings.Contains(string(b), "error") {
			t.Fatalf("stream=%t: a 200 with no error: %q", stream, b)
		}
		t.Logf("stream=%t: %d %s", stream, res.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for open.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("asked %d, open %d", asked.Load(), open.Load())
	if n := open.Load(); n > 0 {
		t.Fatalf("%d of %d vendor requests still open after the fetch was given up on", n, asked.Load())
	}
}
