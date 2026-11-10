package gateway

// The native route: a provider's own API, reached through magpie
// (axelburks on Discord). /v1/images/generations and /v1/videos take
// OpenAI's shape and pass on the vendor fields they don't read, but a
// vendor's API grows faster than magpie maps it. A caller who wants all of
// it asks
//
//	<METHOD> /v1/magpie/native/<provider>/<path>
//
// and magpie sends it to the provider's host, at /<path> as the caller
// wrote it (not under the configured base's /v1 or /api/v3: a vendor's
// API has v2 paths too), the body and query as they came, signed with
// the provider's key; the answer comes back as it came, a stream as it
// arrives. So magpie relays every API a key of the user's reaches without
// handing the key out. What it keeps of the gateway:
//
//   - the gateway's own guards first, as every route (a key from beyond
//     loopback, corsGuard's Origin checks, lanGuard's Host check, a
//     read-only key refused, the key's rate);
//   - a key held to models, accounts or a spend limit is refused: a path
//     magpie doesn't read can't be tied to a model, and magpie prices no
//     answer it doesn't read, so it can't hold such a key to them. Such a
//     key uses the OpenAI routes;
//   - an API key's provider only (nativeKeyed): a vendor's or the user's
//     own, never a subscription's, an OAuth sign-in's or a plugin's, whose
//     tokens aren't API keys and whose vendors don't take their own API
//     asked with them;
//   - a path that stays on the provider's host (nativeTarget): no "." or
//     ".." segment, encoded or not, no scheme, no second host;
//   - the caller's own auth headers (and its gateway key in ?key=) never
//     go on; the provider's key goes in as the provider takes it (Sign);
//   - with Settings › Privacy masking, a JSON body's strings are masked
//     (fails closed; another body is refused) and put back in the answer;
//   - each request is a Requests row and a usage record, with the tokens
//     its answer says when it says them (usageSniffer).
//
// Ark's images and video tasks at their short paths under the key's base
// (nativeArkPath) are served as they were first (nativeArk).

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// NativePath is where a provider's own API is reached.
const NativePath = "/v1/magpie/native/"

// nativeMethods are the methods the native route takes.
var nativeMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// nativePaths are the paths under an Ark key's base (/api/v3) the native
// route passes on, and their methods: images, and video tasks (create,
// list, get, cancel or delete).
func nativeArkPath(method, path string) bool {
	switch {
	case path == "images/generations":
		return method == http.MethodPost
	case path == "contents/generations/tasks":
		return method == http.MethodPost || method == http.MethodGet
	}
	if id, ok := strings.CutPrefix(path, "contents/generations/tasks/"); ok && idPart.MatchString(id) {
		return method == http.MethodGet || method == http.MethodDelete
	}
	return false
}

// nativeHeld says who is held to something the native route can't hold
// it to: models, accounts, or a spend limit, its own or its issuer's.
func nativeHeld(who access.Identity) bool {
	if who.Restricted() || who.Limit != nil || len(who.More) > 0 {
		return true
	}
	return who.Issuer != nil && nativeHeld(*who.Issuer)
}

func (s *Server) native(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id, path := r.PathValue("provider"), r.PathValue("path")
	who := callerOf(r)
	call := &Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Provider: id, headers: headerLogOf(r.Context())}
	usage.Saw(agentOf(r))
	fail := func(code int, msg string) {
		call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
		writeError(w, provider.Chat, code, msg)
		s.record(*call)
	}
	if k := access.Caller(r.Context()); nativeHeld(k) {
		fail(http.StatusForbidden, fmt.Sprintf("The gateway key %q is held to some models, accounts or a spend limit, which magpie can't hold a vendor's own API to: use /v1/images/generations and /v1/videos, which pass on the vendor's own fields too", k.KeyName))
		return
	}
	found, err := provider.Find(id)
	if err != nil || !found.On() || found.DecideOnly() {
		fail(http.StatusNotFound, fmt.Sprintf("magpie has no provider %q turned on", id))
		return
	}
	p := *found
	if !p.IsRemoteMagpie() && isArk(p) && nativeArkPath(r.Method, path) {
		s.nativeArk(w, r, p, call, fail)
		return
	}
	s.nativeAny(w, r, p, call, fail)
}

