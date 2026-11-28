package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// affinityVersion is the one protocol version Affinity's MCP server takes:
// it embeds cpp-mcp with MCP_VERSION "2025-11-25", and initialize at any
// other version is refused (Vokeeeeee on X).
const affinityVersion = "2025-11-25"

// refuseOtherVersion is cpp-mcp's handle_initialize answer to a version
// other than its own, or "" for its own.
func refuseOtherVersion(id json.RawMessage, asked string) string {
	if asked == affinityVersion {
		return ""
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Unsupported protocol version","data":{"supported":[%q],"requested":%q}}}`, id, affinityVersion, asked)
}

type affinityMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

// affinityReply is what cpp-mcp answers m with on a session, nil for a
// notification. Requests other than initialize and ping before
// notifications/initialized are refused, as cpp-mcp does.
func affinityReply(m affinityMsg, initialized *bool) []byte {
	switch m.Method {
	case "initialize":
		if r := refuseOtherVersion(m.ID, m.Params.ProtocolVersion); r != "" {
			return []byte(r)
		}
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"Affinity","version":"3.3.3"}}}`, m.ID, affinityVersion))
	case "notifications/initialized":
		*initialized = true
		return nil
	case "tools/list":
		if !*initialized {
			return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32600,"message":"Session not initialized"}}`, m.ID))
		}
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"get_document_info","inputSchema":{"type":"object"}},{"name":"execute_script","inputSchema":{"type":"object"}}]}}`, m.ID))
	}
	if len(m.ID) == 0 {
		return nil
	}
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, m.ID))
}

// affinitySSE is Affinity's server on the older SSE transport, as cpp-mcp
// writes it: CRLF lines, the endpoint relative with ?session_id=, a
// heartbeat event, 202 "Accepted" as text/plain to each post and every
// reply on the stream as a message event.
func affinitySSE(t *testing.T) *httptest.Server {
	type session struct {
		out         chan []byte
		initialized bool
	}
	var mu sync.Mutex
	sessions := map[string]*session{}
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		id := fmt.Sprintf("a%d", n)
		s := &session{out: make(chan []byte, 16)}
		sessions[id] = s
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fmt.Fprintf(w, "event: endpoint\r\ndata: /message?session_id=%s\r\n\r\n", id)
		fmt.Fprint(w, "event: heartbeat\r\ndata: 0\r\n\r\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case b := <-s.out:
				fmt.Fprintf(w, "event: message\r\ndata: %s\r\n\r\n", b)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /message", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s := sessions[r.URL.Query().Get("session_id")]
		mu.Unlock()
		if s == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"Session not found"}`)
			return
		}
		var m affinityMsg
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("post: %v", err)
		}
		mu.Lock()
		b := affinityReply(m, &s.initialized)
		mu.Unlock()
		if b != nil {
			s.out <- b
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(202)
		fmt.Fprint(w, "Accepted")
	})
	// the reporter's literal config: {"type":"sse","url":"http://127.0.0.1:6767/sse"};
	// a free port stands for 6767, which another program may hold
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: mux}}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// affinityHTTP is the same server on streamable HTTP, answering in JSON.
func affinityHTTP(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	initialized := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var m affinityMsg
		json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		b := affinityReply(m, &initialized)
		mu.Unlock()
		if b == nil {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A server that takes one protocol version, other than the one magpie
// asks for, is asked again at the version it names, on every transport
// and by both the check and the hub. Affinity's refused 2025-06-18 and
// the Library showed "bad reply" (Vokeeeeee on X).
func TestServerOfOneVersion(t *testing.T) {
	if mcpProtocol == affinityVersion {
		t.Fatal("magpie asks for Affinity's version already: this test needs another")
	}
	servers := map[string]func(t *testing.T) *Server{
		"sse": func(t *testing.T) *Server {
			srv := affinitySSE(t)
			return &Server{Name: "affinity", Transport: "sse", URL: srv.URL + "/sse"}
		},
		"http": func(t *testing.T) *Server {
			return &Server{Name: "affinity", Transport: "http", URL: affinityHTTP(t).URL + "/mcp"}
		},
		"stdio": func(t *testing.T) *Server {
			return fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_ONLY": affinityVersion})
		},
	}
	for _, tr := range []string{"sse", "http", "stdio"} {
		t.Run(tr+"/check", func(t *testing.T) {
			h := CheckServer(context.Background(), servers[tr](t))
			if h.State != "ok" || h.Tools == 0 {
				t.Fatalf("got %+v, want ok with its tools", h)
			}
		})
		t.Run(tr+"/hub", func(t *testing.T) {
			c, err := dialHub(context.Background(), servers[tr](t), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer c.close()
			m, err := c.ask(context.Background(), "tools/list", map[string]any{})
			if err != nil || m.Error != nil {
				t.Fatalf("tools/list: %v %+v", err, m)
			}
			var r struct {
				Tools []struct{ Name string } `json:"tools"`
			}
			if json.Unmarshal(m.Result, &r) != nil || len(r.Tools) == 0 {
				t.Fatalf("tools/list: %s", m.Result)
			}
		})
	}
}

// A refusal that names no other version is told as it was, once.
func TestServerOfNoVersion(t *testing.T) {
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m affinityMsg
		json.NewDecoder(r.Body).Decode(&m)
		if m.Method == "initialize" {
			asks.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Unsupported protocol version","data":{"supported":[%q]}}}`, m.ID, mcpProtocol)
	}))
	defer srv.Close()
	h := CheckServer(context.Background(), &Server{Name: "s", Transport: "http", URL: srv.URL})
	if h.State != "error" || h.Why != "protocol" || !strings.Contains(h.Detail, "Unsupported protocol version") || asks.Load() != 1 {
		t.Fatalf("got %+v after %d asks, want the refusal after 1", h, asks.Load())
	}
}

func TestVersionAsked(t *testing.T) {
	msg := func(data string) *rpcMsg {
		m := &rpcMsg{}
		json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Unsupported protocol version","data":`+data+`}}`), m)
		return m
	}
	for data, want := range map[string]string{
		`{"supported":["2025-11-25"],"requested":"2025-06-18"}`:  "2025-11-25",
		`{"supported":"2024-11-05"}`:                             "2024-11-05",
		`{"supported":["2024-11-05","2025-03-26","2025-06-18"]}`: "2025-03-26",
		`{"supported":["2025-06-18"]}`:                           "",
		`{"supported":["latest","2025-11-25; drop table"]}`:      "",
		`{"requested":"2025-06-18"}`:                             "",
		`"Unsupported"`:                                          "",
		`null`:                                                   "",
	} {
		if got := versionAsked(msg(data), "2025-06-18"); got != want {
			t.Errorf("%s: got %q, want %q", data, got, want)
		}
	}
	if versionAsked(nil, mcpProtocol) != "" || versionAsked(&rpcMsg{Result: json.RawMessage(`{}`)}, mcpProtocol) != "" {
		t.Error("a reply with no error asks again")
	}
}
