package library

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake stdio server is this test binary, started again with
// MAGPIE_FAKE_MCP saying how it behaves.
const fakeMode = "MAGPIE_FAKE_MCP"

func TestFakeMCPServer(t *testing.T) {
	mode := os.Getenv(fakeMode)
	if mode == "" {
		t.Skip("run by the checker's tests")
	}
	if f := os.Getenv("MAGPIE_FAKE_MCP_LOG"); f != "" {
		l, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintf(l, "%d\n", os.Getpid())
		l.Close()
	}
	switch mode {
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit":
		fmt.Fprintln(os.Stderr, "starting…")
		fmt.Fprintln(os.Stderr, "Error: GITHUB_TOKEN is not set")
		os.Exit(3)
	case "hang":
		// a child of its own, which the check has to end too
		self, _ := os.Executable()
		c := exec.Command(self, "-test.run=^TestFakeMCPServer$")
		c.Env = append(os.Environ(), fakeMode+"=sleep")
		c.Start()
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	// ok: a log line on stdout first, as some servers write
	fmt.Println("server starting on stdio")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		switch m.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}`+"\n", *m.ID)
		case "notifications/initialized":
			os.WriteFile(os.Getenv("MAGPIE_FAKE_MCP_INIT"), []byte("yes"), 0o644)
		case "tools/list":
			fmt.Printf(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`+"\n"+`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"a"},{"name":"b"},{"name":"c"}]}}`+"\n", *m.ID)
		}
	}
	os.Exit(0)
}

func fakeStdio(t *testing.T, mode string, env map[string]string) *Server {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := map[string]string{fakeMode: mode}
	for k, v := range env {
		e[k] = v
	}
	return &Server{Name: "fake", Transport: "stdio", Command: self, Args: []string{"-test.run=^TestFakeMCPServer$"}, Env: e}
}

func short(t *testing.T, d time.Duration) {
	was := checkTimeout
	checkTimeout = d
	t.Cleanup(func() { checkTimeout = was })
}

func TestCheckStdio(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		initFile := filepath.Join(t.TempDir(), "init")
		h := CheckServer(context.Background(), fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_INIT": initFile}))
		if h.State != "ok" || h.Tools != 3 {
			t.Fatalf("got %+v, want ok with 3 tools", h)
		}
		if b, _ := os.ReadFile(initFile); string(b) != "yes" {
			t.Error("notifications/initialized wasn't sent before tools/list")
		}
	})
	t.Run("exited", func(t *testing.T) {
		h := CheckServer(context.Background(), fakeStdio(t, "exit", nil))
		if h.State != "error" || h.Why != "exited" || h.Code != 3 || h.Detail != "Error: GITHUB_TOKEN is not set" {
			t.Fatalf("got %+v, want exited 3 with the last stderr line", h)
		}
	})
	t.Run("not found", func(t *testing.T) {
		h := CheckServer(context.Background(), &Server{Name: "x", Transport: "stdio", Command: "magpie-no-such-mcp-server"})
		if h.State != "error" || h.Why != "notfound" || h.Detail != "magpie-no-such-mcp-server" {
			t.Fatalf("got %+v, want notfound", h)
		}
	})
	t.Run("timeout ends the tree", func(t *testing.T) {
		short(t, 1500*time.Millisecond)
		pids := filepath.Join(t.TempDir(), "pids")
		start := time.Now()
		h := CheckServer(context.Background(), fakeStdio(t, "hang", map[string]string{"MAGPIE_FAKE_MCP_LOG": pids}))
		if h.State != "error" || h.Why != "timeout" {
			t.Fatalf("got %+v, want timeout", h)
		}
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("took %s", d)
		}
		if runtime.GOOS == "windows" {
			return
		}
		b, _ := os.ReadFile(pids)
		list := strings.Fields(string(b))
		if len(list) != 2 {
			t.Fatalf("pids %q: the server and its child should both have started", list)
		}
		for _, p := range list {
			pid, _ := strconv.Atoi(p)
			// the child may stay a zombie a moment, reaped by init
			gone := false
			for i := 0; i < 50 && !gone; i++ {
				out, _ := exec.Command("ps", "-o", "stat=", "-p", p).Output()
				s := strings.TrimSpace(string(out))
				gone = s == "" || strings.HasPrefix(s, "Z")
				if !gone {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if !gone {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
				t.Errorf("process %d still runs after the check", pid)
			}
		}
	})
}

// mcpHandler is a streamable HTTP server: a session from initialize,
// which tools/list has to carry.
func mcpHandler(t *testing.T, sse bool, seen *http.Header) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		reply := ""
		switch m.Method {
		case "initialize":
			if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || !strings.Contains(r.Header.Get("Accept"), "application/json") {
				t.Errorf("Accept %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Mcp-Session-Id", "s-1")
			reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`, *m.ID)
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "s-1" {
				http.Error(w, "no session", 400)
				return
			}
			*seen = r.Header.Clone()
			reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"a"},{"name":"b"}]}}`, *m.ID)
		}
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: %s\n\n", reply)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}
}

func TestCheckHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("ok sse=%v", sse), func(t *testing.T) {
			var seen http.Header
			srv := httptest.NewServer(mcpHandler(t, sse, &seen))
			defer srv.Close()
			h := CheckServer(context.Background(), &Server{Name: "r", Transport: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer k"}})
			if h.State != "ok" || h.Tools != 2 {
				t.Fatalf("got %+v, want ok with 2 tools", h)
			}
			if seen.Get("Authorization") != "Bearer k" || seen.Get("MCP-Protocol-Version") != "2025-03-26" {
				t.Errorf("tools/list headers %v", seen)
			}
		})
	}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    ServerHealth
	}{
		{"oauth", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
			w.WriteHeader(401)
		}, ServerHealth{State: "auth", Code: 401, OAuth: true}},
		{"key", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }, ServerHealth{State: "auth", Code: 403}},
		{"404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, ServerHealth{State: "error", Why: "http", Code: 404, Detail: "404 Not Found"}},
		{"broken json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>hello</html>")
		}, ServerHealth{State: "error", Why: "protocol", Detail: "not an MCP reply (text/html): <html>hello</html>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			h := CheckServer(context.Background(), &Server{Name: "r", Transport: "http", URL: srv.URL})
			h.At = 0
			if h != c.want {
				t.Fatalf("got %+v, want %+v", h, c.want)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		short(t, time.Second)
		// the body read first: the server notices the client gone only then
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.ReadAll(r.Body)
			<-r.Context().Done()
		}))
		defer srv.Close()
		h := CheckServer(context.Background(), &Server{Name: "r", Transport: "http", URL: srv.URL})
		if h.State != "error" || h.Why != "timeout" {
			t.Fatalf("got %+v, want timeout", h)
		}
	})
	t.Run("refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		h := CheckServer(context.Background(), &Server{Name: "r", Transport: "http", URL: "http://" + addr + "/mcp"})
		if h.State != "error" || h.Why != "refused" {
			t.Fatalf("got %+v, want refused", h)
		}
	})
}

// The older SSE transport: the stream names where to post, and the replies
// come on the stream.
func TestCheckSSE(t *testing.T) {
	var mu sync.Mutex
	streams := map[string]chan string{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		ch := make(chan string, 8)
		mu.Lock()
		streams["1"] = ch
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=1\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case s := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", s)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ch := streams[r.URL.Query().Get("session")]
		mu.Unlock()
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		switch m.Method {
		case "initialize":
			ch <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`, *m.ID)
		case "tools/list":
			ch <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"only"}]}}`, *m.ID)
		}
		w.WriteHeader(202)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := CheckServer(context.Background(), &Server{Name: "s", Transport: "sse", URL: srv.URL + "/sse"})
	if h.State != "ok" || h.Tools != 1 {
		t.Fatalf("got %+v, want ok with 1 tool", h)
	}
}

// A server is started once a session; again when asked fresh, or when it
// was changed meanwhile. Nothing is written to the agents.
func TestCheckServersOnce(t *testing.T) {
	sandbox(t)
	checked.Lock()
	checked.m = map[string]checkedServer{}
	checked.Unlock()
	pids := filepath.Join(t.TempDir(), "pids")
	s := fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_LOG": pids})
	s.Agents = []string{}
	if _, err := SaveServer("", *s); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveServer("", Server{Name: "gone", Transport: "stdio", Command: "magpie-no-such-mcp-server", Agents: []string{}}); err != nil {
		t.Fatal(err)
	}
	starts := func() int {
		b, _ := os.ReadFile(pids)
		return len(strings.Fields(string(b)))
	}
	m, err := CheckServers(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if m["fake"].State != "ok" || m["fake"].Tools != 3 || m["gone"].Why != "notfound" || starts() != 1 {
		t.Fatalf("first check %+v, %d starts", m, starts())
	}
	m, _ = CheckServers(context.Background(), []string{"fake"}, false)
	if len(m) != 1 || m["fake"].Tools != 3 || starts() != 1 {
		t.Fatalf("second check %+v started it again: %d starts", m, starts())
	}
	CheckServers(context.Background(), []string{"fake"}, true)
	if starts() != 2 {
		t.Fatalf("fresh check: %d starts, want 2", starts())
	}
	s.Env["EXTRA"] = "1"
	if _, err := SaveServer("fake", *s); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, os.Getenv("HOME"))
	CheckServers(context.Background(), nil, false)
	if starts() != 3 {
		t.Fatalf("changed server: %d starts, want 3", starts())
	}
	if after := snapshot(t, os.Getenv("HOME")); after != before {
		t.Errorf("the check wrote files:\n%s\n---\n%s", before, after)
	}
}

// snapshot is every file under dir with its size and time.
func snapshot(t *testing.T, dir string) string {
	var b strings.Builder
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	return b.String()
}
