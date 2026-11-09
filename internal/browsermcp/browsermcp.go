// Package browsermcp is `magpie mcp browser`: the browser MCP server
// (gateway.BrowserMCPPath) over stdio, for an agent that starts its MCP
// servers as commands. Each message is passed to the running gateway and
// its answer back; the process is one agent session, which goes on in the
// tab it was working in.
//
// `magpie mcp search` is the web search MCP server (gateway.SearchMCPPath)
// passed on the same way, with no session: the Library's Magpie Web
// Search, which an agent is given as a command as it is Magpie Browser
// (lutra_fs on Discord: the search was an address in Settings alone).
// `magpie mcp vision` is image recognition's (gateway.VisionMCPPath), the
// Library's Magpie Image Recognition (matrix0487 on Discord, #4653).
// `magpie mcp memory` is memory's (gateway.MemoryMCPPath), sent with the
// folder the agent started it in, for the project its memories are kept
// for, and the client name the agent gave at initialize, for which agent
// it is.
package browsermcp

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// Run serves stdin and stdout until stdin ends.
func Run(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("magpie mcp browser takes no arguments")
	}
	return serve(os.Stdin, os.Stdout, gatewayBase())
}

// RunSearch serves the web search MCP server on stdin and stdout until
// stdin ends.
func RunSearch(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("magpie mcp search takes no arguments")
	}
	return serveSearch(os.Stdin, os.Stdout, gatewayBase())
}

// RunVision serves the image recognition MCP server on stdin and stdout
// until stdin ends.
func RunVision(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("magpie mcp vision takes no arguments")
	}
	return serveVision(os.Stdin, os.Stdout, gatewayBase())
}

// RunMemory serves the memory MCP server on stdin and stdout until stdin
// ends.
func RunMemory(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("magpie mcp memory takes no arguments")
	}
	wd, _ := os.Getwd()
	return serveMemory(os.Stdin, os.Stdout, gatewayBase(), wd)
}

func gatewayBase() string {
	base := strings.TrimRight(os.Getenv("MAGPIE_GATEWAY"), "/")
	if base == "" {
		base = gateway.URL()
	}
	return base
}

func session() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "stdio-" + hex.EncodeToString(b)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

func serve(in io.Reader, out io.Writer, base string) error {
	return relay(in, &bridge{base: base, path: gateway.BrowserMCPPath, title: "Magpie Browser", session: session(), client: &http.Client{Timeout: 2 * time.Minute}, out: out})
}

// serveSearch passes each line to the gateway's web search server. A
// search that reads a page through a slow searcher model takes minutes.
func serveSearch(in io.Reader, out io.Writer, base string) error {
	return relay(in, &bridge{base: base, path: gateway.SearchMCPPath, title: "Magpie Web Search", client: &http.Client{Timeout: 5 * time.Minute}, out: out})
}

// serveVision passes each line to the gateway's image recognition server.
// A message may hold an image as a data URL, up to what the gateway takes
// (32 MiB), and a look by a slow model takes minutes.
func serveVision(in io.Reader, out io.Writer, base string) error {
	wd, _ := os.Getwd()
	return relay(in, &bridge{base: base, path: gateway.VisionMCPPath, title: "Magpie Image Recognition", client: &http.Client{Timeout: 5 * time.Minute}, out: out,
		most: 32 << 20, fix: func(b []byte) []byte { return imageFrom(b, wd) }})
}

// serveMemory passes each line to the gateway's memory server, with wd,
// the folder the agent started this in, and the client name its
// initialize gave.
func serveMemory(in io.Reader, out io.Writer, base, wd string) error {
	var mu sync.Mutex
	client := ""
	b := &bridge{base: base, path: gateway.MemoryMCPPath, title: "Magpie Memory", client: &http.Client{Timeout: time.Minute}, out: out}
	b.fix = func(body []byte) []byte {
		// read in order, before the calls after it are passed on
		if name := clientName(body); name != "" {
			mu.Lock()
			client = name
			mu.Unlock()
		}
		return body
	}
	b.header = func(h http.Header) {
		if wd != "" {
			h.Set(gateway.MemoryCwdHeader, wd)
		}
		mu.Lock()
		defer mu.Unlock()
		if client != "" {
			h.Set(gateway.MemoryClientHeader, client)
		}
	}
	return relay(in, b)
}

