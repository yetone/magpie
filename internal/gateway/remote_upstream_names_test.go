package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// upstreamList is what another magpie, its own Provider in model names
// Off, lists for this one: DeepSeek Chat from two of its relays, each
// labelled by its name alone, so the agents here saw two "DeepSeek Chat ·
// Remote magpie" (lcc580 on X). magpie_by names the provider serving each
// there.
const upstreamList = `{"object":"list","data":[` +
	`{"id":"relaya/deepseek-chat","object":"model","owned_by":"relaya","display_name":"DeepSeek Chat","magpie_label":"DeepSeek Chat","magpie_by":"Relay A","native_endpoints":["/v1/chat/completions"],"context_length":128000},` +
	`{"id":"relayb/deepseek-chat","object":"model","owned_by":"relayb","display_name":"DeepSeek Chat","magpie_label":"DeepSeek Chat","magpie_by":"Relay B","native_endpoints":["/v1/chat/completions"],"context_length":128000},` +
	`{"id":"group/fast","object":"model","owned_by":"relaya","display_name":"Fast","magpie_label":"Fast","magpie_by":"routing group","context_length":128000}]}`

type listed struct {
	ID    string `json:"id"`
	Label string `json:"magpie_label"`
	By    string `json:"magpie_by"`
}

func listFor(t *testing.T, asMagpie bool) map[string]listed {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	if asMagpie {
		req.Header.Set(provider.DrawersHeader, "1")
	}
	New().Handler().ServeHTTP(rec, req)
	var out struct{ Data []listed }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]listed{}
	for _, m := range out.Data {
		got[m.ID] = m
	}
	return got
}

// A Remote magpie's upstream names name each of its models by the
// provider serving it there, whatever either magpie's Provider in model
// names is, and its ids stay as they were. Off, the default, they are
// named as before. Listed on to a third magpie, a model names the provider
// at the start of the chain, not the Remote magpie it came through.
func TestRemoteMagpieUpstreamNames(t *testing.T) {
	fresh(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamList)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: "magpie", Preset: provider.RemoteMagpiePreset, Chat: remote.URL})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find(id)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.SetSuffixMode(provider.SuffixOn) })
	set := func(on bool) {
		t.Helper()
		p, _ := provider.Find(id)
		p.UpstreamProviders = on
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		on          bool
		mode        string
		a, b, group string
	}{
		{false, provider.SuffixOn, "DeepSeek Chat · Office", "DeepSeek Chat · Office", "Fast · Office"},
		{false, provider.SuffixOff, "DeepSeek Chat · Office", "DeepSeek Chat · Office", "Fast"},
		{true, provider.SuffixOn, "DeepSeek Chat · Office › Relay A", "DeepSeek Chat · Office › Relay B", "Fast · Office › routing group"},
		{true, provider.SuffixOwn, "DeepSeek Chat · Office › Relay A", "DeepSeek Chat · Office › Relay B", "Fast · Office › routing group"},
		{true, provider.SuffixOff, "DeepSeek Chat · Office › Relay A", "DeepSeek Chat · Office › Relay B", "Fast · Office › routing group"},
	} {
		set(c.on)
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		got := listFor(t, false)
		a, b, g := got["office/relaya/deepseek-chat"], got["office/relayb/deepseek-chat"], got["office/group/fast"]
		if a.Label != c.a || b.Label != c.b || g.Label != c.group {
			t.Errorf("upstream names %v, %s: %q, %q, %q; want %q, %q, %q", c.on, c.mode, a.Label, b.Label, g.Label, c.a, c.b, c.group)
		}
		// an agent's list is told no magpie_by; another magpie's is
		if a.By != "" {
			t.Errorf("an agent's list has magpie_by %q", a.By)
		}
	}
	// a third magpie is told the provider at the start of the chain
	got := listFor(t, true)
	for id, want := range map[string]string{"office/relaya/deepseek-chat": "Relay A", "office/relayb/deepseek-chat": "Relay B", "office/group/fast": "routing group"} {
		if got[id].By != want {
			t.Errorf("%s: magpie_by %q, want %q", id, got[id].By, want)
		}
	}
	// a request still names the model by its id, which goes to the remote
	if _, ok := got["office/relaya/deepseek-chat"]; !ok || strings.Contains(got["office/relaya/deepseek-chat"].ID, "›") {
		t.Errorf("ids changed: %v", got)
	}
}
