package library

// A server run by a command is given to Codex, while Codex is on magpie,
// as the gateway's /mcp/run/<name>, which magpie serves over streamable
// HTTP by running the command itself. Codex starts every server of its
// config once for each thread it has loaded, and keeps them for as long
// as the thread is: 204 node processes, 21 GB, beside one user's Codex
// (#3885). Here a thread's session starts its server only when the thread
// calls on it, and ends it once the session has been idle for runIdle;
// its initialize and its first lists are answered, after the first
// session's, from what the server said then. A session that is in use
// has a server of its own, as it had: what a server keeps for its client
// (a browser, a page) is still kept per thread.
//
// Codex not on magpie, in WSL, or reaching the gateway with a key of its
// own (another machine), a server with a variable magpie hasn't, and one
// the user keeps direct (Server.Hub), are given the command as before: a
// direct one reads its project from the folder it starts in, which Codex
// makes the thread's (codex-mcp's LocalStdioServerLauncher, with no `cwd`
// in the entry), and magpie's would be home (#4382, cuipengcx90).

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

// RunPath is where the gateway serves a server magpie runs for Codex.
const RunPath = "/mcp/run/"

// runIdle is how long a session's server is kept with nothing asked of it.
var runIdle = 15 * time.Minute

// runSpec is the command a server is run with, its variables expanded.
type runSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func (r runSpec) key() string { b, _ := json.Marshal(r); return string(b) }

// specOf is the command magpie runs s with, false for a server it doesn't
// run: a remote one, or one with a variable magpie's environment hasn't.
func specOf(s *Server) (runSpec, bool) {
	if s.Remote() || s.Command == "" {
		return runSpec{}, false
	}
	env, unset := expandAll(s.Env)
	if len(unset) > 0 {
		return runSpec{}, false
	}
	cmd, args := launchCmd(s, "")
	return runSpec{Command: cmd, Args: slices.Clone(args), Env: env}, true
}

var runs struct {
	sync.Mutex
	sessions map[string]*runSession
	// given are the servers the last sync gave Codex this way, by name
	given map[string]runSpec
	// wired is, by agent, whether the last sync gave it servers this way
	wired map[string]bool
	// said is what servers said, by their spec: initialize's result for
	// each protocol version asked, and the first page of each list
	said    map[string]map[string]json.RawMessage
	janitor sync.Once
}

// runsFor says whether t's agent is given servers through the gateway at
// base: Codex on this machine, on magpie, reaching the gateway as this
// machine does.
func runsFor(t *Target, base string) bool {
	return runOn && t.MCP != nil && t.MCP.Format == fmtCodex && !t.MCP.WSL && t.Agent != nil &&
		t.Agent.WSL == "" && agent.KeyAt(base) == gateway.Token && runWired(t.Agent)
}

// runWired says the agent is on magpie, which it is given servers through
// only while it is; a var for tests.
var runWired = func(a *agent.Agent) bool { return a.Wired() && !a.Disconnected() }

// runOn, off in the package's tests that aren't about it.
var runOn = true

func init() {
	gateway.RunMCP = ServeRun
	// a Codex connected or disconnected is given its servers the other
	// way: through the gateway on magpie, by their commands off it
	agent.Rewired = func(id string) {
		if !runOn {
			return
		}
		for _, t := range Targets() {
			if t.Agent == nil || t.Agent.ID != id || t.MCP == nil || t.MCP.Format != fmtCodex {
				continue
			}
			runs.Lock()
			was, seen := runs.wired[id]
			runs.Unlock()
			if !seen || was != runsFor(t, gatewayOf(t)) {
				go func() {
					if _, err := Sync(); err != nil {
						log.Printf("library: syncing %s's MCP servers: %v", id, err)
					}
				}()
			}
			return
		}
	}
}

// runVia is s as an agent that runsFor is given it, and the spec it is
// run with; false for one magpie doesn't run.
func runVia(s *Server, base string) (*Server, runSpec, bool) {
	spec, ok := specOf(s)
	if !ok {
		return s, runSpec{}, false
	}
	c := *s
	c.Transport, c.Command, c.Args, c.Env = "http", "", nil, nil
	c.URL = strings.TrimRight(base, "/") + RunPath + url.PathEscape(s.Name)
	return &c, spec, true
}

