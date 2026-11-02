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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/wslrun"
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
	// ToolList is each tool's name and what it says it does, as tools/list
	// gave them, for the page to list (nf1qaz2w on Discord); at most
	// maxToolList, while Tools counts every one
	ToolList []Tool `json:"toolList,omitempty"`
	// Why is the kind of error, for the page to put in its own words:
	// notfound, start, exited, timeout, http, refused, unreachable,
	// protocol, novar (a ${NAME} magpie's environment hasn't: Detail names
	// them), wsldown (a command only agents in WSL start, whose distros,
	// Detail, aren't running: it wasn't checked)
	Why    string `json:"why,omitempty"`
	Code   int    `json:"code,omitempty"`   // the exit code, or the HTTP status
	Detail string `json:"detail,omitempty"` // the last line it wrote to stderr, or the error's text
	// OAuth is a refusal that named a sign-in (WWW-Authenticate), which
	// magpie can do itself for a streamable HTTP server
	OAuth bool `json:"oauth,omitempty"`
	// SignIn is magpie's sign-in to a streamable HTTP server as the check
	// left it: one whose renewed token the server refused has run out
	// since, and the page's tag says so (新v学员 on Discord)
	SignIn *mcpauth.Status `json:"signIn,omitempty"`
	// AgentSignIn are the agents signed in to a server that refused
	// magpie's check on their own (Claude Code's /mcp, mcp_agent_signin.go),
	// by id: they use it, though the check, without their tokens, was asked
	// to sign in (CyberCaius on Discord)
	AgentSignIn []string `json:"agentSignIn,omitempty"`
	// Step is what a server that timed out was asked and didn't answer
	// (initialize, notifications/initialized, tools/list, or connect: an
	// SSE server's stream), and Waited how long it was waited for, in ms
	Step   string `json:"step,omitempty"`
	Waited int64  `json:"waited,omitempty"`
	// Checked are where a command was started, in order, for one that agents
	// in WSL have (mcp_check_wsl.go): "" for this machine, a distro's name
	// for its agents there. A failure is the last one's; none for a
	// command only this machine's agents start.
	Checked []string `json:"checked,omitempty"`
	At      int64    `json:"at"` // when it was checked, unix ms
}

// Each step of a check (initialize, notifications/initialized, tools/list)
// has a budget of its own, not one for the whole check: a remote server
// that is slow but healthy took 0.6-12s to initialize and 38-53s to list
// its tools (#1467, xiaozhu1337), which a 15s budget for all of it always
// called "no answer". A command gets less: it runs on this machine, though
// npx fetching a package the first time can take a while.
var (
	stdioStep  = 30 * time.Second
	remoteStep = 60 * time.Second
)

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

