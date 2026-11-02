package gateway

// A decision group (provider/decide_group.go) is asked at /v1/systemone as
// a chat group is asked a conversation (ARNO on Discord: 给decision模型也加
// 上group功能): "group/<id>", or the group's id or name, goes to its models'
// keys and accounts in the group's routing order, and one that fails as a
// chat request would fail over (retryable) rests, the next asked, each try
// in the routing log.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// decisionGroupOf is the decision group a System One request's model names:
// "group/<id>", or a group's id or name as GroupFor takes it. ok is false
// when it names none, and the model goes to RouteDecider; err is said to
// the caller when it names a group System One can't ask (a chat group, or
// one switched off or gone), with its status.
func decisionGroupOf(asked string) (g provider.Group, ms []provider.Member, ok bool, status int, err error) {
	ref := strings.TrimSpace(asked)
	explicit := provider.IsGroupRef(ref)
	if !explicit {
		gid, found := provider.GroupFor(ref)
		if !found {
			return g, nil, false, 0, nil
		}
		ref = gid
	}
	g, ms, found := provider.FindGroup(ref)
	if !found {
		if !explicit {
			return g, nil, false, 0, nil
		}
		if off, isOff := provider.DisabledGroup(ref); isOff {
			return g, nil, false, http.StatusNotFound, fmt.Errorf("the routing group %s is switched off in Magpie, so %q is not served; switch it on again on Magpie's Routing page to use it", off.Name, asked)
		}
		return g, nil, false, http.StatusNotFound, fmt.Errorf("magpie has no routing group %q", strings.TrimPrefix(ref, provider.GroupPrefix))
	}
	if !g.Decides() {
		if !explicit {
			return provider.Group{}, nil, false, 0, nil // a chat group by that name: a model of it may still be one
		}
		return g, nil, false, http.StatusBadRequest, fmt.Errorf("the routing group %s holds models that hold conversations; System One asks a group of decision models, or a decision model as \"provider/model\"", g.Name)
	}
	return g, ms, true, 0, nil
}

// decideTries are the keys and accounts of a decision group's models, in
// the group's routing order — those resting after the rest, as for a
// chat — with the models of it that answer no System One left out. left
// and order are what the routing log shows of the plan.
func (s *Server) decideTries(g provider.Group, ms []provider.Member) ([]candidate, planned, []provider.Member) {
	ms = slices.DeleteFunc(slices.Clone(ms), func(m provider.Member) bool { return !m.Provider.DecidesModel(m.Model) })
	if len(ms) == 0 {
		return nil, planned{}, nil
	}
	cs, pl := s.planGroup(g, ms, provider.Chat)
	return cs, pl, ms
}

// decideFailed rests a candidate whose System One call failed as status,
// header and body say, the way a chat's failed try rests: not when the
// request itself was at fault (its shape, its prompt, its content), which
// would fail the same at any. It returns why it failed, and its rest when
// it rests.
func (s *Server) decideFailed(c candidate, status int, header http.Header, body []byte) (string, *Rest) {
	if shapeRefused(status, body) || promptRefused(status, body) || protectionRefused(status, body) || zdrRefused(status, body) || !retryable(status, body) {
		return failureOf(c, status, body), nil
	}
	rest := s.restAfter(c, status, header, body)
	return rest.Why, &rest
}