// nativeArk is Ark's images and video tasks at their short paths, joined
// to the key's base (/api/v3), as the route first served them.
func (s *Server) nativeArk(w http.ResponseWriter, r *http.Request, p provider.Provider, call *Call, fail func(int, string)) {
	start, path := call.Time, r.PathValue("path")
	var body []byte
	if r.Method == http.MethodPost {
		in, admitted := s.requestBody(w, r, provider.Chat)
		if !admitted {
			return
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(in))
		dec.UseNumber() // a seed or an id goes as it came
		if err := dec.Decode(&v); err != nil {
			fail(http.StatusBadRequest, fmt.Sprintf("the request isn't JSON: %v", err))
			return
		}
		obj, _ := v.(map[string]any)
		if obj == nil {
			fail(http.StatusBadRequest, "the request is a JSON object, as Ark takes")
			return
		}
		model, _ := obj["model"].(string)
		call.Model = model
		if msg, off := modelOff(p, model, p.ID+"/"+model); model != "" && off {
			fail(http.StatusForbidden, msg)
			return
		}
		// as nativeAny's: one the user set to go unmasked goes as written
		if !p.SkipsRedaction() {
			var hits maskHits
			if err := maskStrings(r.Context(), obj, &hits); err != nil { // fails closed
				fail(maskStatus(err), err.Error())
				return
			}
			if hits.n > 0 {
				b, _ := json.Marshal(obj)
				noteMasked(r, "native", hits, string(b), 0, p.ID, model)
			}
		}
		body, _ = json.Marshal(obj)
	}
	w, done := restoring(w)
	defer done()
	ctx := s.metered(p.Via(r.Context()), p, "")
	at := cmp.Or(strings.TrimRight(p.Base(provider.Responses), "/"), strings.TrimRight(p.Base(provider.Chat), "/")) + "/" + path
	if q := r.URL.RawQuery; q != "" {
		at += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, at, bytes.NewReader(body))
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	stampUA(ctx, p, req.Header)
	// the headers the user gave the model the body names
	ctx = provider.WithModel(ctx, p.ID, call.Model)
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		fail(http.StatusBadGateway, fmt.Sprintf("%s didn't answer: %v", p.Name, err))
		return
	}
	defer res.Body.Close()
	call.Status, call.Millis = res.StatusCode, time.Since(start).Milliseconds()
	if res.StatusCode >= 400 {
		call.Error = fmt.Sprintf("%s answered %d", p.Name, res.StatusCode)
	}
	s.record(*call)
	appendUsage(r, usage.Record{Operation: "native", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: call.Model, Requested: call.Model, ProviderAccount: accountOf(p),
		Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
	for _, k := range []string{"Content-Type", "Cache-Control", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	// a stream goes on as it comes
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil { // io.EOF, or the vendor gone
			return
		}
	}
}

// nativeKeyed: p is reached with an API key of the user's, a vendor's or
// their own provider's: no subscription, OAuth sign-in or plugin account
// (Account), no Vertex AI (a Google token, not a key) and no remote
// magpie (its key is a gateway key, and its own routes are magpie's).
func nativeKeyed(p provider.Provider) bool {
	return p.Account == nil && p.Key != "" && !p.IsRemoteMagpie() && !p.IsVertex()
}

// nativeProto is the protocol whose base names p's host and whose
// signing p's key is sent with: OpenAI's (Chat or Responses) where p
// speaks it, else Anthropic's, else Gemini's.
func nativeProto(p provider.Provider) provider.Protocol {
	for _, pr := range []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic, provider.Gemini} {
		if p.Base(pr) != "" {
			return pr
		}
	}
	return provider.Chat
}

// schemeLike is a first segment that reads as a URL's scheme ("http:",
// "javascript:x").
var schemeLike = lazyre.New(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// nativeTarget is raw, the path after the provider as the caller escaped
// it, at base's host: scheme and host from base, nothing of base's path.
// It refuses what could leave the host or climb out of the path: an
// empty path, a "." or ".." segment (encoded too, %2e%2e, and across an
// encoded slash), a backslash, a second leading slash, a scheme or "://".
func nativeTarget(base, raw, query string) (*url.URL, error) {
	b, err := url.Parse(base)
	if err != nil || b.Host == "" || (b.Scheme != "http" && b.Scheme != "https") {
		return nil, fmt.Errorf("the provider's base URL %q names no host", base)
	}
	path, err := url.PathUnescape(raw)
	if err != nil {
		return nil, fmt.Errorf("the path isn't escaped right: %v", err)
	}
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") || strings.Contains(path, "://") {
		return nil, errors.New("the path is the provider's own, after its host: one that is empty, starts with a slash, holds a backslash or names a URL is refused")
	}
	segs := strings.Split(path, "/")
	if schemeLike.MatchString(segs[0]) {
		return nil, errors.New("the path is the provider's own, after its host: an absolute URL is refused")
	}
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return nil, errors.New(`the path is the provider's own, after its host: a "." or ".." segment is refused`)
		}
	}
	u := &url.URL{Scheme: b.Scheme, Host: b.Host, Path: "/" + path, RawPath: "/" + raw, RawQuery: query}
	// the URL as it goes out names base's host, whatever the path held
	if back, err := url.Parse(u.String()); err != nil || back.Host != b.Host || back.Scheme != b.Scheme {
		return nil, errors.New("the path is the provider's own, after its host: it named another")
	}
	return u, nil
}

