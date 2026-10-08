package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A remote magpie (another computer's gateway as a provider) is asked in
// the API the model's own provider speaks there: a Chat client's request
// for a model served on Anthropic's Messages goes to it as Messages, not as
// Chat to be translated on the other side too; a routing group's, which
// could be any member, goes on in the client's own API. Here the gateway is
// both computers': "office" is it, reached over HTTP, and the vendor behind
// it speaks Anthropic's Messages alone.
func TestRemoteMagpieNativeAPI(t *testing.T) {
	var mu sync.Mutex
	var vendor, hops []string // paths the vendor, and the remote magpie, were asked on
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, r.URL.Path+" "+gjsonModel(b))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"input_tokens":42}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`data: {"type":"message_stop"}`))
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			hops = append(hops, r.URL.Path)
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)

	// added as the Add sheet does: the address alone, on the Chat field
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if office.Chat != remote.URL+"/v1" || office.Responses != remote.URL+"/v1" || office.Anthropic != remote.URL {
		t.Fatalf("endpoints: %q %q %q", office.Chat, office.Responses, office.Anthropic)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	live, _, _ := catalog.Live("office")
	apis := map[string][]string{}
	for _, m := range live {
		apis[m.ID] = m.APIs
	}
	if !slices.Equal(apis["fake/m1"], []string{"anthropic"}) {
		t.Fatalf("fake/m1 is served on Messages there: %v", apis)
	}
	if a, ok := apis["group/g"]; !ok || a != nil {
		t.Fatalf("the group is listed, on no one API: %v", apis)
	}

	calls := []struct{ path, body, hop, vendor string }{
		// Chat client, a model served on Messages: turned into Messages here, once
		{"/v1/chat/completions", `{"model":"office/fake/m1","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "/v1/messages m1"},
		// Anthropic client: Messages all the way
		{"/v1/messages", `{"model":"office/fake/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "/v1/messages m1"},
		{"/v1/messages/count_tokens", `{"model":"office/fake/m1","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages/count_tokens", "/v1/messages/count_tokens m1"},
		// a group's request goes on in the client's API; the remote picks
		{"/v1/responses", `{"model":"office/group/g","input":"hi","stream":true}`, "/v1/responses", "/v1/messages m1"},
		{"/v1beta/models/office/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "/v1/messages", "/v1/messages m1"},
		{"/v1beta/models/office/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "/v1/messages", "/v1/messages m1"},
	}
	for _, c := range calls {
		mu.Lock()
		vendor, hops = nil, nil
		mu.Unlock()
		code, body := post(t, c.path, c.body)
		mu.Lock()
		gotHops, gotVendor := slices.Clone(hops), slices.Clone(vendor)
		mu.Unlock()
		if code != 200 {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
		if !slices.Equal(gotHops, []string{c.hop}) || !slices.Equal(gotVendor, []string{c.vendor}) {
			t.Errorf("%s: remote asked on %v, vendor on %v; want %s, %s", c.path, gotHops, gotVendor, c.hop, c.vendor)
		}
		if strings.HasSuffix(c.path, "count_tokens") && !strings.Contains(body, `"input_tokens":42`) {
			t.Errorf("count: %s", body)
		}
		if !strings.HasSuffix(c.path, "count_tokens") && !strings.Contains(body, "hello") {
			t.Errorf("%s reply: %s", c.path, body)
		}
	}
}

func gjsonModel(b []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &v)
	return v.Model
}