// runGiven records the servers a sync gave through the gateway, and ends
// the servers of any session whose server is given no longer, or with
// another command: the next call starts it as it is now.
func runGiven(given map[string]runSpec) {
	if given == nil {
		given = map[string]runSpec{}
	}
	runs.Lock()
	runs.given = given
	var stop []*runSession
	for _, s := range runs.sessions {
		stop = append(stop, s)
	}
	runs.Unlock()
	for _, s := range stop {
		spec, ok := given[s.name]
		s.mu.Lock()
		changed := !ok || spec.key() != s.spec.key()
		if changed {
			s.spec, s.gone = spec, !ok
		}
		s.mu.Unlock()
		if changed {
			s.stop()
		}
	}
}

// runSpecOf is the spec of the server called name: as the last sync gave
// it, or, before a sync has, as the library has it.
func runSpecOf(name string) (runSpec, bool) {
	runs.Lock()
	given := runs.given
	runs.Unlock()
	if given != nil {
		spec, ok := given[name]
		return spec, ok
	}
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return runSpec{}, false
	}
	s := l.server(name)
	if s == nil || !slices.Contains(s.Agents, "codex") || s.Hub == HubDirect {
		return runSpec{}, false
	}
	return specOf(npxDirect(s))
}

type runSession struct {
	id, name string
	start    sync.Mutex // held while the server starts

	mu      sync.Mutex
	spec    runSpec
	gone    bool            // the server is given no longer
	init    json.RawMessage // the session's initialize params
	child   *runChild
	last    time.Time
	busy    int
	streams []chan []byte // the event streams open, newest last
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// session is the session sid of name, made with init when there is none:
// one magpie was restarted under is taken up as it is.
func session(sid, name string, spec runSpec, init json.RawMessage) *runSession {
	runs.Lock()
	defer runs.Unlock()
	if s := runs.sessions[sid]; s != nil && s.name == name {
		return s
	}
	if runs.sessions == nil {
		runs.sessions = map[string]*runSession{}
	}
	s := &runSession{id: sid, name: name, spec: spec, init: init, last: time.Now()}
	runs.sessions[sid] = s
	runs.janitor.Do(func() { go runJanitor() })
	return s
}

// runJanitor ends the servers of idle sessions, and forgets sessions a
// day idle.
func runJanitor() {
	for {
		time.Sleep(runIdle / 15)
		runSweep(time.Now())
	}
}

func runSweep(now time.Time) {
	runs.Lock()
	var idle []*runSession
	for id, s := range runs.sessions {
		s.mu.Lock()
		quiet := now.Sub(s.last)
		if s.child != nil && s.busy == 0 && quiet > runIdle {
			idle = append(idle, s)
		}
		if s.child == nil && s.busy == 0 && len(s.streams) == 0 && quiet > 24*time.Hour {
			delete(runs.sessions, id)
		}
		s.mu.Unlock()
	}
	runs.Unlock()
	for _, s := range idle {
		s.stop()
	}
}

// stop ends the session's server, if it has one running.
func (s *runSession) stop() {
	s.mu.Lock()
	c := s.child
	s.child = nil
	s.mu.Unlock()
	if c != nil {
		c.end()
	}
}

// running is the session's server while it runs.
func (s *runSession) running() *runChild {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child != nil && s.child.ended() {
		s.child = nil
	}
	return s.child
}

// ensure is the session's server, started if it isn't running, and what
// it answered the session's initialize with when it was started now.
func (s *runSession) ensure() (*runChild, json.RawMessage, error) {
	s.start.Lock()
	defer s.start.Unlock()
	if c := s.running(); c != nil {
		return c, nil, nil
	}
	s.mu.Lock()
	spec, init, gone := s.spec, s.init, s.gone
	s.mu.Unlock()
	if gone {
		return nil, nil, fmt.Errorf("magpie gives Codex no server called %s now", s.name)
	}
	c, res, err := runStart(s, spec, init)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	s.child = c
	s.mu.Unlock()
	return c, res, nil
}

// deliver passes a message of the server's that isn't a reply to the
// newest stream open; a request with none open is refused, as it would
// wait for an answer that never comes.
func (s *runSession) deliver(c *runChild, line []byte, id json.RawMessage, method string) {
	switch method {
	case "notifications/tools/list_changed":
		forget(s.spec, "tools/list")
	case "notifications/prompts/list_changed":
		forget(s.spec, "prompts/list")
	case "notifications/resources/list_changed":
		forget(s.spec, "resources/list", "resources/templates/list")
	}
	s.mu.Lock()
	var to chan []byte
	if n := len(s.streams); n > 0 {
		to = s.streams[n-1]
	}
	s.mu.Unlock()
	if to != nil {
		select {
		case to <- line:
			return
		default:
		}
	}
	if len(id) > 0 {
		_ = c.send(rpcError(id, -32603, "the client has no stream open for this request"))
	}
}

func (s *runSession) open() chan []byte {
	ch := make(chan []byte, 64)
	s.mu.Lock()
	s.streams = append(s.streams, ch)
	s.mu.Unlock()
	return ch
}

func (s *runSession) close(ch chan []byte) {
	s.mu.Lock()
	if i := slices.Index(s.streams, ch); i >= 0 {
		s.streams = slices.Delete(s.streams, i, i+1)
	}
	s.mu.Unlock()
}

// runChild is a server magpie started for a session.
type runChild struct {
	tree  *proc.Tree
	in    io.WriteCloser
	errs  *tail
	wmu   sync.Mutex
	mu    sync.Mutex
	waits map[string]chan json.RawMessage
	done  chan struct{}
}

func (c *runChild) ended() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *runChild) end() {
	c.in.Close()
	c.tree.Kill()
}