// clientName is the client's name an initialize message gives, "" for
// any other message.
func clientName(b []byte) string {
	var m struct {
		Method string `json:"method"`
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}
	if json.Unmarshal(b, &m) != nil || m.Method != "initialize" {
		return ""
	}
	n := strings.TrimSpace(m.Params.ClientInfo.Name)
	if len(n) > 64 || strings.ContainsAny(n, "\r\n") {
		return ""
	}
	return n
}

// imageFrom is the message b with analyze_image's image, when it is a path
// relative to the agent's folder, made absolute against wd: the gateway,
// which reads the file, doesn't know the agent's folder, and this process
// was started in it. Any other message, or image, goes as it came.
func imageFrom(b []byte, wd string) []byte {
	var m map[string]json.RawMessage
	if wd == "" || json.Unmarshal(b, &m) != nil || string(m["method"]) != `"tools/call"` {
		return b
	}
	var p map[string]json.RawMessage
	var a map[string]json.RawMessage
	var name, image string
	if json.Unmarshal(m["params"], &p) != nil || json.Unmarshal(p["name"], &name) != nil || name != "analyze_image" ||
		json.Unmarshal(p["arguments"], &a) != nil || json.Unmarshal(a["image"], &image) != nil {
		return b
	}
	image = strings.TrimSpace(image)
	low := strings.ToLower(image)
	if image == "" || filepath.IsAbs(image) || strings.HasPrefix(image, "~") || strings.Contains(low[:min(len(low), 12)], ":") {
		return b // absolute, the home's, or a URL (data:, http:, file:) or a drive (C:)
	}
	a["image"], _ = json.Marshal(filepath.Join(wd, image))
	p["arguments"], _ = json.Marshal(a)
	m["params"], _ = json.Marshal(p)
	out, err := json.Marshal(m)
	if err != nil {
		return b
	}
	return out
}

// callerSession is the browser session a message goes on in: the hub's
// caller's, which magpie's MCP router names in a call's _meta
// (gateway.HubSessionMeta) since it runs this one process for every agent
// behind it, else this process's own. Two agents behind the router then
// work in tabs of their own, as two given this server directly do.
func callerSession(b []byte, own string) string {
	var m struct {
		Method string `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	var v string
	if json.Unmarshal(b, &m) != nil || m.Method != "tools/call" || json.Unmarshal(m.Params.Meta[gateway.HubSessionMeta], &v) != nil {
		return own
	}
	if v = strings.TrimSpace(v); v == "" {
		return own
	}
	return "hub:" + v
}

func relay(in io.Reader, s *bridge) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), max(s.most, 16<<20))
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		body := append([]byte(nil), line...)
		if s.fix != nil {
			body = s.fix(body)
		}
		wg.Add(1)
		// calls run side by side, as an agent may send the next before the
		// last is answered (a ping during a long wait)
		go func() {
			defer wg.Done()
			s.pass(m, body)
		}()
	}
	wg.Wait()
	return sc.Err()
}

// bridge passes one stdio session's messages to the gateway's server at
// path, title naming it when the gateway can't be reached; a session ""
// sends no session header. most is the longest line it reads (16 MiB when
// 0), and fix, when set, makes each message what the gateway is sent;
// header, when set, adds to each request's headers.
type bridge struct {
	base, path, title, session string
	client                     *http.Client
	mu                         sync.Mutex
	out                        io.Writer
	most                       int
	fix                        func([]byte) []byte
	header                     func(http.Header)
}

func (s *bridge) write(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(bytes.TrimSpace(b), '\n'))
}

func (s *bridge) fail(id json.RawMessage, msg string) {
	if len(id) == 0 {
		return
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": msg}})
	s.write(b)
}

func (s *bridge) pass(m message, body []byte) {
	r, err := http.NewRequest(http.MethodPost, s.base+s.path, bytes.NewReader(body))
	if err != nil {
		s.fail(m.ID, err.Error())
		return
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Authorization", "Bearer "+gateway.Token)
	if s.session != "" {
		r.Header.Set(gateway.BrowserSessionHeader, callerSession(body, s.session))
	}
	if s.header != nil {
		s.header(r.Header)
	}
	resp, err := s.client.Do(r)
	if err != nil {
		s.fail(m.ID, "magpie isn't running, so "+s.title+" can't be reached: start magpie ("+err.Error()+")")
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if len(m.ID) == 0 || resp.StatusCode == http.StatusAccepted {
		return
	}
	if resp.StatusCode != http.StatusOK {
		s.fail(m.ID, fmt.Sprintf("magpie answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
		return
	}
	s.write(b)
}
