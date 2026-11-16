package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// The Realtime API over a WebSocket: GET /v1/realtime?model=… with
// Upgrade: websocket, as OpenAI's Realtime API and DashScope's (Qwen's)
// realtime models take it (gaubeebangeel on X: a Qwen Token Plan's
// realtime voice through magpie). Alibaba sells the Token Plan's
// realtime voice model, qwen-audio-3.0-realtime-plus, on the plan's own
// host at /api-ws/v1/realtime with the plan's sk-sp- key
// (platform.qianwenai.com/docs/token-plan/best-practices/realtime-voice).
//
// The session goes to one model's provider, over its keys or accounts as
// a chat would, and is passed on frame by frame both ways; magpie reads
// the frames only to mask what goes out (as the settings say), put the
// masked values back in what comes in, and count each response.done's
// usage. The upgrade comes through the whole handler, so the allow list,
// the gateway key and its models and accounts, the Origin and Host
// checks judge it as they judge any request; a page from another origin
// can't open one at all (a browser's WebSocket isn't held by CORS), and a
// session opened with a key ends when the key is no longer on.
//
// A provider's realtime address is its OpenAI-style base's: DashScope's
// (…/compatible-mode/v1) at /api-ws/v1/realtime on the same host, any
// other at base + /realtime (OpenAI's /v1/realtime).

var (
	// realtimeDial bounds the upstream handshake
	realtimeDial = 30 * time.Second
	// realtimeKeyCheck is how often a session opened with a gateway key
	// looks whether the key is still on
	realtimeKeyCheck = 5 * time.Second
)

// realtimeReadLimit is the largest frame read either way: a few seconds
// of base64 audio is well under it.
const realtimeReadLimit = 32 << 20

// realtimeSessions are the sessions being served; their connections were
// taken from the server (hijacked), so they are closed when it ends.
type realtimeSessions struct {
	mu      sync.Mutex
	m       map[*websocket.Conn]bool
	closing bool
	// running counts the handlers serving a session, for end's caller to
	// wait for (wait); idle is closed when it is back at none
	running int
	idle    chan struct{}
}

func (rs *realtimeSessions) enter() {
	rs.mu.Lock()
	rs.running++
	rs.mu.Unlock()
}

func (rs *realtimeSessions) leave() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.running--; rs.running == 0 && rs.idle != nil {
		close(rs.idle)
		rs.idle = nil
	}
}

func (rs *realtimeSessions) add(c *websocket.Conn) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closing {
		return false
	}
	if rs.m == nil {
		rs.m = map[*websocket.Conn]bool{}
	}
	rs.m[c] = true
	return true
}

func (rs *realtimeSessions) remove(c *websocket.Conn) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.m, c)
}

func (rs *realtimeSessions) ending() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.closing
}

// end closes every session going away; a client opens a new one to the
// gateway serving the port then.
func (rs *realtimeSessions) end() {
	rs.mu.Lock()
	rs.closing = true
	all := make([]*websocket.Conn, 0, len(rs.m))
	for c := range rs.m {
		all = append(all, c)
	}
	rs.mu.Unlock()
	for _, c := range all {
		go c.Close(websocket.StatusGoingAway, "magpie is restarting")
	}
}

// wait waits for every session's handler to have returned, or ctx.
func (rs *realtimeSessions) wait(ctx context.Context) {
	rs.mu.Lock()
	if rs.running == 0 {
		rs.mu.Unlock()
		return
	}
	if rs.idle == nil {
		rs.idle = make(chan struct{})
	}
	idle := rs.idle
	rs.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
}

