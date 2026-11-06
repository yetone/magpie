package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/browserext"
	"github.com/yetone/magpie/internal/settings"
)

// Magpie Browser: the agents magpie serves use the user's own browser,
// its tabs signed in to whatever the user is, through the Magpie Browser
// extension (internal/browserext). Not a browser of magpie's own: a fresh
// profile is signed in to nothing, and an agent's own browser tool stops
// working when its account changes.
//
// The extension holds a WebSocket to BrowserExtensionPath, taken only from
// its own origin. An agent calls the tools at BrowserMCPPath (Streamable
// HTTP, or `magpie mcp browser` over stdio), and each call is handed to
// the extension, which carries it out with the DevTools protocol in the
// tab and answers with MCP content.
//
// Whose browser is whose: the tools act as the user wherever the browser
// is signed in, so a browser is used only by who it is the browser of.
// One on this computer (a loopback connection) is this computer's, and
// only this computer's agents use it. One elsewhere connects with a
// gateway key (magpie in a container, whose host's browser reaches it
// through a published port, or a browser on another computer), and is
// that key's: only callers with that key use it, and this computer's
// agents don't. A caller on another computer with no key, or with another
// key, is never handed a browser. Keyed callers' sessions are the key's
// own ("key:<id>/…", browserOwner), so a key names only its own browsers.
//
// The key the GUI writes into the extension's zip (OwnBrowserKey) is a
// browser key (access.Key.Browser): it opens BrowserExtensionPath and
// nothing else (readOnlyGuard), since the zip sits in Downloads and a copy
// of it must not spend the user's subscriptions. A browser connected with
// it is this computer's too only when it connected from this computer
// (local); a connection lives only while its key is on (watchKey).

// BrowserMCPPath is where the browser MCP server is served.
const BrowserMCPPath = "/mcp/magpie/browser"

// BrowserExtensionPath is where the extension connects.
const BrowserExtensionPath = "/v1/magpie/browser/extension"

// BrowserStatusPath tells whether the extension is connected.
const BrowserStatusPath = "/v1/magpie/browser"

// BrowserSessionHeader names the agent session a call is from, so each
// one goes on in the tab it was working in; `magpie mcp browser` sends one
// a process.
const BrowserSessionHeader = "X-Magpie-Browser-Session"

// browserCallWait is how long a call waits for the extension: a
// browser_wait waits up to 60s, a load 30s, and each acts then waits for the
// page to settle.
const browserCallWait = 100 * time.Second

// browserRelay is the extensions connected: each browser that has Magpie
// Browser (Chrome and Edge both, two profiles of one). A session goes on in
// the one it picked (browser_tabs use), else the last to connect.
type browserRelay struct {
	recs    browserRecs
	mu      sync.Mutex
	conns   []*browserConn
	chosen  map[string]*browserConn // session → the browser it picked
	pending map[string]chan json.RawMessage
	next    uint64
	ids     uint64
}

type browserConn struct {
	c       *websocket.Conn
	id      string // b1, b2: in the order they connected, while magpie runs
	version string
	agent   string
	// key is the gateway key the browser connected with, whose callers
	// use it; "" for this computer's, connected over loopback
	key string
	// who is that key as it was when the browser connected: once it
	// isn't Current (turned off, removed, expired or rotated) the
	// connection is closed (watchKey, call)
	who access.Identity
	// own: key is the one this magpie's own page made for the user's own
	// browser (OwnBrowserKey) and the browser connected from this
	// computer (local), so this computer's callers use it too, as they
	// do one on loopback. From elsewhere the key alone is no proof: it is
	// in a zip in Downloads, and whoever holds a copy could otherwise
	// connect a browser of theirs and be handed this computer's agents'
	// calls (their pages read, and pages of theirs fed back).
	own   bool
	since time.Time
	wmu   sync.Mutex
}

// browserName is the browser a User-Agent names, with its major version:
// Edge, Opera, Vivaldi and Yandex say so; Brave, Arc and the rest of
// Chromium's say only Chrome.
func browserName(ua string) string {
	for _, b := range []struct{ token, name string }{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Vivaldi/", "Vivaldi"}, {"YaBrowser/", "Yandex"}, {"Chrome/", "Chrome"}} {
		if i := strings.Index(ua, b.token); i >= 0 {
			v := ua[i+len(b.token):]
			if j := strings.IndexAny(v, ". "); j >= 0 {
				v = v[:j]
			}
			return strings.TrimSpace(b.name + " " + v)
		}
	}
	return "a Chromium browser"
}