// serveDecisionGroup answers a System One request to the decision group g
// (decisionGroupOf), its models ms: each of the group's keys and accounts
// in turn until one answers, or the last one's answer passed on.
func (s *Server) serveDecisionGroup(w http.ResponseWriter, r *http.Request, body []byte, asked string, g provider.Group, ms []provider.Member) {
	start := time.Now()
	if refuseOff(w, provider.Chat, asked) { // every model of it disabled (#1867)
		return
	}
	g = g.Live()
	r = heldByGroup(r, g) // held to the group's accounts, if it is
	keyWho, keyHeld := keyHolds(r)
	accWho, accHeld := accountHolds(r)
	// a gateway key held to some models (#882) names the group, or the
	// models of it it may use
	if keyHeld && !groupAllowed(keyWho, g, ms) {
		writeError(w, provider.Chat, http.StatusForbidden, keyModelError(keyWho, asked))
		return
	}
	// a model a pause rule holds for now is out, as for a chat
	ms, paused := provider.PausedOut(g, ms, agentOf(r), "", ruleClock())
	if len(ms) == 0 && len(paused) > 0 {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, pausedError(asked, paused))
		return
	}
	cs, pl, ms := s.decideTries(g, ms)
	if len(ms) == 0 {
		writeError(w, provider.Chat, http.StatusNotFound, fmt.Sprintf("no decision model of the group %s can be asked now: their providers are switched off or gone", g.Name))
		return
	}
	if keyHeld || accHeld {
		who := keyWho
		if !keyHeld {
			who = accWho
		}
		var members map[string]bool
		if keyHeld {
			members = groupKeeps(keyWho, g, ms)
		}
		var held bool
		if cs, pl, held = allowedCandidates(who, cs, pl, members); len(cs) == 0 && held {
			writeError(w, provider.Chat, http.StatusForbidden, keyAccountsError(who, asked))
			return
		}
	}
	cs = probeOne(cs) // of those resting, only the one back soonest (#1799)
	if len(cs) == 0 {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, fmt.Sprintf("no key or account of the group %s's decision models may serve them now", g.Name))
		return
	}
	s.serveDecideTries(w, r, body, asked, start, cs, pl, groupRef(g, ms))
}

// decideKeys are a decision provider's keys or accounts that may be asked
// model, in its own routing's order (Smart, In order, In turn…), those
// resting after a failure behind the rest — planned as a chat's are, its
// fallbacks left out: a provider of several keys asked by itself, as a
// group's classifier or at /v1/systemone, fails over among them as a
// decision group's members do (fflake33 on Discord: a TypeSafe Jev key
// out of quota was asked on every turn, the next never).
func (s *Server) decideKeys(p provider.Provider, model string) ([]candidate, planned) {
	p.Fallback = nil
	return s.plan(p, model, provider.Chat)
}