func healthKey(s *Server, own ownSignIns) string {
	c := *s
	c.Name, c.Agents = "", nil
	b, _ := json.Marshal(c)
	st := mcpauth.StatusOf(s.Name, s.URL)
	// and where a command is started, which its agents decide
	var sides []string
	if s.Transport == "stdio" {
		sides = checkSides(s)
	}
	// and the agents signed in to it themselves: one signed in since
	// isn't left as "needs sign-in"
	return fmt.Sprintf("%s|%v|%v|%d|%q|%q", b, st.SignedIn, st.Dead, st.At, sides, own.of(s))
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
	own := readOwnSignIns()
	out := map[string]ServerHealth{}
	var outMu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, checkAtOnce)
	for _, s := range list {
		key := healthKey(s, own)
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
			h := checkServer(ctx, s, own)
			// as the server is now: a sign-in the check found run out
			// isn't one to check it again for
			key := healthKey(s, own)
			// a check cut short by the page going away isn't one, and a
			// server that didn't answer in time is asked again next time:
			// a slow server may answer then (#1467)
			if ctx.Err() == nil {
				checked.Lock()
				if h.Why == "timeout" {
					// nor is an answer from before kept in its place
					delete(checked.m, s.Name)
				} else {
					checked.m[s.Name] = checkedServer{key, h}
				}
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
	return checkServer(ctx, s, nil)
}

// checkServer is CheckServer with the agents' own sign-ins read already,
// once for every server checked together; nil reads them when a server
// asks for a sign-in.
func checkServer(ctx context.Context, s *Server, own ownSignIns) ServerHealth {
	each := remoteStep
	if s.Transport == "stdio" {
		each = stdioStep
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := &checkSteps{step: "initialize", each: each, timer: time.AfterFunc(each, cancel)}
	defer st.timer.Stop()
	ctx = context.WithValue(ctx, stepsKey{}, st)
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
	if s.Transport == "http" {
		// only a server magpie is signed in to has one to report
		if st := mcpauth.StatusOf(s.Name, s.URL); st.SignedIn || st.Dead {
			h.SignIn = &st
		}
	}
	if h.State == "auth" && s.Transport != "stdio" {
		if own == nil {
			own = readOwnSignIns()
		}
		h.AgentSignIn = own.of(s)
	}
	return h
}

func failed(why, detail string) ServerHealth {
	return ServerHealth{State: "error", Why: why, Detail: detail}
}

// checkSteps is the step a check is at, and the timer that ends the check
// when the step takes longer than each.
type checkSteps struct {
	mu    sync.Mutex
	step  string
	each  time.Duration
	timer *time.Timer
}

type stepsKey struct{}

// stepTo starts the check's next step, with a budget of its own.
func stepTo(ctx context.Context, step string) {
	st, _ := ctx.Value(stepsKey{}).(*checkSteps)
	if st == nil || ctx.Err() != nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.step = step
	st.timer.Reset(st.each)
}

// timedOut is a server that didn't answer a step in time; the page says
// which step, and how long it waited.
func timedOut(ctx context.Context) ServerHealth {
	h := failed("timeout", "")
	if st, _ := ctx.Value(stepsKey{}).(*checkSteps); st != nil {
		st.mu.Lock()
		h.Step, h.Waited = st.step, st.each.Milliseconds()
		st.mu.Unlock()
	}
	return h
}

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

func initializeReq() []byte { return request(1, "initialize", initParams()) }

func initParams() map[string]any {
	return map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "magpie", "version": gateway.Version},
	}
}

var initializedNote = []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

// Tool is one tool a server listed: its name, and the first paragraph of
// its description, cut short.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// What of the tools a check keeps: every tool's name up to maxToolList,
// a description up to maxToolDesc runes, and up to maxToolPages pages of
// tools/list, each with a step's budget of its own.
const (
	maxToolList  = 500
	maxToolName  = 128
	maxToolDesc  = 500
	maxToolPages = 10
)

// clip is s with its whitespace runs made one space, cut at n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// toolDesc is the first paragraph of a tool's description: the rest is
// usually how to call it, which the agent reads, not the person.
func toolDesc(d string) string {
	d = strings.TrimSpace(strings.ReplaceAll(d, "\r\n", "\n"))
	if i := strings.Index(d, "\n\n"); i >= 0 {
		d = d[:i]
	}
	return clip(d, maxToolDesc)
}

