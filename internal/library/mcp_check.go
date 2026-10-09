package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// The Library page says of each server whether it works, found the only
// way that tells: by connecting to it as an agent would. A command is
// started, asked to initialize and to list its tools, and ended; a URL is
// asked the same over HTTP. Nothing is written anywhere: the check only
// reads the library, and what it found is kept in memory for the session.

// ServerHealth is what a check of one server found.
type ServerHealth struct {
	// State is ok (it answered, with Tools tools), auth (it asks for a
	// sign-in or a key) or error (Why says what went wrong)
	State string `json:"state"`
	Tools int    `json:"tools"`
	// Why is the kind of error, for the page to put in its own words:
	// notfound, start, exited, timeout, http, refused, unreachable,
	// protocol, novar (a ${NAME} magpie's environment hasn't: Detail names
	// them)
	Why    string `json:"why,omitempty"`
	Code   int    `json:"code,omitempty"`   // the exit code, or the HTTP status
	Detail string `json:"detail,omitempty"` // the last line it wrote to stderr, or the error's text
	// OAuth is a refusal that named a sign-in (WWW-Authenticate), which
	// magpie can do itself for a streamable HTTP server
	OAuth bool  `json:"oauth,omitempty"`
	At    int64 `json:"at"` // when it was checked, unix ms
}

// checkTimeout is how long a server has to start and list its tools: npx
// fetching a package the first time can take a while.
var checkTimeout = 15 * time.Second

// checkAtOnce is how many servers are checked together, each a process or
// a few requests.
const checkAtOnce = 4

const mcpProtocol = "2025-06-18"

var checkClient = &http.Client{Transport: checkTransport()}

func checkTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// checked is the last check of each server, by name, with what the
// server was then: an edited server, or one signed in to since, is checked
// again.
var checked = struct {
	sync.Mutex
	m map[string]checkedServer
}{m: map[string]checkedServer{}}

type checkedServer struct {
	key string
	h   ServerHealth
}

func healthKey(s *Server) string {
	c := *s
	c.Name, c.Agents = "", nil
	b, _ := json.Marshal(c)
	st := mcpauth.StatusOf(s.Name, s.URL)
	return fmt.Sprintf("%s|%v|%v|%d", b, st.SignedIn, st.Dead, st.At)
}

// CheckServers checks the library's servers called names (every one when
// there are none). One checked already this session, as it is now, isn't
// started again unless fresh.
func CheckServers(ctx context.Context, names []string, fresh bool) (map[string]ServerHealth, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	var list []*Server
	if len(names) == 0 {
		list = l.MCP
	} else {
		for _, n := range names {
			if s := l.server(n); s != nil {
				list = append(list, s)
			}
		}
	}
	out := map[string]ServerHealth{}
	var outMu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, checkAtOnce)
	for _, s := range list {
		key := healthKey(s)
		checked.Lock()
		c, ok := checked.m[s.Name]
		checked.Unlock()
		if ok && c.key == key && !fresh {
			outMu.Lock()
			out[s.Name] = c.h
			outMu.Unlock()
			continue
		}
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			h := CheckServer(ctx, s)
			if ctx.Err() == nil { // a check cut short by the page going away isn't one
				checked.Lock()
				checked.m[s.Name] = checkedServer{key, h}
				checked.Unlock()
			}
			outMu.Lock()
			out[s.Name] = h
			outMu.Unlock()
		}(s)
	}
	wg.Wait()
	return out, nil
}

// CheckServer connects to one server and asks it for its tools.
func CheckServer(ctx context.Context, s *Server) ServerHealth {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	var h ServerHealth
	switch s.Transport {
	case "stdio":
		h = checkStdio(ctx, s)
	case "sse":
		h = checkSSE(ctx, s)
	default:
		h = checkHTTP(ctx, s)
	}
	h.At = time.Now().UnixMilli()
	return h
}