// realtime serves GET /v1/realtime.
func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	if !wantsSocket(r) {
		w.Header().Set("Upgrade", "websocket")
		writeError(w, provider.Chat, http.StatusUpgradeRequired, "the Realtime API is a WebSocket: open GET /v1/realtime?model=<provider>/<model> with Upgrade: websocket")
		return
	}
	s.realtimes.enter()
	defer s.realtimes.leave()
	start := time.Now()
	asked := strings.TrimSpace(r.URL.Query().Get("model"))
	who := callerOf(r)
	call := Call{Time: start, From: provider.Chat, To: provider.Chat, Agent: who.agent, Via: who.via, Model: asked, headers: headerLogOf(r.Context())}
	usage.Saw(agentOf(r))
	refuse := func(code int, msg string) {
		call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
		writeError(w, provider.Chat, code, msg)
		s.record(call)
	}
	// a page of another origin (the library's check, made before anything
	// is dialled): corsGuard let a page on this computer by, which a
	// browser's WebSocket isn't held from by CORS
	if !sameOriginSocket(r) {
		refuse(http.StatusForbidden, "magpie opens a realtime session only for a client that isn't a web page, or a page at magpie's own address")
		return
	}
	if s.realtimes.ending() {
		refuse(http.StatusServiceUnavailable, "magpie is restarting")
		return
	}
	if asked == "" {
		refuse(http.StatusBadRequest, "name the model: /v1/realtime?model=<provider>/<model> (qwen-token-plan/qwen-audio-3.0-realtime-plus, openai/gpt-realtime)")
		return
	}
	if msg, off := idOff(asked); off {
		refuse(http.StatusForbidden, msg)
		return
	}
	if _, _, isGroup := provider.FindGroup(asked); isGroup {
		refuse(http.StatusBadRequest, "a realtime session goes to one model: name a model, not the routing group "+asked)
		return
	}
	p, model, code, msg := resolveRealtimeModel(asked)
	if code != 0 {
		refuse(code, msg)
		return
	}
	if msg, off := modelOff(p, model, asked); off {
		refuse(http.StatusForbidden, msg)
		return
	}
	if keyWho, held := keyHolds(r); held && !modelAllowed(keyWho, p, model) {
		st, msg := keyModelRefusal(w, keyWho, asked)
		refuse(st, msg)
		return
	}
	tries, code, msg := s.onKeys(r, p, model, asked)
	if code != 0 {
		refuse(code, msg)
		return
	}
	if keyWho, held := accountHolds(r); held {
		tries = slices.DeleteFunc(tries, func(c candidate) bool { return !accountAllowed(keyWho, c) })
		if len(tries) == 0 {
			refuse(http.StatusForbidden, keyAccountsError(keyWho, asked))
			return
		}
	}
	tries = probeOne(tries) // of those resting, only the one back soonest (#1799)

	// the session lives past the handshake: its context ends when the
	// handler returns
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	offered := realtimeSubprotocols(r.Header)
	var up *websocket.Conn
	var c candidate
	var skipped []string
	for i, try := range tries {
		last := i == len(tries)-1
		call.Provider, call.Model = try.p.ID, asked
		u, why := realtimeURL(try.p, try.model)
		var err error
		code := http.StatusBadRequest
		if why != "" {
			err = errors.New(why)
		} else {
			up, code, err = s.dialRealtime(ctx, r, try.p, u, offered)
		}
		if err == nil {
			c = try
			break
		}
		if !last && r.Context().Err() == nil {
			skipped = append(skipped, try.label()+": "+err.Error())
			continue
		}
		call.Fallback = strings.Join(skipped, "; ")
		msg := err.Error()
		if len(skipped) > 0 {
			msg += " (tried first: " + call.Fallback + ")"
		}
		refuse(code, msg)
		return
	}
	call.Fallback = strings.Join(skipped, "; ")
	defer up.CloseNow()
	up.SetReadLimit(realtimeReadLimit)

	// what goes out is masked, unless the provider is set to go unmasked;
	// fails closed before the client is answered
	var mask *realtimeMask
	if !c.unmasked() {
		mctx, m, err := maskingFor(r.Context())
		if err != nil {
			up.Close(websocket.StatusGoingAway, "")
			refuse(maskStatus(err), err.Error())
			return
		}
		if m != nil {
			mask = &realtimeMask{ctx: mctx, m: m}
		}
	}

	var accept []string
	if sp := up.Subprotocol(); sp != "" {
		accept = []string{sp}
	}
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: accept})
	if err != nil {
		up.Close(websocket.StatusGoingAway, "")
		call.Status, call.Error, call.Millis = http.StatusBadRequest, "the client's WebSocket handshake failed: "+err.Error(), time.Since(start).Milliseconds()
		s.record(call)
		return
	}
	defer client.CloseNow()
	client.SetReadLimit(realtimeReadLimit)
	if !s.realtimes.add(client) {
		client.Close(websocket.StatusGoingAway, "magpie is restarting")
		up.Close(websocket.StatusGoingAway, "")
		return
	}
	defer s.realtimes.remove(client)

	// a session opened with a gateway key ends once the key isn't on, or
	// its limit is spent (keyLimited let it in while it had some left)
	if keyWho := access.Caller(r.Context()); keyWho.KeyID != "" {
		go func() {
			t := time.NewTicker(realtimeKeyCheck)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if !access.Current(keyWho) {
						log.Printf("realtime: the gateway key %s is no longer on, so its session to %s is closed", keyWho.KeyName, c.p.ID)
						go up.Close(websocket.StatusPolicyViolation, "")
						client.Close(websocket.StatusPolicyViolation, "the gateway key this session was opened with is no longer on")
						return
					}
					if keyWho.Limited() {
						release, err := reserveFor(keyWho, 0, asked, limitClock())
						if err != nil {
							log.Printf("realtime: the gateway key %s can't spend more (%v), so its session to %s is closed", keyWho.KeyName, err, c.p.ID)
							go up.Close(websocket.StatusPolicyViolation, "")
							client.Close(websocket.StatusPolicyViolation, trimReason(err.Error()))
							return
						}
						release()
					}
				}
			}
		}()
	}

	sess := &realtimeSession{s: s, r: r, c: c, asked: asked, mask: mask}
	status, why := sess.pipe(ctx, client, up)
	call.Status, call.Millis, call.Usage = status, time.Since(start).Milliseconds(), sess.total
	call.Error = cmpOr(why, sess.lastErr)
	s.record(call)
}