// toolsOf is the health one reply to tools/list says, and the cursor of
// the page after it: a server with no tools may not know the method at all.
func toolsOf(m *rpcMsg) (ServerHealth, string) {
	if m.Error != nil {
		if m.Error.Code == -32601 {
			return ServerHealth{State: "ok"}, ""
		}
		return failed("protocol", "tools/list: "+m.Error.Message), ""
	}
	var r struct {
		Tools []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(m.Result, &r); err != nil {
		return failed("protocol", "tools/list: "+err.Error()), ""
	}
	h := ServerHealth{State: "ok", Tools: len(r.Tools)}
	for _, t := range r.Tools {
		if len(h.ToolList) == maxToolList {
			break
		}
		d := toolDesc(t.Description)
		if d == "" {
			d = clip(t.Title, maxToolDesc)
		}
		h.ToolList = append(h.ToolList, Tool{Name: clip(t.Name, maxToolName), Description: d})
	}
	return h, r.NextCursor
}

// listTools asks for the server's tools, a page at a time while it names
// a next one, up to maxToolPages. ask sends a request and reads the reply
// to its id. A page after the first that is refused ends the list there.
func listTools(ctx context.Context, ask func(id int, req []byte) (*rpcMsg, *ServerHealth)) ServerHealth {
	var all ServerHealth
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		stepTo(ctx, "tools/list")
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		id := 2 + page
		m, bad := ask(id, request(id, "tools/list", params))
		if bad != nil {
			return *bad
		}
		h, next := toolsOf(m)
		if h.State != "ok" {
			if page == 0 {
				return h
			}
			break
		}
		all.State = "ok"
		all.Tools += h.Tools
		for _, t := range h.ToolList {
			if len(all.ToolList) < maxToolList {
				all.ToolList = append(all.ToolList, t)
			}
		}
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return all
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

// checkStdio starts the command where the agents given it start it
// (checkSides), each place in turn; the first failure is the answer.
func checkStdio(ctx context.Context, s *Server) ServerHealth {
	sides := checkSides(s)
	if len(sides) == 1 && sides[0] == "" {
		return checkStdioAt(ctx, s, "")
	}
	var done, down []string
	var first *ServerHealth
	for _, d := range sides {
		if len(done) > 0 {
			stepTo(ctx, "initialize")
		}
		h := checkStdioAt(ctx, s, d)
		if h.Why == "wsldown" {
			down = append(down, d)
			continue
		}
		done = append(done, d)
		h.Checked = slices.Clone(done)
		if h.State != "ok" {
			return h
		}
		if first == nil {
			first = &h
		}
	}
	if first == nil {
		return failed("wsldown", strings.Join(down, ", "))
	}
	first.Checked = done
	return *first
}

// stdioProgram is the program checkStdio starts for a server's command,
// and the folder it was found in when magpie's PATH lacks it. A bare name
// PATH doesn't have is looked for as a terminal would find it
// (proc.FindTool: the folders a user's tools are installed in, fnm's Nodes
// among them, #2685), and that folder goes on the child's PATH, as it is
// on a terminal's: npx's package runs the node beside it by name. A path
// is taken as it is.
func stdioProgram(name string) (prog, found string) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return name, ""
	}
	if _, err := exec.LookPath(name); err == nil {
		return name, ""
	}
	if p := proc.FindTool(name); p != "" {
		return p, filepath.Dir(p)
	}
	return name, ""
}

// withPath is env with dir after the folders its PATH has (Path, as
// Windows spells it, too).
func withPath(env []string, dir string) []string {
	out := slices.Clone(env)
	for i := len(out) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(out[i], "=")
		if ok && (k == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(k, "PATH")) {
			if v != "" {
				v += string(os.PathListSeparator)
			}
			out[i] = k + "=" + v + dir
			return out
		}
	}
	return append(out, "PATH="+dir)
}