func (c *runChild) send(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.in.Write(append(slices.Clip(b), '\n'))
	return err
}

// wait is where the reply to id comes; closed with none when the server
// ends.
func (c *runChild) wait(id json.RawMessage) chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.waits[idKey(id)] = ch
	c.mu.Unlock()
	if c.ended() {
		c.unwait(id)
	}
	return ch
}

func (c *runChild) unwait(id json.RawMessage) {
	c.mu.Lock()
	if ch, ok := c.waits[idKey(id)]; ok {
		delete(c.waits, idKey(id))
		close(ch)
	}
	c.mu.Unlock()
}

func idKey(id json.RawMessage) string { return string(bytes.TrimSpace(id)) }

const runInitID = `"magpie-run-init"`

// runStart starts the session's server and initializes it with the
// session's own params, as Codex would have.
func runStart(s *runSession, spec runSpec, init json.RawMessage) (*runChild, json.RawMessage, error) {
	prog, found := stdioProgram(spec.Command)
	cmd := proc.Command(prog, spec.Args...)
	// as checkStdioAt starts it: from home, in magpie's environment with
	// the server's own variables over it
	cmd.Env = os.Environ()
	if d := home(); d != "" {
		cmd.Dir = d
		if runtime.GOOS != "windows" {
			cmd.Env = append(cmd.Env, "PWD="+d)
		}
	}
	if found != "" {
		cmd.Env = withPath(cmd.Env, found)
	}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	errs := &tail{}
	cmd.Stderr = errs
	tree, err := proc.StartTree(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("couldn't start %s: %w", spec.Command, err)
	}
	c := &runChild{tree: tree, in: in, errs: errs, waits: map[string]chan json.RawMessage{}, done: make(chan struct{})}
	go func() {
		err := tree.Wait()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			if t := errs.last(); t != "" {
				log.Printf("library: MCP server %s for Codex ended: %v: %s", s.name, err, t)
			}
		}
	}()
	go func() {
		defer func() {
			close(c.done)
			c.mu.Lock()
			for k, ch := range c.waits {
				delete(c.waits, k)
				close(ch)
			}
			c.mu.Unlock()
		}()
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 64<<20)
		for sc.Scan() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			// a line that isn't JSON-RPC is a server logging to the
			// wrong stream; agents pass over it too
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			line := slices.Clone(sc.Bytes())
			if m.Method == "" && len(m.ID) > 0 {
				c.mu.Lock()
				ch, ok := c.waits[idKey(m.ID)]
				delete(c.waits, idKey(m.ID))
				c.mu.Unlock()
				if ok {
					ch <- line
					close(ch)
				}
				continue
			}
			s.deliver(c, line, m.ID, m.Method)
		}
	}()
	id := json.RawMessage(runInitID)
	ch := c.wait(id)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "initialize", "params": init})
	if err := c.send(req); err != nil {
		c.end()
		return nil, nil, fmt.Errorf("couldn't start %s: %w", spec.Command, err)
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	var reply json.RawMessage
	select {
	case reply = <-ch:
	case <-timer.C:
	}
	var m struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if reply == nil || json.Unmarshal(reply, &m) != nil || m.Error != nil || len(m.Result) == 0 {
		c.end()
		why := "it didn't answer initialize"
		switch {
		case m.Error != nil:
			why = "initialize: " + m.Error.Message
		case c.ended() && errs.last() != "":
			why = "it ended: " + errs.last()
		}
		return nil, nil, fmt.Errorf("couldn't start %s: %s", s.name, why)
	}
	_ = c.send(initializedNote)
	return c, m.Result, nil
}