// realtimeUpgrade: r opens a realtime session, a GET that spends as a
// chat does, so a key's limits and slots hold it (keyLimited, keySlots).
func realtimeUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet && (r.URL.Path == "/v1/realtime" || r.URL.Path == "/realtime") && wantsSocket(r)
}

// sameOriginSocket: the upgrade has no Origin (a program's client), or
// one at the address it was sent to, as coder/websocket's own check
// takes it.
func sameOriginSocket(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// realtimeSubprotocols are the subprotocols the client offered, less
// the ones a browser carries an OpenAI key or organization in, which are
// never passed on.
func realtimeSubprotocols(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" || strings.HasPrefix(p, "openai-insecure-api-key.") || strings.HasPrefix(p, "openai-organization.") || strings.HasPrefix(p, "openai-project.") {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// resolveRealtimeModel finds the model's provider as a chat's is found;
// a bare id no provider lists (the Token Plan's hosts list nothing) goes
// to the one DashScope provider turned on, when there is one.
func resolveRealtimeModel(asked string) (provider.Provider, string, int, string) {
	if p, model, ok := provider.Resolve(asked); ok {
		return p, model, 0, ""
	}
	if off, isOff := provider.SwitchedOff(asked); isOff {
		return provider.Provider{}, "", http.StatusNotFound, switchedOff(off, asked)
	}
	if strings.Contains(asked, "/") {
		return provider.Provider{}, "", http.StatusNotFound, unknownModel(asked, "")
	}
	var found []provider.Provider
	for _, p := range provider.All() {
		if p.On() && !p.DecideOnly() && dashScopeBase(p.OpenAIBase()) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], asked, 0, ""
	case 0:
		return provider.Provider{}, "", http.StatusNotFound, unknownModel(asked, "")
	}
	ids := make([]string, len(found))
	for i, p := range found {
		ids[i] = p.ID + "/" + asked
	}
	return provider.Provider{}, "", http.StatusNotFound, fmt.Sprintf("more than one provider may serve %s: name one, as %s", asked, strings.Join(ids, " or "))
}

func dashScopeBase(base string) bool {
	return strings.HasSuffix(strings.TrimRight(base, "/"), "/compatible-mode/v1")
}

// realtimeURL is where the provider takes a realtime session for model,
// or why it takes none.
func realtimeURL(p provider.Provider, model string) (string, string) {
	base := strings.TrimRight(p.OpenAIBase(), "/")
	if base == "" {
		return "", p.Name + " has no OpenAI-style API, so no Realtime API"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", p.Name + "'s address isn't one a WebSocket is opened at"
	}
	if dashScopeBase(base) {
		u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/compatible-mode/v1") + "/api-ws/v1/realtime"
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/realtime"
	}
	q := u.Query()
	q.Set("model", model)
	u.RawQuery = q.Encode()
	return u.String(), ""
}

// dialRealtime opens the session at the provider's realtime address,
// signed as its requests are. Its connection lives as long as ctx; the
// handshake has realtimeDial. A refusal's code is the vendor's.
func (s *Server) dialRealtime(ctx context.Context, r *http.Request, p provider.Provider, u string, subprotocols []string) (*websocket.Conn, int, error) {
	ctx = s.metered(p.Via(ctx), p, "") // counted against its MaxRPM (rpm.go)
	dctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(realtimeDial, cancel)
	defer timer.Stop()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, http.StatusBadRequest, err
	}
	stampUA(dctx, p, req.Header)
	if p.IsRemoteMagpie() {
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(dctx, req)
	}
	// OpenAI's beta Realtime API asks for its header
	if v := r.Header.Get("OpenAI-Beta"); v != "" {
		req.Header.Set("OpenAI-Beta", v)
	}
	if err := p.Sign(dctx, req, provider.Chat, nil); err != nil {
		cancel()
		return nil, http.StatusBadGateway, err
	}
	conn, res, err := websocket.Dial(dctx, u, &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: s.client.Transport, CheckRedirect: provider.KeepsCredentials},
		HTTPHeader:   req.Header,
		Subprotocols: subprotocols,
	})
	if err != nil {
		timedOut := !timer.Stop() && ctx.Err() == nil
		cancel()
		host := provider.HostOf(u)
		if res != nil && res.StatusCode >= 300 {
			var b []byte
			if res.Body != nil {
				b, _ = io.ReadAll(io.LimitReader(res.Body, 64<<10))
				res.Body.Close()
			}
			hint := ""
			if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
				hint = " — " + p.Name + " may not serve the Realtime API"
			}
			return nil, res.StatusCode, fmt.Errorf("%s: %d %s%s%s", host, res.StatusCode, http.StatusText(res.StatusCode), vendorSaid(vendorMessage(bytes.TrimSpace(b))), hint)
		}
		if timedOut {
			return nil, http.StatusGatewayTimeout, fmt.Errorf("%s didn't open the session in %s", host, realtimeDial)
		}
		return nil, http.StatusBadGateway, fmt.Errorf("%s: %v", host, err)
	}
	// the connection is the session's: cancel ends it with ctx
	context.AfterFunc(ctx, cancel)
	return conn, http.StatusSwitchingProtocols, nil
}

