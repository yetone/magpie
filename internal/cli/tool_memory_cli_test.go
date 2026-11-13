package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// toolSandbox is a HOME of the test's own and a real gateway in front of
// it, which magpie tool asks.
func toolSandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	t.Setenv("MAGPIE_GATEWAY", gw.URL)
}

// magpie tool memory_search failed with "isn't JSON-RPC" and exit 1, and
// memory_save said so after saving (#6049, #6392, Norethcy on X): the CLI
// named no folder, so the memory server asked it for its roots on an event
// stream, which the CLI read as one JSON body. It names the folder it runs
// in now, so --scope project is this folder's project, and it answers at
// once, without the server's wait for roots it never gave.
func TestToolMemoryThroughTheGateway(t *testing.T) {
	toolSandbox(t)
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://github.com/someone/proj.git\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	start := time.Now()
	out, err := runOut(t, "tool", "memory_save", "Heavy tests run on mote", "--kind", "fact", "--scope", "project")
	if err != nil || !strings.Contains(out, "Remembered") || !strings.Contains(out, "this project") {
		t.Fatalf("memory_save: %v\n%s", err, out)
	}
	out, err = runOut(t, "tool", "memory_search", "--scope", "project", "--limit", "10", "--json")
	if err != nil {
		t.Fatalf("memory_search --json: %v\n%s", err, out)
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "Heavy tests run on mote") {
		t.Fatalf("memory_search --json printed %q (%v)", out, err)
	}
	// the server's wait for roots is 3s a call
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("two calls took %s: the server waited for roots", took)
	}
}

// A server that asks the CLI something on the call's event stream gets an
// answer, and the call's own result, sent after on that stream, is read:
// roots/list is the folder the CLI runs in, anything else is refused.
func TestToolReadsAnEventStreamAndAnswersTheServer(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	answers := make(chan map[string]any, 4)
	var mu sync.Mutex
	var cwds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		cwds = append(cwds, r.Header.Get(gateway.MemoryCwdHeader))
		mu.Unlock()
		if _, ok := m["method"]; !ok {
			answers <- m
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if m["method"] != "tools/call" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"tools": []any{
				map[string]any{"name": "memory_search", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}},
			}}})
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("Accept %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			io.WriteString(w, "event: message\ndata: "+string(b)+"\n\n")
			w.(http.Flusher).Flush()
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"progress": 1}})
		var got []map[string]any
		for _, ask := range []string{"roots/list", "sampling/createMessage"} {
			send(map[string]any{"jsonrpc": "2.0", "id": "magpie-" + ask, "method": ask})
			select {
			case a := <-answers:
				got = append(got, a)
			case <-time.After(5 * time.Second):
				t.Errorf("%s wasn't answered", ask)
				return
			}
		}
		send(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "found it"}}, "isError": false}})
		mu.Lock()
		defer mu.Unlock()
		roots, _ := json.Marshal(got[0]["result"])
		if got[0]["id"] != "magpie-roots/list" || !strings.Contains(string(roots), `"uri":"file://`) || !strings.Contains(string(roots), filepath.ToSlash(filepath.Base(dir))) {
			t.Errorf("roots/list answered %v", got[0])
		}
		if got[1]["id"] != "magpie-sampling/createMessage" || got[1]["error"] == nil {
			t.Errorf("sampling answered %v", got[1])
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_GATEWAY", srv.URL)
	out, err := runOut(t, "tool", "memory_search", "--query", "anything")
	if err != nil || strings.TrimSpace(out) != "found it" {
		t.Fatalf("%v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range cwds {
		if c == "" {
			t.Fatalf("a request named no folder: %q", cwds)
		}
	}
}