func failed(why, detail string) ServerHealth {
	return ServerHealth{State: "error", Why: why, Detail: detail}
}

// timedOut is a server that didn't answer in checkTimeout; the page says
// how long that is.
func timedOut() ServerHealth { return failed("timeout", "") }

// rpcMsg is a JSON-RPC message from the server: a reply has an id.
type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m *rpcMsg) is(id int) bool { return string(bytes.TrimSpace(m.ID)) == fmt.Sprint(id) }

func request(id int, method string, params any) []byte {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return b
}

func initializeReq() []byte {
	return request(1, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "magpie", "version": gateway.Version},
	})
}

var initializedNote = []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

// toolsOf is the health the reply to tools/list says: a server with no
// tools may not know the method at all.
func toolsOf(m *rpcMsg) ServerHealth {
	if m.Error != nil {
		if m.Error.Code == -32601 {
			return ServerHealth{State: "ok"}
		}
		return failed("protocol", "tools/list: "+m.Error.Message)
	}
	var r struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(m.Result, &r); err != nil {
		return failed("protocol", "tools/list: "+err.Error())
	}
	return ServerHealth{State: "ok", Tools: len(r.Tools)}
}

// initError is what an error reply to initialize says.
func initError(m *rpcMsg) *ServerHealth {
	if m.Error == nil {
		return nil
	}
	h := failed("protocol", "initialize: "+m.Error.Message)
	return &h
}

// ---- a command ---------------------------------------------------------------

// tail keeps the end of what a server writes to stderr: its last line is
// usually why it stopped.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300] + "…"
	}
	return l
}

func checkStdio(ctx context.Context, s *Server) ServerHealth {
	// started as the agents start it: in the environment magpie has, with
	// the server's own variables over it
	env, unset := expandAll(s.Env)
	if len(unset) > 0 {
		return failed("novar", strings.Join(unset, ", "))
	}
	cmd := proc.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failed("start", err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return failed("start", err.Error())
	}
	errs := &tail{}
	cmd.Stderr = errs
	tree, err := proc.StartTree(cmd)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return failed("notfound", s.Command)
		}
		return failed("start", err.Error())
	}
	var exitErr error
	exited := make(chan struct{})
	go func() { exitErr = tree.Wait(); close(exited) }()
	// the server ends with the check, and whatever it started with it
	defer func() {
		stdin.Close()
		tree.Kill()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
		}
	}()

	msgs := make(chan *rpcMsg, 16)
	go func() {
		defer close(msgs)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			var m rpcMsg
			// a line that isn't JSON-RPC is a server logging to the wrong
			// stream; agents pass over it too
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			select {
			case msgs <- &m:
			case <-ctx.Done():
				return
			}
		}
	}()
	send := func(b []byte) error {
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	// a reply, or why there is none: the process ended or time ran out. A
	// process that ended may still have lines to read, the reply among
	// them, so its output is read to the end first.
	out, gone, ended := msgs, exited, false
	await := func(id int) (*rpcMsg, *ServerHealth) {
		for {
			select {
			case m, ok := <-out:
				if !ok {
					out = nil
					if ended {
						h := exitHealth(exitErr, errs)
						return nil, &h
					}
					continue
				}
				if m.is(id) {
					return m, nil
				}
			case <-gone:
				gone, ended = nil, true
				if out == nil {
					h := exitHealth(exitErr, errs)
					return nil, &h
				}
			case <-ctx.Done():
				h := timedOut()
				h.Detail = errs.last()
				return nil, &h
			}
		}
	}
	if err := send(initializeReq()); err != nil {
		if _, h := await(1); h != nil {
			return *h
		}
		return failed("start", err.Error())
	}
	m, h := await(1)
	if h != nil {
		return *h
	}
	if h := initError(m); h != nil {
		return *h
	}
	_ = send(initializedNote)
	_ = send(request(2, "tools/list", nil))
	m, h = await(2)
	if h != nil {
		return *h
	}
	return toolsOf(m)
}