// runLists are the lists whose first page is answered from what a server
// said before.
var runLists = []string{"tools/list", "prompts/list", "resources/list", "resources/templates/list"}

func said(spec runSpec, what string) json.RawMessage {
	runs.Lock()
	defer runs.Unlock()
	return runs.said[spec.key()][what]
}

func remember(spec runSpec, what string, v json.RawMessage) {
	runs.Lock()
	defer runs.Unlock()
	if runs.said == nil {
		runs.said = map[string]map[string]json.RawMessage{}
	}
	m := runs.said[spec.key()]
	if m == nil {
		m = map[string]json.RawMessage{}
		runs.said[spec.key()] = m
	}
	m[what] = v
}

func forget(spec runSpec, what ...string) {
	runs.Lock()
	defer runs.Unlock()
	for _, w := range what {
		delete(runs.said[spec.key()], w)
	}
}

func rpcResult(id, result json.RawMessage) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return b
}

func rpcError(id json.RawMessage, code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	return b
}

// rpcIn is a message from the client.
type rpcIn struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// ServeRun is the gateway's RunPath<name>: the server called name, run by
// magpie, over streamable HTTP. The gateway lets only this machine's
// agents here.
func ServeRun(w http.ResponseWriter, r *http.Request, name string) {
	sid := r.Header.Get("Mcp-Session-Id")
	switch r.Method {
	case http.MethodDelete:
		runs.Lock()
		s := runs.sessions[sid]
		delete(runs.sessions, sid)
		runs.Unlock()
		if s != nil {
			s.stop()
		}
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		runStream(w, r, name, sid)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > 32<<20 {
		http.Error(w, "the message is over 32 MB", http.StatusRequestEntityTooLarge)
		return
	}
	var m rpcIn
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '[' {
		http.Error(w, "magpie takes one JSON-RPC message a request", http.StatusBadRequest)
		return
	}
	if json.Unmarshal(body, &m) != nil {
		http.Error(w, "the body isn't a JSON-RPC message", http.StatusBadRequest)
		return
	}
	var line bytes.Buffer
	if json.Compact(&line, body) != nil {
		http.Error(w, "the body isn't JSON", http.StatusBadRequest)
		return
	}
	spec, ok := runSpecOf(name)
	if !ok {
		http.Error(w, "magpie gives Codex no server called "+name, http.StatusNotFound)
		return
	}
	if m.Method == "initialize" {
		runInitialize(w, name, spec, m)
		return
	}
	if sid == "" {
		http.Error(w, "no Mcp-Session-Id: initialize first", http.StatusBadRequest)
		return
	}
	s := session(sid, name, spec, defaultInit())
	w.Header().Set("Mcp-Session-Id", sid)
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	if len(m.ID) == 0 || m.Method == "" {
		// a notification, or the client's answer to the server's request:
		// for the server, if it runs (it is told it is initialized when
		// magpie starts it)
		if c := s.running(); c != nil && m.Method != "notifications/initialized" {
			_ = c.send(line.Bytes())
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if m.Method == "ping" {
		runReply(w, rpcResult(m.ID, json.RawMessage("{}")))
		return
	}
	first := slices.Contains(runLists, m.Method) && !hasCursor(m.Params)
	if first {
		if v := said(spec, m.Method); v != nil {
			runReply(w, rpcResult(m.ID, v))
			return
		}
	}
	s.mu.Lock()
	s.busy++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy--
		s.last = time.Now()
		s.mu.Unlock()
	}()
	c, _, err := s.ensure()
	if err != nil {
		runReply(w, rpcError(m.ID, -32603, err.Error()))
		return
	}
	reply := c.wait(m.ID)
	if err := c.send(line.Bytes()); err != nil {
		c.unwait(m.ID)
		runReply(w, rpcError(m.ID, -32603, "the server "+name+" ended"))
		return
	}
	got := func(b json.RawMessage) {
		if !first {
			return
		}
		var r struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &r) == nil && len(r.Result) > 0 {
			remember(spec, m.Method, r.Result)
		}
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		select {
		case b, ok := <-reply:
			if !ok {
				b = rpcError(m.ID, -32603, "the server "+name+" ended")
			}
			got(b)
			runReply(w, b)
		case <-r.Context().Done():
			c.unwait(m.ID)
			cancelled(c, m.ID)
		}
		return
	}
	events := s.open()
	defer s.close(events)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flush(w)
	for {
		select {
		case b := <-events:
			writeEvent(w, b)
		case b, ok := <-reply:
			if !ok {
				b = rpcError(m.ID, -32603, "the server "+name+" ended")
			}
			got(b)
			writeEvent(w, b)
			return
		case <-r.Context().Done():
			c.unwait(m.ID)
			cancelled(c, m.ID)
			return
		}
	}
}

// runInitialize starts a session: answered from what the server said to
// an initialize of the same protocol version before, else by starting it.
func runInitialize(w http.ResponseWriter, name string, spec runSpec, m rpcIn) {
	var p struct {
		Version string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(m.Params, &p)
	sid := newSessionID()
	s := session(sid, name, spec, m.Params)
	w.Header().Set("Mcp-Session-Id", sid)
	what := "initialize " + p.Version
	if v := said(spec, what); v != nil {
		runReply(w, rpcResult(m.ID, v))
		return
	}
	s.mu.Lock()
	s.busy++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy--
		s.last = time.Now()
		s.mu.Unlock()
	}()
	_, res, err := s.ensure()
	if err != nil {
		runReply(w, rpcError(m.ID, -32603, err.Error()))
		return
	}
	if res != nil {
		remember(spec, what, res)
	} else if res = said(spec, what); res == nil {
		runReply(w, rpcError(m.ID, -32603, "the server "+name+" was started already"))
		return
	}
	runReply(w, rpcResult(m.ID, res))
}

// runStream is a GET's event stream: the server's messages while no
// request's stream is open.
func runStream(w http.ResponseWriter, r *http.Request, name, sid string) {
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "a GET here is an event stream", http.StatusNotAcceptable)
		return
	}
	spec, ok := runSpecOf(name)
	if !ok || sid == "" {
		http.Error(w, "no session", http.StatusNotFound)
		return
	}
	s := session(sid, name, spec, defaultInit())
	events := make(chan []byte, 64)
	s.mu.Lock()
	// below the requests' streams, which are newer
	s.streams = slices.Insert(s.streams, 0, events)
	s.mu.Unlock()
	defer s.close(events)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Mcp-Session-Id", sid)
	w.WriteHeader(http.StatusOK)
	flush(w)
	for {
		select {
		case b := <-events:
			writeEvent(w, b)
		case <-r.Context().Done():
			return
		}
	}
}

// defaultInit is the initialize params a session magpie didn't see start
// (one from before a restart) starts its server with.
func defaultInit() json.RawMessage {
	var m struct {
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(initializeReq(), &m)
	return m.Params
}

func hasCursor(params json.RawMessage) bool {
	var p struct {
		Cursor *string `json:"cursor"`
	}
	return json.Unmarshal(params, &p) == nil && p.Cursor != nil
}

func cancelled(c *runChild, id json.RawMessage) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id, "reason": "the client went"}})
	_ = c.send(b)
}

func runReply(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func writeEvent(w http.ResponseWriter, b []byte) {
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
	flush(w)
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