// nativeQuery is the caller's query without its gateway key (?key=,
// which callerKeys reads), as it came otherwise.
func nativeQuery(raw string) string {
	if raw == "" || !strings.Contains(raw, "key=") {
		return raw
	}
	var kept []string
	for _, kv := range strings.Split(raw, "&") {
		name, _, _ := strings.Cut(kv, "=")
		if n, err := url.QueryUnescape(name); err == nil && n == "key" {
			continue
		}
		kept = append(kept, kv)
	}
	return strings.Join(kept, "&")
}

// nativeDropped are the caller's headers that never go to the vendor:
// its auth (a gateway key in any place callerKeys reads one, and any
// other credential), the browser's and a proxy's, the hop's own, and
// magpie's; and what a proxy says of the caller (proxyIdentity). The rest
// go as they came.
var nativeDropped = map[string]bool{
	"Authorization": true, "Proxy-Authorization": true, "X-Api-Key": true, "Api-Key": true, "X-Goog-Api-Key": true, "Cookie": true,
	"Host": true, "Origin": true, "Referer": true,
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Content-Length": true, "Accept-Encoding": true, "User-Agent": true,
}

func nativeHeaderDropped(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return nativeDropped[k] || proxyIdentity(k) || strings.HasPrefix(k, "X-Magpie-") || strings.HasPrefix(k, "Sec-")
}

// proxyIdentity says k is what a proxy or tunnel in front of magpie says
// of the caller: its address (every forwardHeaders entry, X-Forwarded-*,
// Cloudflare's Cf-*) or who it is (Tailscale Serve's Tailscale-User-Login,
// the user's email, -Name and -Profile-Pic; a LazyCat box's X-HC-*, the
// box account a signed-in browser or app is, its role, its device and
// when it signed in, which the box's ingress adds to every request it
// passes an app, the gateway's client routes too). It is for magpie to
// judge a request by (proxied), never for a vendor or a server magpie
// passes the request on to.
func proxyIdentity(k string) bool {
	k = http.CanonicalHeaderKey(k)
	if slices.ContainsFunc(forwardHeaders, func(h string) bool { return http.CanonicalHeaderKey(h) == k }) {
		return true
	}
	switch k {
	case "Forwarded", "Via", "X-Real-Ip", "True-Client-Ip", "X-Client-Ip", "X-Original-Forwarded-For":
		return true
	}
	return strings.HasPrefix(k, "X-Forwarded-") || strings.HasPrefix(k, "Cf-") || strings.HasPrefix(k, "Tailscale-") || strings.HasPrefix(k, "X-Hc-")
}

// nativeAnswerDropped are the vendor's answer headers that don't come
// back: the hop's own, its cookies, and what is about its own host.
var nativeAnswerDropped = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true, "Proxy-Authenticate": true,
	"Set-Cookie": true, "Alt-Svc": true, "Strict-Transport-Security": true,
}

// nativeSniffed is the protocol a reply at path is read for usage as.
func nativeSniffed(proto provider.Protocol, path string) provider.Protocol {
	switch {
	case strings.HasSuffix(path, "/responses") || path == "responses":
		return provider.Responses
	case strings.HasSuffix(path, "/messages") || path == "messages":
		return provider.Anthropic
	case strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent"):
		return provider.Gemini
	case proto == provider.Anthropic || proto == provider.Gemini:
		return proto
	}
	return provider.Chat
}

