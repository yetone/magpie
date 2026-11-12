package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Another magpie's word on how long its Claude Code's cache is kept is
// taken from a magpie's request alone, and only as one of its three
// values; this computer's Claude Code is known by its address.
func TestClaudeCacheTTLFromAMagpieOnly(t *testing.T) {
	for _, c := range []struct {
		name, ua, addr, ttl string
		want                string
		here                bool
	}{
		{name: "a magpie's 5m", ua: "magpie/0.1.3267", addr: "192.0.2.7:5000", ttl: "5m", want: "5m"},
		{name: "a magpie's default", ua: "magpie/0.1.3267", addr: "192.0.2.7:5000", ttl: "default", want: "default"},
		{name: "a magpie's unknown value", ua: "magpie/0.1.3267", addr: "192.0.2.7:5000", ttl: "7d"},
		{name: "Claude Code's own header", ua: "claude-cli/2.1.316 (external, cli)", addr: "192.0.2.7:5000", ttl: "1h"},
		{name: "Claude Code here", ua: "claude-cli/2.1.316 (external, cli)", addr: "127.0.0.1:5000", ttl: "1h", here: true},
		{name: "Codex here", ua: "codex_cli_rs/0.50.0", addr: "127.0.0.1:5000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			r.RemoteAddr = c.addr
			r.Header.Set("User-Agent", c.ua)
			if c.ttl != "" {
				r.Header.Set(ClaudeCacheTTLHeader, c.ttl)
			}
			got := callerFrom(r)
			if got.cacheTTL != c.want || got.claudeHere != c.here {
				t.Errorf("caller: ttl %q here %v, want %q %v", got.cacheTTL, got.claudeHere, c.want, c.here)
			}
			var seen *http.Request
			withCaller(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r })).ServeHTTP(httptest.NewRecorder(), r)
			if seen == nil || seen.Header.Get(ClaudeCacheTTLHeader) != "" {
				t.Errorf("the header went on past withCaller")
			}
		})
	}
}

// What a Remote magpie is told of the cache lifetime: Claude Code's here
// is its user's pick, "default" with none, nothing when settings.json
// can't be read; another magpie's word goes on as it came; no other
// agent's request names one.
func TestClaudeCacheTTLOf(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", "")
	t.Setenv("FORCE_PROMPT_CACHING_5M", "")
	settings := filepath.Join(config, "settings.json")
	for _, c := range []struct {
		name, settings string
		who            caller
		want           string
	}{
		{name: "here, nothing picked", who: caller{agent: "claude", claudeHere: true}, want: "default"},
		{name: "here, empty settings", settings: " ", who: caller{agent: "claude", claudeHere: true}, want: "default"},
		{name: "here, 5m", settings: `{"promptCacheTtl":"5m"}`, who: caller{agent: "claude", claudeHere: true}, want: "5m"},
		{name: "here, 1h in settings' env", settings: `{"env":{"CLAUDE_CODE_PROMPT_CACHE_TTL":"1h"},"promptCacheTtl":"5m"}`, who: caller{agent: "claude", claudeHere: true}, want: "1h"},
		{name: "here, forced", settings: "// picked\n{\"env\":{\"FORCE_PROMPT_CACHING_5M\":\"1\"},\"promptCacheTtl\":\"1h\",}", who: caller{agent: "claude", claudeHere: true}, want: "5m"},
		{name: "here, unreadable", settings: `{"promptCacheTtl":"5m"`, who: caller{agent: "claude", claudeHere: true}},
		{name: "passed on", settings: `{"promptCacheTtl":"5m"}`, who: caller{agent: "claude", cacheTTL: "1h"}, want: "1h"},
		{name: "Claude Desktop, marks with no ttl are no pick", settings: `{"promptCacheTtl":"5m"}`, who: caller{agent: "claude-desktop"}, want: "default"},
		{name: "Claude Desktop through a magpie", who: caller{agent: "claude-desktop", cacheTTL: "5m"}, want: "5m"},
		{name: "another agent", settings: `{"promptCacheTtl":"5m"}`, who: caller{agent: "codex"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(settings)
			if c.settings != "" {
				if err := os.WriteFile(settings, []byte(c.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := claudeTTLOf(c.who); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// Claude Code on a Mac, through its magpie, to a magpie on another
// computer (linkaww's Docker one): the other magpie is told the Mac's
// pick, which Claude Code's own marks can't say, and the vendor never is.
func TestClaudeCacheTTLReachesTheRemoteMagpie(t *testing.T) {
	var mu sync.Mutex
	var told []string // the header as the remote magpie was sent it
	var leaked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
				leaked = append(leaked, k)
			}
		}
		mu.Unlock()
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
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", "")
	t.Setenv("FORCE_PROMPT_CACHING_5M", "")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	there := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			mu.Lock()
			told = append(told, r.Header.Get(ClaudeCacheTTLHeader))
			mu.Unlock()
		}
		there.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	send := func(ua, addr, settings string) string {
		t.Helper()
		os.Remove(filepath.Join(config, "settings.json"))
		if settings != "" {
			if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		n := len(told)
		mu.Unlock()
		body := `{"model":"office/fake/m1","max_tokens":10,"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(told) != n+1 {
			t.Fatalf("the remote magpie was asked %d times", len(told)-n)
		}
		return told[n]
	}
	const claude, here = "claude-cli/2.1.316 (external, cli)", "127.0.0.1:51000"
	if got := send(claude, here, `{"promptCacheTtl":"5m"}`); got != "5m" {
		t.Errorf("a 5m pick: the remote magpie was told %q", got)
	}
	if got := send(claude, here, ""); got != "default" {
		t.Errorf("nothing picked: the remote magpie was told %q", got)
	}
	if got := send(claude, here, `{"promptCacheTtl":"5m"`); got != "" {
		t.Errorf("settings.json unreadable: the remote magpie was told %q", got)
	}
	if got := send("opencode/1.2.3", here, `{"promptCacheTtl":"5m"}`); got != "" {
		t.Errorf("another agent: the remote magpie was told %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Errorf("the vendor was sent magpie's headers: %v", leaked)
	}
}

// Claude Desktop's marks carry no ttl (Claude Code takes the gateway key
// for an API key: 5 minutes), yet its run is on a subscription: it keeps an
// hour, as Claude Code's default is there, and a 1h mark stays 1h
// (DIYgod on X: Desktop through magpie wrote 5-minute cache).
func TestClaudeDesktopRunKeepsAnHour(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", "")
	t.Setenv("FORCE_PROMPT_CACHING_5M", "")
	for _, c := range []struct {
		agent, asked, want string
	}{
		{"claude-desktop", "5m", "1h"},
		{"claude-desktop", "1h", "1h"},
		{"claude-desktop", "", "1h"},
		{"opencode", "5m", "5m"},
	} {
		req := &Request{askedTTL: c.asked, pickedTTL: claudeTTLOf(caller{agent: c.agent})}
		if got := runTTLFor(req, nil); got != c.want {
			t.Errorf("%s asking %q: %q, want %q", c.agent, c.asked, got, c.want)
		}
	}
}