func exitHealth(err error, errs *tail) ServerHealth {
	h := failed("exited", errs.last())
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		h.Code = ee.ExitCode()
	}
	return h
}

// ---- a URL -------------------------------------------------------------------

// remoteHeaders are the server's own headers, with magpie's token when it
// is signed in to it: the agents reach it with that too.
func remoteHeaders(ctx context.Context, s *Server) (http.Header, *ServerHealth) {
	h := http.Header{}
	headers, unset := expandAll(s.Headers)
	if len(unset) > 0 {
		f := failed("novar", strings.Join(unset, ", "))
		return nil, &f
	}
	for k, v := range headers {
		h.Set(k, v)
	}
	if s.Transport == "http" && mcpauth.SignedIn(s.Name, s.URL) {
		tok, err := mcpauth.Token(ctx, s.Name)
		if err != nil {
			return nil, &ServerHealth{State: "auth", OAuth: true, Detail: err.Error()}
		}
		h.Set("Authorization", "Bearer "+tok)
	}
	return h, nil
}

// netHealth puts a failed request in the page's words.
func netHealth(ctx context.Context, err error) ServerHealth {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return timedOut()
	}
	if errors.Is(err, connectionRefused) {
		return failed("refused", err.Error())
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return failed("unreachable", "can't find "+dns.Name)
	}
	return failed("unreachable", err.Error())
}

// refused is the health of a 401 or 403: the server wants a sign-in, or a
// key it wasn't given.
func refused(res *http.Response) ServerHealth {
	return ServerHealth{State: "auth", Code: res.StatusCode, OAuth: res.Header.Get("WWW-Authenticate") != ""}
}

func statusHealth(res *http.Response) ServerHealth {
	h := failed("http", res.Status)
	h.Code = res.StatusCode
	return h
}

func checkHTTP(ctx context.Context, s *Server) ServerHealth {
	hdr, bad := remoteHeaders(ctx, s)
	if bad != nil {
		return *bad
	}
	session, version := "", mcpProtocol
	post := func(body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
			req.Header.Set("MCP-Protocol-Version", version)
		}
		return checkClient.Do(req)
	}
	// call posts a request and reads its reply, as JSON or from an event
	// stream
	call := func(id int, body []byte) (*rpcMsg, *ServerHealth) {
		res, err := post(body)
		if err != nil {
			h := netHealth(ctx, err)
			return nil, &h
		}
		defer res.Body.Close()
		if res.StatusCode == 401 || res.StatusCode == 403 {
			h := refused(res)
			return nil, &h
		}
		if res.StatusCode/100 != 2 {
			h := statusHealth(res)
			return nil, &h
		}
		if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
			session = sid
		}
		m, err := replyOf(res, id)
		if err != nil {
			if ctx.Err() != nil {
				h := timedOut()
				return nil, &h
			}
			h := failed("protocol", err.Error())
			return nil, &h
		}
		return m, nil
	}
	m, h := call(1, initializeReq())
	if h != nil {
		return *h
	}
	if h := initError(m); h != nil {
		return *h
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(m.Result, &init) == nil && init.ProtocolVersion != "" {
		version = init.ProtocolVersion
	}
	if session != "" {
		defer endSession(s.URL, hdr, session)
	}
	if res, err := post(initializedNote); err == nil {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
	}
	m, h = call(2, request(2, "tools/list", nil))
	if h != nil {
		return *h
	}
	return toolsOf(m)
}

// endSession tells the server the check is done with its session, as a
// client leaving should; one that doesn't take it is no matter.
func endSession(u string, hdr http.Header, session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Mcp-Session-Id", session)
	if res, err := checkClient.Do(req); err == nil {
		res.Body.Close()
	}
}

