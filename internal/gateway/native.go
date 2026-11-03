package gateway

// The native route: a provider's own API, reached through magpie
// (axelburks on Discord). /v1/images/generations and /v1/videos take
// OpenAI's shape and pass on the vendor fields they don't read, but a
// vendor's API grows faster than magpie maps it: Ark's image streams, its
// task list and cancel, a field of a new model. A caller who wants all of
// it asks
//
//	POST /v1/magpie/native/<provider>/<path>
//
// and gets the vendor's answer as it came, its stream too. What it keeps
// of the gateway:
//
//   - the gateway's own guards first, as every route (a key from beyond
//     loopback, a read-only key refused, corsGuard, the key's rate);
//   - a key held to models, accounts or a spend limit is refused: the
//     route can't say which model a task's poll or cancel is of, and
//     magpie prices no answer it doesn't read, so it can't hold such a key
//     to them. Such a key uses the OpenAI routes;
//   - only paths the vendor makes images and videos at (nativePaths), on
//     the methods the vendor takes there: never its chat, whose routing,
//     usage and masking are the chat routes';
//   - the body's text masked as a video's fields are (maskStrings, fails
//     closed), and put back in the answer;
//   - the provider's first key signs it, the user's key never seen by the
//     caller; a model the user turned off is refused;
//   - each request is in the Requests log and in usage, untimed and
//     uncounted in tokens, as a video's start is.
//
// Only Volcengine Ark is served so far: the one asked for, and the one
// whose API magpie's own routes read. Another provider is told so.

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// NativePath is where a provider's own API is reached.
const NativePath = "/v1/magpie/native/"

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
	call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Provider: id, headers: headerLogOf(r.Context())}
	usage.Saw(agentOf(r))
	fail := func(code int, msg string) {
		call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
		writeError(w, provider.Chat, code, msg)
		s.record(call)
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
	if p.IsRemoteMagpie() || !isArk(p) {
		fail(http.StatusNotFound, fmt.Sprintf("%s's own API isn't passed on: magpie passes on Volcengine Ark's (a provider at ark.<region>.volces.com) so far. Use /v1/images/generations and /v1/videos", p.Name))
		return
	}
	if !nativeArkPath(r.Method, path) {
		fail(http.StatusNotFound, fmt.Sprintf("magpie passes on Ark's images and video tasks: POST images/generations, POST or GET contents/generations/tasks, GET or DELETE contents/generations/tasks/<id>; not %s %s", r.Method, path))
		return
	}
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
		var hits maskHits
		if err := maskStrings(r.Context(), obj, &hits); err != nil { // fails closed
			fail(maskStatus(err), err.Error())
			return
		}
		if hits.n > 0 {
			b, _ := json.Marshal(obj)
			noteMasked(r, "native", hits, string(b), 0, p.ID, model)
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
	s.record(call)
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