// realtimeMask masks the client's frames and puts the values back in the
// vendor's.
type realtimeMask struct {
	ctx context.Context
	m   gwmw.Masker
}

// out is a client's frame masked, or the error to hold it back for.
func (rm *realtimeMask) out(b []byte) ([]byte, error) {
	got, err := gwmw.Safely(func() []byte {
		var hits []gwmw.Hit
		switch t := rm.m.(type) {
		case gwmw.ContextTeller:
			b, hits = t.JSONHitsContext(rm.ctx, b)
		case gwmw.Teller:
			b, hits = t.JSONHits(b)
		default:
			b, _ = rm.m.JSON(b)
			return b
		}
		if err := blocked(hitsOf(hits)); err != nil {
			panic(err)
		}
		return b
	})
	return got, err
}

// in is a vendor's frame with the masked values back.
func (rm *realtimeMask) in(b []byte) []byte {
	return []byte(rm.m.Restore(string(b), true))
}

// audioFrame: a frame that carries base64 audio and nothing else, which
// isn't masked (a run of base64 can look like a key) or read for values.
func audioFrame(typ string) bool {
	return typ == "input_audio_buffer.append" || strings.HasSuffix(typ, "audio.delta")
}

type realtimeSession struct {
	s     *Server
	r     *http.Request
	c     candidate
	asked string
	mask  *realtimeMask

	mu      sync.Mutex
	total   Usage
	lastErr string
}