// startServer starts the command s runs as the agents start it, on this
// machine or in WSL distro: in the environment magpie has, with the
// server's own variables over it, in home. Its stdin and stdout are the
// MCP connection; what it writes to stderr is kept in errs; stop ends a
// run in a distro (call it beside tree.Kill). The check starts it so, and
// so does the hub (mcp_hub.go), on this machine.
func startServer(ctx context.Context, s *Server, distro string) (tree *proc.Tree, stdin io.WriteCloser, stdout io.ReadCloser, errs *tail, stop func(), bad *ServerHealth) {
	fail := func(h ServerHealth) (*proc.Tree, io.WriteCloser, io.ReadCloser, *tail, func(), *ServerHealth) {
		return nil, nil, nil, nil, nil, &h
	}
	env, unset := expandAll(s.Env)
	if len(unset) > 0 {
		return fail(failed("novar", strings.Join(unset, ", ")))
	}
	var cmd *exec.Cmd
	var t wslrun.Tool
	stop = func() {}
	found := ""
	if distro == "" {
		var prog string
		prog, found = stdioProgram(s.Command)
		cmd = proc.Command(prog, s.Args...)
	} else {
		var err error
		t, err = wslrun.In(distro, s.Command)
		switch {
		case errors.Is(err, wslrun.ErrDown):
			return fail(failed("wsldown", distro))
		case errors.Is(err, wslrun.ErrNotFound):
			return fail(failed("notfound", s.Command))
		case err != nil:
			return fail(failed("start", "wsl.exe -d "+distro+": "+err.Error()))
		}
		// its own context, ended with the check, which ends the run inside
		// the distro too (killing wsl.exe alone leaves it there); not the
		// check's, whose timeout is told as one rather than as an exit
		var rctx context.Context
		rctx, stop = context.WithCancel(context.WithoutCancel(ctx))
		cmd = wslCommand(rctx, t, s.Args...)
	}
	// Agents start MCP servers from a project or home directory. A magpie
	// launched from a shortcut (Windows Start menu) often has C:\Windows\system32
	// as its own cwd, which is not writable; inherit that and a server that
	// writes into "." dies as "exited (1)" with no log. Pin the check to HOME.
	cmd.Env = os.Environ()
	if d := home(); d != "" {
		cmd.Dir = d
		// and PWD with it, as exec does itself only when Env is nil: a
		// shell-script server that reads $PWD would otherwise see magpie's
		// own folder while it runs in home (Windows doesn't use PWD)
		if runtime.GOOS != "windows" && distro == "" {
			cmd.Env = append(cmd.Env, "PWD="+d)
		}
	}
	if found != "" {
		cmd.Env = withPath(cmd.Env, found)
	}
	var names []string
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
		names = append(names, k)
	}
	if distro != "" {
		// a run in the distro is given the server's own variables, by
		// WSLENV, and none of Windows' others
		slices.Sort(names)
		cmd.Env = t.Env(cmd.Env, names...)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return fail(failed("start", err.Error()))
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return fail(failed("start", err.Error()))
	}
	errs = &tail{}
	cmd.Stderr = errs
	tree, err = proc.StartTree(cmd)
	if err != nil {
		h := failed("start", err.Error())
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			h = failed("notfound", s.Command)
		}
		stop()
		return fail(h)
	}
	return tree, in, out, errs, stop, nil
}

// checkStdioAt starts the command on this machine, or in WSL distro, and
// asks it for its tools.
func checkStdioAt(ctx context.Context, s *Server, distro string) ServerHealth {
	tree, stdin, stdout, errs, stop, bad := startServer(ctx, s, distro)
	if bad != nil {
		return *bad
	}
	var exitErr error
	exited := make(chan struct{})
	go func() { exitErr = tree.Wait(); close(exited) }()
	// the server ends with the check, and whatever it started with it
	defer func() {
		stdin.Close()
		stop()
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
				h := timedOut(ctx)
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
	return listTools(ctx, func(id int, req []byte) (*rpcMsg, *ServerHealth) {
		_ = send(req)
		return await(id)
	})
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
		return timedOut(ctx)
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

// httpSession is a connection to a streamable HTTP server: its headers,
// with magpie's token when it is signed in to it, and the session the
// server gave. A check holds one for its few requests, the hub for as long
// as the server is used (mcp_hub.go).
type httpSession struct {
	s *Server

	mu  sync.Mutex
	hdr http.Header
	// tok is magpie's own token, when it is signed in: one the server
	// refuses is renewed once and asked again with, as the gateway's
	// relay does, and one refused just after it was renewed is a sign-in
	// that has run out
	tok              string
	renewed          bool
	session, version string
	// init is initialize's result
	init json.RawMessage
}

func newHTTPSession(ctx context.Context, s *Server) (*httpSession, *ServerHealth) {
	hdr, bad := remoteHeaders(ctx, s)
	if bad != nil {
		return nil, bad
	}
	c := &httpSession{s: s, hdr: hdr, version: mcpProtocol}
	if s.Transport == "http" && mcpauth.SignedIn(s.Name, s.URL) {
		c.tok = strings.TrimPrefix(hdr.Get("Authorization"), "Bearer ")
	}
	return c, nil
}

func (c *httpSession) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for k, v := range c.hdr {
		req.Header[k] = slices.Clone(v)
	}
	session, version := c.session, c.version
	c.mu.Unlock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", version)
	}
	return checkClient.Do(req)
}