// replyOf is the reply to id in a response: its JSON body, or the event
// stream's message that carries it.
func replyOf(res *http.Response, id int) (*rpcMsg, error) {
	ct := res.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		done := make(chan struct{})
		defer close(done)
		for e := range sseEvents(res.Body, done) {
			if e.name != "" && e.name != "message" {
				continue
			}
			var m rpcMsg
			if json.Unmarshal([]byte(e.data), &m) == nil && m.is(id) {
				return &m, nil
			}
		}
		return nil, errors.New("the event stream ended without a reply")
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var m rpcMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not an MCP reply (%s): %s", ct, snippet(b))
	}
	return &m, nil
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

type sseEvent struct{ name, data string }

// sseEvents reads an event stream's events until it ends.
// done ends the reading once the events are no longer wanted.
func sseEvents(r io.Reader, done <-chan struct{}) <-chan sseEvent {
	out := make(chan sseEvent)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		var e sseEvent
		var data []string
		for sc.Scan() {
			l := strings.TrimSuffix(sc.Text(), "\r")
			switch {
			case l == "":
				if len(data) > 0 {
					e.data = strings.Join(data, "\n")
					select {
					case out <- e:
					case <-done:
						return
					}
				}
				e, data = sseEvent{}, nil
			case strings.HasPrefix(l, "event:"):
				e.name = strings.TrimSpace(l[6:])
			case strings.HasPrefix(l, "data:"):
				data = append(data, strings.TrimPrefix(l[5:], " "))
			}
		}
	}()
	return out
}

// checkSSE checks a server on the older SSE transport: its stream names
// where to post, and the replies come back on the stream.
func checkSSE(ctx context.Context, s *Server) ServerHealth {
	hdr, bad := remoteHeaders(ctx, s)
	if bad != nil {
		return *bad
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return failed("unreachable", err.Error())
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := checkClient.Do(req)
	if err != nil {
		return netHealth(ctx, err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return refused(res)
	}
	if res.StatusCode/100 != 2 {
		return statusHealth(res)
	}
	events := sseEvents(res.Body, ctx.Done())
	next := func() (sseEvent, *ServerHealth) {
		select {
		case e, ok := <-events:
			if !ok {
				h := timedOut()
				if ctx.Err() == nil {
					h = failed("protocol", "the event stream ended")
				}
				return e, &h
			}
			return e, nil
		case <-ctx.Done():
			h := timedOut()
			return sseEvent{}, &h
		}
	}
	var endpoint *url.URL
	for endpoint == nil {
		e, h := next()
		if h != nil {
			return *h
		}
		if e.name == "endpoint" {
			base, _ := url.Parse(s.URL)
			endpoint, err = base.Parse(strings.TrimSpace(e.data))
			if err != nil {
				return failed("protocol", "endpoint: "+err.Error())
			}
		}
	}
	post := func(body []byte) *ServerHealth {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			h := failed("protocol", err.Error())
			return &h
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := checkClient.Do(req)
		if err != nil {
			h := netHealth(ctx, err)
			return &h
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if res.StatusCode == 401 || res.StatusCode == 403 {
			h := refused(res)
			return &h
		}
		if res.StatusCode/100 != 2 {
			h := statusHealth(res)
			return &h
		}
		return nil
	}
	await := func(id int) (*rpcMsg, *ServerHealth) {
		for {
			e, h := next()
			if h != nil {
				return nil, h
			}
			var m rpcMsg
			if json.Unmarshal([]byte(e.data), &m) == nil && m.is(id) {
				return &m, nil
			}
		}
	}
	if h := post(initializeReq()); h != nil {
		return *h
	}
	m, h := await(1)
	if h != nil {
		return *h
	}
	if h := initError(m); h != nil {
		return *h
	}
	_ = post(initializedNote)
	if h := post(request(2, "tools/list", nil)); h != nil {
		return *h
	}
	m, h = await(2)
	if h != nil {
		return *h
	}
	return toolsOf(m)
}