func (c *browserConn) name() string { return browserName(c.agent) }

func (c *browserConn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.c.Write(ctx, websocket.MessageText, b)
}

// current is the browser connected last, whoever's it is, for the GUI.
func (b *browserRelay) current() *browserConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.conns) == 0 {
		return nil
	}
	return b.conns[len(b.conns)-1]
}

// browserOwner is the gateway key whose browsers a session's calls go to:
// the id in a "key:<id>/" the session starts with (after the "hub:" of a
// call through magpie's router), "" for this computer's own.
func browserOwner(session string) string {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(session, "hub:"), "key:")
	if !ok {
		return ""
	}
	// a session that says it is a key's but names none is no one's: never
	// this computer's ("" is no key's id)
	if id, _, ok := strings.Cut(rest, "/"); ok && id != "" {
		return id
	}
	return "?"
}

// keyedSession is session as a caller with gateway key id names it: its
// own, so it reaches only that key's browsers whatever it says.
func keyedSession(id, session string) string { return "key:" + id + "/" + session }

// serves: a session owned by owner (browserOwner) may use c. This
// computer's callers ("") use this computer's browsers and the one its
// own page gave a key (own); a key's callers use the browsers that
// connected with that key.
func (c *browserConn) serves(owner string) bool {
	if owner == "" {
		return c.key == "" || c.own
	}
	return c.key == owner
}

// ownBrowserKeyFile holds the id of the gateway key this magpie's page
// made for the user's own browser, when magpie's folders are out of that
// browser's reach (magpie in a container, the page on another computer):
// the extension downloaded from the page connects with it.
func ownBrowserKeyFile() string { return filepath.Join(settings.Dir(), "browser-key") }

// OwnBrowserKey is the id SetOwnBrowserKey recorded; "" when there is
// none or it can't be read, and a browser with a key is then only that
// key's.
func OwnBrowserKey() string {
	b, err := os.ReadFile(ownBrowserKeyFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetOwnBrowserKey records id as the gateway key of the user's own
// browser (OwnBrowserKey).
func SetOwnBrowserKey(id string) error {
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err
	}
	tmp := ownBrowserKeyFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ownBrowserKeyFile())
}

// ScopeOwnBrowserKey makes the key the extension's zip was given
// (OwnBrowserKey) a browser key, if a magpie from before browser keys made
// it a full-access one: the extension, which holds the same secret, keeps
// connecting, and a copy of the zip left in Downloads no longer reaches a
// model, MCP server or stats. Only a key the GUI made for the zip is
// recorded there (gui's browserKey), so no key the user made is touched.
func ScopeOwnBrowserKey() {
	id := OwnBrowserKey()
	if id == "" {
		return
	}
	if err := access.ScopeBrowser(id); err != nil && !errors.Is(err, access.ErrNoKey) {
		log.Printf("Magpie Browser's gateway key %s: can't make it a browser key: %v", id, err)
	}
}

// forSession is the browser a session's calls go to: the one it picked
// while that one is connected, else the last of its owner's to connect.
func (b *browserRelay) forSession(session string) *browserConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := browserOwner(session)
	if c := b.chosen[session]; c != nil && session != "" && c.serves(owner) {
		return c
	}
	for i := len(b.conns) - 1; i >= 0; i-- {
		if b.conns[i].serves(owner) {
			return b.conns[i]
		}
	}
	return nil
}

// all is every browser session's calls may go to, the first to connect
// first
func (b *browserRelay) all(session string) []*browserConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := browserOwner(session)
	var out []*browserConn
	for _, c := range b.conns {
		if c.serves(owner) {
			out = append(out, c)
		}
	}
	return out
}

// choose has a session's calls go to the connected browser named by its id
// (b2) or, failing that, the one whose name has it (edge), when only one
// does.
func (b *browserRelay) choose(session, which string) (*browserConn, error) {
	which = strings.ToLower(strings.TrimSpace(which))
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := browserOwner(session)
	var by, theirs []*browserConn
	for _, c := range b.conns {
		if !c.serves(owner) {
			continue
		}
		theirs = append(theirs, c)
		if c.id == which {
			by = []*browserConn{c}
			break
		}
		if which != "" && strings.Contains(strings.ToLower(c.name()), which) {
			by = append(by, c)
		}
	}
	switch {
	case len(theirs) == 0:
		return nil, browserAway(session)
	case len(by) == 0:
		return nil, fmt.Errorf("no connected browser is %q: browser_tabs with action browsers lists them", which)
	case len(by) > 1:
		return nil, fmt.Errorf("%d connected browsers are %q: pick one by its id (browser_tabs with action browsers lists them)", len(by), which)
	}
	if b.chosen == nil {
		b.chosen = map[string]*browserConn{}
	}
	b.chosen[session] = by[0]
	return by[0], nil
}