// call posts a request and reads its reply, as JSON or from an event
// stream.
func (c *httpSession) call(ctx context.Context, id int, body []byte) (*rpcMsg, *ServerHealth) {
	res, err := c.post(ctx, body)
	if err != nil {
		h := netHealth(ctx, err)
		return nil, &h
	}
	defer res.Body.Close()
	c.mu.Lock()
	tok, renewed := c.tok, c.renewed
	c.mu.Unlock()
	if res.StatusCode == 401 && tok != "" {
		if renewed {
			mcpauth.Rejected(c.s.Name, tok)
		} else {
			io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
			fresh, err := mcpauth.Renew(ctx, c.s.Name, tok)
			if err != nil {
				return nil, &ServerHealth{State: "auth", Code: 401, OAuth: true, Detail: err.Error()}
			}
			c.mu.Lock()
			c.renewed, c.tok = true, fresh
			c.hdr.Set("Authorization", "Bearer "+fresh)
			c.mu.Unlock()
			return c.call(ctx, id, body)
		}
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		h := refused(res)
		return nil, &h
	}
	if res.StatusCode/100 != 2 {
		h := statusHealth(res)
		return nil, &h
	}
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.session = sid
		c.mu.Unlock()
	}
	m, err := replyOf(res, id)
	if err != nil {
		if ctx.Err() != nil {
			h := timedOut(ctx)
			return nil, &h
		}
		h := failed("protocol", err.Error())
		return nil, &h
	}
	return m, nil
}

// initialize opens the session: initialize, and the note that it is.
func (c *httpSession) initialize(ctx context.Context, init []byte) *ServerHealth {
	m, h := c.call(ctx, 1, init)
	if h != nil {
		return h
	}
	if h := initError(m); h != nil {
		return h
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	c.mu.Lock()
	c.init = m.Result
	if json.Unmarshal(m.Result, &r) == nil && r.ProtocolVersion != "" {
		c.version = r.ProtocolVersion
	}
	c.mu.Unlock()
	stepTo(ctx, "notifications/initialized")
	if res, err := c.post(ctx, initializedNote); err == nil {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
	} else if ctx.Err() != nil {
		h := timedOut(ctx)
		return &h
	}
	return nil
}

// end tells the server the session is done with, when it gave one.
func (c *httpSession) end() {
	c.mu.Lock()
	hdr, session := c.hdr.Clone(), c.session
	c.mu.Unlock()
	if session != "" {
		endSession(c.s.URL, hdr, session)
	}
}

func checkHTTP(ctx context.Context, s *Server) ServerHealth {
	c, bad := newHTTPSession(ctx, s)
	if bad != nil {
		return *bad
	}
	if h := c.initialize(ctx, initializeReq()); h != nil {
		return *h
	}
	defer c.end()
	return listTools(ctx, func(id int, body []byte) (*rpcMsg, *ServerHealth) { return c.call(ctx, id, body) })
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
	stepTo(ctx, "connect")
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
				h := timedOut(ctx)
				if ctx.Err() == nil {
					h = failed("protocol", "the event stream ended")
				}
				return e, &h
			}
			return e, nil
		case <-ctx.Done():
			h := timedOut(ctx)
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
	stepTo(ctx, "initialize")
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
	stepTo(ctx, "notifications/initialized")
	if h := post(initializedNote); h != nil && h.Why == "timeout" {
		return *h
	}
	return listTools(ctx, func(id int, req []byte) (*rpcMsg, *ServerHealth) {
		if h := post(req); h != nil {
			return nil, h
		}
		return await(id)
	})
}