// serveDecideTries answers a System One request with cs, a decision
// group's keys and accounts or one decision provider's, each in turn until
// one answers, the last one's answer passed on: one that fails as a chat's
// try would rests, and every try is a Try of one route (group is the
// group it was asked by, nil for a provider).
func (s *Server) serveDecideTries(w http.ResponseWriter, r *http.Request, body []byte, asked string, start time.Time, cs []candidate, pl planned, group *GroupRef) {
	who := callerOf(r)
	// the call is in Recent calls, and with the request archive on is kept
	// there, as a chat's is (wangb on Discord: 请求存档开启, 但是 systemone
	// 的请求没有记录下来): the route names it from the start, as the
	// Routing page reads it once Recent calls has let it go
	capture := &captureResponseWriter{ResponseWriter: w}
	w = capture
	call := Call{Time: start, From: provider.Chat, To: provider.Chat, Model: asked, Provider: cs[0].p.ID, Agent: who.agent, Via: who.via,
		wire: archiving(r, capture, start, body)}
	call.RequestBody, call.RequestTruncated = captureRequestBody(body)
	defer discardArchive(capture)
	tr := s.trace.beginFor(r, Route{Time: start, Agent: who.agent, Session: sessionOf(r.Header), Model: asked, Provider: cs[0].p.ID,
		Group: group, Order: pl.order, Left: pl.left, Archive: call.archiveName()})
	call.Route = tr.ID
	defer func() {
		call.ResponseBody, call.ResponseTruncated = capture.body.text(), capture.body.truncated
		call.Millis = time.Since(start).Milliseconds()
		s.record(call)
	}()
	var skipped []string
	for i, c := range cs {
		last := i == len(cs)-1
		at := time.Now()
		s.trace.update(tr, func(t *Route) {
			t.Provider = c.p.ID
			t.Tries = append(t.Tries, Try{ID: c.rest, Model: c.model, Start: at})
		})
		// each try ends as it went, and the route with the last
		tried := func(status int, msg, fail string, rest *Rest, used Usage) {
			ms := time.Since(at).Milliseconds()
			s.trace.update(tr, func(t *Route) {
				try := &t.Tries[len(t.Tries)-1]
				try.Done, try.Status, try.Millis, try.Error, try.Fail, try.Rest = true, status, ms, msg, fail, rest
				if status < 400 || last || r.Context().Err() != nil {
					t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, time.Since(start).Milliseconds(), used.Input+used.Output
					t.Usage = routeUsage(c.p.ID, c.model, used)
				}
			})
		}
		d, err := s.postDecideSent(r.Context(), c.p, c.model, withModel(body, c.model))
		status, b, header := d.status, d.answer, d.header
		call.Provider = c.p.ID
		if err != nil {
			fail, rest := failOther, (*Rest)(nil)
			var tooMany *errRPM
			if r.Context().Err() == nil && !errors.As(err, &tooMany) {
				// unreachable: it rests, as a chat's would
				fail, rest = s.decideFailed(c, http.StatusBadGateway, nil, []byte(err.Error()))
			}
			if !last && r.Context().Err() == nil {
				tried(http.StatusBadGateway, err.Error(), fail, rest, Usage{})
				skipped = append(skipped, c.label()+": "+err.Error())
				continue
			}
			last = true
			tried(http.StatusBadGateway, err.Error(), fail, rest, Usage{})
			msg := err.Error()
			if len(skipped) > 0 {
				msg += " (tried first: " + strings.Join(skipped, "; ") + ")"
			}
			call.Status, call.Error = http.StatusBadGateway, msg
			writeError(w, provider.Chat, http.StatusBadGateway, msg)
			return
		}
		var use struct {
			Model string `json:"model"` // the one that answered, when the reply says
			Usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(b, &use)
		used := Usage{Input: use.Usage.Input, Output: use.Usage.Output}
		errMsg, fail := "", ""
		var rest *Rest
		if status >= 300 {
			errMsg = provider.APIError(b, fmt.Sprintf("%d %s", status, http.StatusText(status)))
		}
		if status >= 400 {
			fail, rest = s.decideFailed(c, status, header, b)
			if !last && retryable(status, b) {
				tried(status, errMsg, fail, rest, used)
				skipped = append(skipped, fmt.Sprintf("%s: %s", c.label(), errMsg))
				continue
			}
		}
		last = true
		took := time.Since(start).Milliseconds() // before what magpie writes down of it (#1939)
		tried(status, errMsg, fail, rest, used)
		if status < 400 {
			servedCandidate(c, used.Input+used.Output)
		}
		var keyID, keyName string
		if c.p.Account == nil && c.p.Key != "" {
			keyID, keyName = provider.KeyID(c.p.Key), c.p.KeyName
		}
		call.Status, call.Error, call.Fallback = status, errMsg, strings.Join(skipped, "; ")
		call.Usage = Usage{Input: use.Usage.Input, Output: use.Usage.Output, Served: use.Model}
		appendUsage(r, usage.Record{RouteID: tr.ID, Time: start, Agent: who.agent, Via: who.via, Provider: c.p.ID, Host: c.p.Where(), Model: c.model, Requested: asked, Served: use.Model,
			ProviderKeyID: keyID, ProviderKeyName: keyName, ProviderAccount: accountOf(c.p),
			Input: use.Usage.Input, Output: use.Usage.Output, Millis: took, Status: status, Archive: call.archiveName()})
		ctype := header.Get("Content-Type")
		if ctype == "" || status < 300 {
			ctype = "application/json"
		}
		if status >= 400 {
			keepRetryOut(w.Header(), header, b)
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		w.Write(b)
		return
	}
}

// decideRefused is a System One call a decision provider answered with a
// failure: its status, headers and body, for a decision group's classifier
// to rest the one that failed as a chat's try would rest.
type decideRefused struct {
	err    error
	status int
	header http.Header
	body   []byte
}

func (e decideRefused) Error() string { return e.err.Error() }
func (e decideRefused) Unwrap() error { return e.err }

// askDecisionGroup is askClassifier for a decision group: its keys and
// accounts in the group's order, each asked as Jev is (askJev) until one
// answers, the one that failed resting as it would for a chat. ok is false
// when model names no decision group.
func (s *Server) askDecisionGroup(wait time.Duration, model string, intents []string, prev before, effort bool, text string) (v verdict, ok bool, err error) {
	if !provider.IsGroupRef(model) {
		return verdict{}, false, nil
	}
	g, ms, found := provider.FindGroup(model)
	if !found || !g.Decides() {
		return verdict{}, false, nil
	}
	g = g.Live()
	ms, _ = provider.PausedOut(g, ms, RouterAgent, "", ruleClock())
	cs, _, _ := s.decideTries(g, ms)
	// held to the group's own accounts, if it is: no request carries a
	// caller here to hold (heldByGroup)
	cs = slices.DeleteFunc(cs, func(c candidate) bool { return groupHeld(c, []provider.Group{g}) != "" })
	cs = probeOne(cs) // of those resting, only the one back soonest (#1799)
	if len(cs) == 0 {
		return verdict{}, true, fmt.Errorf("no decision model of the group %s can be asked now", g.Name)
	}
	v, err = s.askDecideTries(wait, cs, intents, prev, effort, text)
	return v, true, err
}

// askDecider is askClassifier for one decision provider's model: its keys
// or accounts in its routing's order (decideKeys), each asked until one
// answers, as a decision group's are.
func (s *Server) askDecider(wait time.Duration, p provider.Provider, model string, intents []string, prev before, effort bool, text string) (verdict, error) {
	cs, _ := s.decideKeys(p, model)
	cs = probeOne(cs) // of those resting, only the one back soonest (#1799)
	if len(cs) == 0 {
		return verdict{}, fmt.Errorf("no key or account of %s may be asked %s now", p.Name, model)
	}
	return s.askDecideTries(wait, cs, intents, prev, effort, text)
}

// askDecideTries asks each of cs as Jev is (askJev) until one answers, the
// one that failed resting as it would for a chat; the last one's error
// when none does. Every try shares the one classifier's wait, as Settings
// says it is given, its failover included: a try each given the whole
// wait held a request N waits before routing it (Jason on Discord, a 2m
// hold). wait is that wait, or what a request has left of it (askWaited):
// a try cut off by less than the whole wait is not rested for it.
func (s *Server) askDecideTries(wait time.Duration, cs []candidate, intents []string, prev before, effort bool, text string) (v verdict, err error) {
	full := classifyTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for _, c := range cs {
		if ctx.Err() != nil {
			break // the wait is spent: the rest are not asked, nor rested
		}
		if v, err = s.askJev(ctx, wait, c.p, c.model, intents, prev, effort, text); err == nil {
			servedCandidate(c, 0)
			return v, nil
		}
		var refused decideRefused
		var answer answerError
		switch {
		case wait < full && ctx.Err() != nil && !errors.As(err, &refused) && !errors.As(err, &answer):
			// cut off by the request's wait running out, not its own
			return verdict{}, cutShort(c.p.Name+"/"+c.model, wait, full)
		case errors.As(err, &refused):
			s.decideFailed(c, refused.status, refused.header, refused.body)
		case errors.As(err, &answer), errors.Is(err, context.Canceled):
			// it answered, not in System One's shape: nothing to rest for
		default:
			s.decideFailed(c, http.StatusBadGateway, nil, []byte(err.Error()))
		}
	}
	return verdict{}, err
}