func (b *browserRelay) add(c *browserConn) {
	b.mu.Lock()
	b.ids++
	c.id = "b" + strconv.FormatUint(b.ids, 10)
	b.conns = append(b.conns, c)
	b.mu.Unlock()
}

func (b *browserRelay) remove(c *browserConn) {
	b.mu.Lock()
	for i, x := range b.conns {
		if x == c {
			b.conns = append(b.conns[:i], b.conns[i+1:]...)
			break
		}
	}
	for k, v := range b.chosen {
		if v == c {
			delete(b.chosen, k)
		}
	}
	b.mu.Unlock()
}

func (b *browserRelay) answered(id string, result json.RawMessage) {
	b.mu.Lock()
	ch := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if ch != nil {
		ch <- result
	}
}

// call hands a tool call to the extension and waits for its MCP result.
func (b *browserRelay) call(ctx context.Context, session, tool string, args json.RawMessage) (json.RawMessage, error) {
	c := b.forSession(session)
	if c == nil {
		return nil, browserAway(session)
	}
	if c.key != "" && !access.Current(c.who) {
		// its key was turned off since watchKey last looked: handed nothing
		b.remove(c)
		go c.drop()
		return nil, browserAway(session)
	}
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]chan json.RawMessage{}
	}
	b.next++
	id := strconv.FormatUint(b.next, 10)
	ch := make(chan json.RawMessage, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if err := c.send(ctx, map[string]any{"id": id, "session": session, "tool": tool, "args": args}); err != nil {
		return nil, errors.New("couldn't reach Magpie Browser: " + err.Error())
	}
	t := time.NewTimer(browserCallWait)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-t.C:
		return nil, errors.New("Magpie Browser didn't answer in " + browserCallWait.String() + ": the page may be stuck; take a snapshot or a screenshot to see where it is")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errBrowserAway = errors.New("Magpie Browser isn't connected, so there is no browser to use. Ask the user to set it up: magpie's Library → Magpie Browser → Set up, which loads the extension in Chrome, Edge or another Chromium browser. Once installed it connects by itself whenever that browser and magpie are both running.")

// browserAway is why session has no browser to use.
func browserAway(session string) error {
	if browserOwner(session) == "" {
		return errBrowserAway
	}
	return errors.New("Magpie Browser isn't connected with this gateway key, so there is no browser to use: a browser connects as a key's when its extension is given the key (the extension's popup → Gateway key), and is used only by that key's agents and magpie's own. Ask the user to set it up there.")
}

// browserExtensionKey is the gateway key the extension's request r comes
// with (no KeyID for one on this computer), and whether it is let
// connect: from this computer over loopback, or with a gateway key that
// is on and may connect a browser (one with full access, or the
// extension's own, Key.Browser; never a read-only one).
func browserExtensionKey(r *http.Request) (access.Identity, bool) {
	if r.Header.Get("Origin") != browserext.Origin {
		return access.Identity{}, false
	}
	if local(r) && loopbackHost(r.Host) {
		return access.Identity{}, true
	}
	who := access.Caller(r.Context())
	if who.KeyID == "" {
		// a key lanGuard didn't read: the gateway isn't on the network,
		// so this is a container's published port or a proxy on this
		// computer
		for _, k := range callerKeys(r) {
			if c, err := access.Check(k); err == nil && c.KeyID != "" {
				who = c
				break
			}
		}
	}
	if who.KeyID == "" || (who.ReadOnly && !who.Browser) {
		return access.Identity{}, false
	}
	return who, true
}

// browserKeyCheck is how often a browser connected with a key looks
// whether the key is still on (watchKey).
var browserKeyCheck = 5 * time.Second

// watchKey closes c's connection once the key it connected with is no
// longer Current: turned off, removed, expired or rotated, looking every
// each. A browser call looks too (call), so none is handed to it in
// between. each is browserKeyCheck as the connection was taken, read
// there and not on this goroutine: a test that sets browserKeyCheck once
// a browser has connected is ordered after that read, never beside it
// (#5569).
func (c *browserConn) watchKey(ctx context.Context, each time.Duration) {
	t := time.NewTicker(each)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !access.Current(c.who) {
				c.drop()
				return
			}
		}
	}
}