// pipe passes frames both ways until one side ends, then closes the
// other with the same code; it gives the session's status and, for one
// that broke, why.
func (rs *realtimeSession) pipe(ctx context.Context, client, up *websocket.Conn) (int, string) {
	type end struct {
		fromClient bool
		err        error
	}
	ends := make(chan end, 2)
	go func() { ends <- end{true, rs.copy(ctx, client, up, true)} }()
	go func() { ends <- end{false, rs.copy(ctx, up, client, false)} }()
	e := <-ends
	code, reason := websocket.CloseStatus(e.err), ""
	var ce websocket.CloseError
	if errors.As(e.err, &ce) {
		reason = ce.Reason
	}
	status, why := http.StatusOK, ""
	host := provider.HostOf(rs.c.p.OpenAIBase())
	var held realtimeHeld
	switch {
	case errors.As(e.err, &held):
		// a frame masking held back: the session ends, nothing of it sent
		status, why = maskStatus(held.err), held.err.Error()
		code, reason = websocket.StatusPolicyViolation, trimReason(why)
	case code == -1 || code == websocket.StatusNoStatusRcvd || code == websocket.StatusAbnormalClosure || code == websocket.StatusTLSHandshake:
		if e.fromClient {
			code, reason = websocket.StatusGoingAway, ""
		} else {
			status, why = http.StatusBadGateway, host+" ended the session: "+e.err.Error()
			code, reason = websocket.StatusInternalError, trimReason(why)
		}
	case !e.fromClient && code != websocket.StatusNormalClosure && code != websocket.StatusGoingAway:
		status, why = http.StatusBadGateway, fmt.Sprintf("%s closed the session %d %s", host, code, reason)
	}
	// both closed at once: each waits for its peer's close frame
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); client.Close(code, reason) }()
	go func() { defer wg.Done(); up.Close(code, reason) }()
	wg.Wait()
	<-ends
	return status, why
}

// trimReason fits a close frame's reason, 123 bytes at most.
func trimReason(s string) string {
	if len(s) <= 123 {
		return s
	}
	s = s[:120]
	for len(s) > 0 && !utf8Start(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	if len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// realtimeHeld is a client's frame masking wouldn't let go.
type realtimeHeld struct{ err error }

func (h realtimeHeld) Error() string { return h.err.Error() }

// copy passes from's frames to to, masking a client's and counting the
// vendor's usage, until from ends.
func (rs *realtimeSession) copy(ctx context.Context, from, to *websocket.Conn, fromClient bool) error {
	for {
		typ, b, err := from.Read(ctx)
		if err != nil {
			return err
		}
		if typ == websocket.MessageText {
			kind := gjson.GetBytes(b, "type").String()
			switch {
			case audioFrame(kind):
			case fromClient && rs.mask != nil:
				if b, err = rs.mask.out(b); err != nil {
					return realtimeHeld{err}
				}
			case !fromClient:
				rs.heard(kind, b)
				if rs.mask != nil {
					b = rs.mask.in(b)
				}
			}
		}
		if err := to.Write(ctx, typ, b); err != nil {
			return err
		}
	}
}

// heard counts a vendor event: a response.done's usage is recorded as a
// call of its own in the usage ledger, an error kept for the record.
func (rs *realtimeSession) heard(kind string, b []byte) {
	switch kind {
	case "error":
		rs.mu.Lock()
		rs.lastErr = gjson.GetBytes(b, "error.message").String()
		rs.mu.Unlock()
	case "response.done":
		u := gjson.GetBytes(b, "response.usage")
		if !u.Exists() {
			return
		}
		in, out := int(u.Get("input_tokens").Int()), int(u.Get("output_tokens").Int())
		cached := int(u.Get("input_token_details.cached_tokens").Int())
		if cached == 0 {
			cached = int(u.Get("input_tokens_details.cached_tokens").Int())
		}
		cached = min(cached, in)
		in -= cached // the ledger's input leaves out what was read from the cache
		rs.mu.Lock()
		rs.total.Input += in
		rs.total.Output += out
		rs.total.CacheRead += cached
		rs.mu.Unlock()
		c := rs.c
		keyID, keyName := "", ""
		if c.p.Account == nil && c.p.Key != "" {
			keyID, keyName = provider.KeyID(c.p.Key), c.p.KeyName
		}
		who := callerOf(rs.r)
		status := http.StatusOK
		if st := gjson.GetBytes(b, "response.status").String(); st == "failed" {
			status = http.StatusBadGateway
		}
		appendUsage(rs.r, usage.Record{Operation: "realtime", Time: time.Now(), Agent: who.agent, Via: who.via, Provider: c.p.ID, Host: c.p.Where(), Model: c.model, Requested: rs.asked,
			ProviderKeyID: keyID, ProviderKeyName: keyName, ProviderAccount: accountOf(c.p),
			Input: in, Output: out, CacheRead: cached, Status: status, Session: sessionOf(rs.r.Header)})
	}
}
