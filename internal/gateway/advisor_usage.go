package gateway

import (
	"cmp"
	"net/http"

	"github.com/yetone/magpie/internal/usage"
)

// advisorRows are the usage rows of the advisor calls a reply says its
// vendor made (Anthropic's advisor tool, run at the vendor on a request
// that goes as it is: advisorTools), one a call, beside rec, the
// executor's own row. Anthropic's top-level usage is the executor's alone
// and bills each advisor_message iteration at the advisor model's rates,
// so a row of the request's model held only part of what was spent: an
// Opus advisor's tokens beside a Sonnet executor were in no row at all
// (#6977, Refeccd on Discord). Each row is under the advisor's model, by
// magpie's id the request named it by (advisedAs), else the vendor's name
// for it, its Served the vendor's; nothing is taken from rec. It is its
// own request, as an advisor call magpie runs itself is (advise): Kind
// says it is the advisor's, and it has the request's route, agent,
// account and session. It has no timing of its own (the vendor's stream
// tells none), and its status is the call's, which the reply listed as
// done, not the executor's.
func advisorRows(rec usage.Record, u Usage) []usage.Record {
	a := u.advised
	if a == nil {
		return nil
	}
	out := make([]usage.Record, 0, len(a.calls))
	for _, c := range a.calls {
		b := c.use
		if b.Input+b.Output+b.CacheRead+b.CacheWrite == 0 {
			continue
		}
		model := cmp.Or(a.model, c.served)
		out = append(out, usage.Record{RouteID: rec.RouteID, Time: rec.Time, Agent: rec.Agent, Via: rec.Via, Provider: rec.Provider, Host: rec.Host,
			ProviderKeyID: rec.ProviderKeyID, ProviderKeyName: rec.ProviderKeyName, ProviderAccount: rec.ProviderAccount,
			Model: model, Requested: model, Served: c.served,
			Input: b.Input, Output: b.Output, CacheRead: b.CacheRead, CacheWrite: b.CacheWrite, CacheWrite1h: b.CacheWrite1h, Reasoning: b.Reasoning,
			Status: http.StatusOK, Session: rec.Session, NativeSession: rec.NativeSession, Kind: usage.KindAdvisor, Endpoint: rec.Endpoint})
	}
	return out
}

// appendAdvised writes rec, then the rows of the advisor calls u says its
// reply made (advisorRows).
func appendAdvised(r *http.Request, rec usage.Record, u Usage) {
	appendUsage(r, rec)
	for _, a := range advisorRows(rec, u) {
		appendUsage(r, a)
	}
}

// advisorRouteUsage is the Routing page's pricing of the advisor calls u
// says its reply made, under provider id, before the request's own (which
// stays last: promptCounted reads the prompt from it).
func advisorRouteUsage(id string, u Usage) []RouteUsage {
	var out []RouteUsage
	for _, a := range advisorRows(usage.Record{Provider: id}, u) {
		out = append(out, RouteUsage{Provider: id, Model: a.Model, Input: a.Input, Output: a.Output,
			CacheRead: a.CacheRead, CacheWrite: a.CacheWrite, Reasoning: a.Reasoning})
	}
	return out
}