// drop closes the connection of a browser whose key is no longer on.
func (c *browserConn) drop() {
	log.Printf("Magpie Browser connected with gateway key %s: the key is no longer on, so the connection is closed", c.key)
	c.c.Close(websocket.StatusPolicyViolation, "the gateway key this browser connected with is no longer on")
}

// browserExtension takes the extension's WebSocket.
func (s *Server) browserExtension(w http.ResponseWriter, r *http.Request) {
	who, ok := browserExtensionKey(r)
	key := who.KeyID
	if !ok {
		http.Error(w, "only the Magpie Browser extension connects here: on this computer, or with a gateway key", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"chrome-extension://" + browserext.ID}})
	if err != nil {
		log.Printf("browser extension socket: %v", err)
		return
	}
	ws.SetReadLimit(64 << 20) // a full-page screenshot
	c := &browserConn{c: ws, key: key, who: who, own: key != "" && local(r) && key == OwnBrowserKey(), since: time.Now()}
	ctx := r.Context()
	defer ws.CloseNow()
	if key != "" {
		watch, stop := context.WithCancel(ctx)
		defer stop()
		go c.watchKey(watch, browserKeyCheck)
	}
	added := false
	defer func() {
		if added {
			s.browser.remove(c)
			log.Printf("Magpie Browser disconnected")
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Result  json.RawMessage `json:"result"`
			Version string          `json:"version"`
			Agent   string          `json:"agent"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch {
		case m.Type == "hello":
			c.version, c.agent = m.Version, m.Agent
			if !added {
				s.browser.add(c)
				added = true
				if key != "" {
					log.Printf("Magpie Browser %s connected with gateway key %s", m.Version, key)
				} else {
					log.Printf("Magpie Browser %s connected", m.Version)
				}
			}
			if browserext.Older(m.Version, browserext.Version()) {
				// an older extension: its folder gets this magpie's, which
				// the browser reads when it starts again (or the extension's
				// reload on chrome://extensions). The extension isn't told
				// to reload itself: an unpacked one that calls
				// chrome.runtime.reload() doesn't come back (Chrome for
				// Testing 1234, headless and not, loaded either way).
				browserext.Refresh(URL())
			}
		case m.Type == "ping":
			c.send(ctx, map[string]string{"type": "pong"})
		case m.ID != "":
			s.browser.answered(m.ID, m.Result)
		}
	}
}

// browserStatus says whether the extension is connected, for the GUI.
func (s *Server) browserStatus(w http.ResponseWriter, r *http.Request) {
	if !local(r) || fromBrowser(r) {
		http.Error(w, "Magpie Browser's state is told to this computer's magpie only", http.StatusForbidden)
		return
	}
	out := map[string]any{"connected": false, "version": browserext.Version()}
	if c := s.browser.current(); c != nil {
		out["connected"] = true
		out["extension"] = c.version
		out["browser"] = c.agent
		out["since"] = c.since
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) browserMCP(w http.ResponseWriter, r *http.Request) {
	// no web page's, whatever Settings lists; another computer's only with
	// a gateway key, and then only that key's browsers (browserSession)
	if fromBrowser(r) || (!local(r) && access.Caller(r.Context()).KeyID == "") {
		http.Error(w, "magpie's browser MCP server answers agents on this computer, or with a gateway key", http.StatusForbidden)
		return
	}
	serveMCP(w, r, "magpie's browser", "magpie-browser", 1<<20, s.browserMCPCall)
}

func (s *Server) browserMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie-browser", "title": "Magpie Browser", "version": Version},
			"instructions":    browserInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{browserOneTool()}}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		one, shot := p.Name == "browser", false
		acted := map[string]any{} // the arguments, for a recording's frame
		if one {
			tool, args, wants, err := browserUnfold(p.Arguments)
			if err != nil {
				return toolResult(err.Error(), true), nil
			}
			json.Unmarshal(p.Arguments, &acted)
			if tool == browserRecordTool {
				return s.browserRecord(r, browserSession(r), acted["action"] == "record_stop"), nil
			}
			p.Name, p.Arguments, shot = tool, args, wants
		} else if !browserToolNamed(p.Name) {
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name}
		}
		said := func(r map[string]any) map[string]any {
			if one {
				browserSays(r)
			}
			return r
		}
		session := browserSession(r)
		var tabs struct{ Action, Browser string }
		if p.Name == "browser_tabs" {
			json.Unmarshal(p.Arguments, &tabs)
			switch tabs.Action {
			case "browsers":
				return said(toolResult(s.browser.list(session), false)), nil
			case "use":
				c, err := s.browser.choose(session, tabs.Browser)
				if err != nil {
					return said(toolResult(err.Error(), true)), nil
				}
				return said(toolResult("Calls now go to "+c.id+" ("+c.name()+"). browser_tabs lists its tabs.", false)), nil
			}
		}
		out, err := s.browser.call(r.Context(), session, p.Name, p.Arguments)
		if err != nil {
			return said(toolResult(err.Error(), true)), nil
		}
		var result map[string]any
		if json.Unmarshal(out, &result) != nil || result["content"] == nil {
			return toolResult("Magpie Browser answered with something that isn't a tool result: update it (magpie's Library → Magpie Browser)", true), nil
		}
		// with two browsers connected, the list of tabs says whose they are
		if p.Name == "browser_tabs" && (tabs.Action == "" || tabs.Action == "list") {
			if all := s.browser.all(session); len(all) > 1 {
				if c := s.browser.forSession(session); c != nil {
					if content, ok := result["content"].([]any); ok {
						result["content"] = append([]any{map[string]any{"type": "text", "text": fmt.Sprintf("These are %s's tabs (%s); %d browsers are connected: browser_tabs with action browsers lists them.", c.id, c.name(), len(all))}}, content...)
					}
				}
			}
		}
		rec := s.browser.recs.on(session) && result["isError"] != true && browserRecWorth(p.Name, tabs.Action)
		if (shot || rec) && result["isError"] != true {
			data, why := s.browserPicture(r, session)
			if shot {
				s.browserShot(result, data, why)
			}
			if rec && data != "" {
				name := strings.TrimPrefix(p.Name, "browser_")
				if a, _ := acted["action"].(string); a != "" {
					name = a
				}
				if f, err := browserRecFrame(data, browserRecSay(s.browser.recs.count(session)+1, name, acted)); err == nil {
					s.browser.recs.add(session, f)
				}
			}
		}
		return said(result), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// browserPicture is a picture of session's window (base64), or why there
// is none.
func (s *Server) browserPicture(r *http.Request, session string) (data, why string) {
	out, err := s.browser.call(r.Context(), session, "browser_screenshot", nil)
	if err != nil {
		return "", err.Error()
	}
	var shot struct {
		Content []struct{ Type, Data string }
		IsError bool
	}
	if json.Unmarshal(out, &shot) == nil && !shot.IsError {
		for _, c := range shot.Content {
			if c.Type == "image" && c.Data != "" {
				return c.Data, ""
			}
		}
	}
	return "", "it didn't answer with a picture"
}

// browserShot adds a small picture of the window, data, to an action's
// result. One that couldn't be taken says why; the action still happened.
func (s *Server) browserShot(result map[string]any, data, why string) {
	content, _ := result["content"].([]any)
	if data != "" {
		if small, ok := browserSmall(data); ok {
			result["content"] = append(content, map[string]any{"type": "image", "data": small, "mimeType": "image/jpeg"})
			return
		}
		why = "it didn't answer with a picture"
	}
	result["content"] = append(content, map[string]any{"type": "text", "text": "(no screenshot: " + why + ")"})
}

// list is the connected browsers, the one session's calls go to marked *.
func (b *browserRelay) list(session string) string {
	all := b.all(session)
	if len(all) == 0 {
		return browserAway(session).Error()
	}
	mine := b.forSession(session)
	lines := []string{fmt.Sprintf("%d browsers with Magpie Browser are connected (* is the one calls go to; browser_tabs with action use and browser picks another, by id or name):", len(all))}
	for _, c := range all {
		mark := " "
		if c == mine {
			mark = "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s, extension %s, connected %s", mark, c.id, c.name(), c.version, c.since.Format("15:04")))
	}
	return strings.Join(lines, "\n")
}

// browserSession is which agent session a call is from: the header the
// stdio server sends, else the MCP session, else the agent. Another
// computer's caller's is its gateway key's (keyedSession); this
// computer's callers are trusted with any.
func browserSession(r *http.Request) string {
	session := "default"
	for _, h := range []string{BrowserSessionHeader, "Mcp-Session-Id"} {
		if v := boundedSession(r.Header.Get(h)); v != "" {
			session = v
			break
		}
	}
	if session == "default" {
		if a := callerOf(r).agent; a != "" {
			session = "agent:" + a
		}
	}
	if !local(r) {
		return keyedSession(access.Caller(r.Context()).KeyID, session)
	}
	return session
}