// nativeAny passes any request of the caller's to p's own API, at p's
// host (nativeTarget), as it came.
func (s *Server) nativeAny(w http.ResponseWriter, r *http.Request, p provider.Provider, call *Call, fail func(int, string)) {
	start := call.Time
	if !nativeKeyed(p) {
		fail(http.StatusForbidden, fmt.Sprintf("%s isn't reached with an API key, so its own API isn't passed on: the native route serves a provider added with a key (a vendor's or your own), never a subscription's, a sign-in's or a plugin's account", p.Name))
		return
	}
	proto := nativeProto(p)
	call.To = proto
	// the path after the provider, as the caller escaped it
	rest := strings.TrimPrefix(r.URL.EscapedPath(), NativePath)
	_, raw, _ := strings.Cut(rest, "/")
	at, err := nativeTarget(p.Base(proto), raw, nativeQuery(r.URL.RawQuery))
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	var body []byte
	masked := false
	if r.Method != http.MethodGet {
		in, admitted := s.requestBody(w, r, provider.Chat)
		if !admitted {
			return
		}
		body = in
	}
	if len(body) > 0 {
		trimmed := bytes.TrimSpace(body)
		// the model, read where a JSON body names one, for a model the
		// user turned off and the record
		if len(trimmed) > 0 && trimmed[0] == '{' && len(trimmed) <= 32<<20 {
			var m struct {
				Model any `json:"model"`
			}
			if json.Unmarshal(trimmed, &m) == nil {
				call.Model, _ = m.Model.(string)
			}
		}
		if msg, off := modelOff(p, call.Model, p.ID+"/"+call.Model); call.Model != "" && off {
			fail(http.StatusForbidden, msg)
			return
		}
		if redactionWanted() && !p.SkipsRedaction() {
			var v any
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber() // a seed or an id goes as it came
			if err := dec.Decode(&v); err != nil || dec.More() {
				fail(http.StatusUnsupportedMediaType, "Settings › Privacy masks what goes to a vendor, and magpie masks a JSON body only: this one isn't JSON, so nothing was sent. Send JSON, or set this provider to go unmasked")
				return
			}
			// the value inside a list, so a lone string is masked as well
			box := []any{v}
			var hits maskHits
			if err := maskStrings(r.Context(), box, &hits); err != nil { // fails closed
				fail(maskStatus(err), err.Error())
				return
			}
			if hits.n > 0 {
				v = box[0]
				var b bytes.Buffer
				enc := json.NewEncoder(&b)
				enc.SetEscapeHTML(false)
				if err := enc.Encode(v); err != nil {
					fail(http.StatusBadRequest, err.Error())
					return
				}
				body, masked = bytes.TrimSuffix(b.Bytes(), []byte("\n")), true
				noteMasked(r, "native", hits, string(body), 0, p.ID, call.Model)
			}
		}
	}
	if masked {
		var done func()
		w, done = restoring(w)
		defer done()
	}
	ctx := s.metered(p.Via(r.Context()), p, "")
	req, err := http.NewRequestWithContext(ctx, r.Method, at.String(), bytes.NewReader(body))
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	for k, vs := range r.Header {
		if !nativeHeaderDropped(k) {
			req.Header[k] = append([]string(nil), vs...)
		}
	}
	stampUA(ctx, p, req.Header)
	// the headers the user gave the model the body names
	ctx = provider.WithModel(ctx, p.ID, call.Model)
	if err := p.Sign(ctx, req, proto, body); err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		fail(http.StatusBadGateway, fmt.Sprintf("%s didn't answer: %v", p.Name, err))
		return
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	var sn *usageSniffer
	if strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/event-stream") {
		sn = newSniffer(nativeSniffed(proto, at.Path), ct)
	}
	for k, vs := range res.Header {
		if nativeAnswerDropped[http.CanonicalHeaderKey(k)] || (res.Uncompressed && (k == "Content-Length" || k == "Content-Encoding")) {
			continue
		}
		w.Header()[k] = append([]string(nil), vs...)
	}
	w.WriteHeader(res.StatusCode)
	// as it comes, never held whole: a stream flushed as it arrives, a
	// large file copied through
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if sn != nil {
				sn.write(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			rc.Flush()
		}
		if err != nil { // io.EOF, or the vendor gone
			break
		}
	}
	call.Status, call.Millis = res.StatusCode, time.Since(start).Milliseconds()
	if res.StatusCode >= 400 {
		call.Error = fmt.Sprintf("%s answered %d", p.Name, res.StatusCode)
	}
	var u Usage
	if sn != nil && res.StatusCode < 400 {
		u = sn.usage()
	}
	call.Usage = u
	s.record(*call)
	appendUsage(r, usage.Record{Operation: "native", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: call.Model, Requested: call.Model, Served: u.Served, Upstream: u.Upstream,
		ProviderKeyID: provider.KeyID(p.Key), ProviderKeyName: p.KeyName,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, CacheWrite1h: u.CacheWrite1h, Reasoning: u.Reasoning,
		Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
}
