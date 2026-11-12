package provider

import "slices"

// endpointOf is the gateway path a protocol is asked at.
var endpointOf = map[Protocol]string{Chat: "/v1/chat/completions", Responses: "/v1/responses", Anthropic: "/v1/messages"}

// NativeEndpoints are the paths a request for the model is relayed on to
// its provider as it is (/v1/models' and an agent plugin's connect's
// "native_endpoints"): the APIs the provider serves it on. A routing
// group's are GroupEndpoints, which need its members; none here. None
// either for a model every request to is translated anyway (a
// subscription served through its agent's own API).
func NativeEndpoints(e Entry) []string {
	if e.Group != "" {
		return nil
	}
	return endpointsOf(relayed(*e.Provider, e.Model))
}

// relayed are the APIs p takes a request for model on as it is, in the
// order p speaks them: none for a model every request to is translated
// anyway.
func relayed(p Provider, model string) []Protocol {
	if p.Native(model) == "" {
		return nil
	}
	apis := p.APIs(model)
	var out []Protocol
	for _, pr := range p.Speaks() {
		if slices.Contains(Protocols, pr) && (apis == nil || slices.Contains(apis, pr)) {
			out = append(out, pr)
		}
	}
	return out
}

func endpointsOf(prs []Protocol) []string {
	var out []string
	for _, pr := range prs {
		out = append(out, endpointOf[pr])
	}
	return out
}

// GroupEndpoints are the paths a request for a routing group is relayed
// on as it is to whichever of its members ms answers it: the APIs every
// member takes it on. None when a member translates every request anyway,
// or the members share no API.
func GroupEndpoints(ms []Member) []string {
	var common []Protocol
	for i, m := range ms {
		r := relayed(m.Provider, m.Model)
		if i == 0 {
			common = r
			continue
		}
		common = slices.DeleteFunc(common, func(pr Protocol) bool { return !slices.Contains(r, pr) })
	}
	return endpointsOf(common)
}

// GroupAPI is the API an agent that asks each model on one API is wired
// to ask a routing group on, "" for Chat. It is Responses when a member
// is served best there (a ChatGPT sign-in, GPT on OpenAI's API or
// Copilot's, a relay given as a Responses URL) and every other member
// takes a Responses request as it is too, or translates every request
// anyway (a Claude Code subscription): no member is then translated that
// Chat would have relayed, and the ones best on Responses are relayed
// instead of translated from Chat (zyh6036 on X: a group of
// subscriptions that all serve Responses was asked on Chat, as only a
// group whose members were all best there went on Responses, #1594). A
// group with a member Responses doesn't reach (a Chat-only key, a
// Messages-only Claude) stays on Chat, so that member keeps being
// relayed; a Messages-only group stays on Chat too, as the thinking a
// Claude member takes is known by its own model, not by the group's id.
func GroupAPI(ms []Member) Protocol {
	best := false
	for _, m := range ms {
		n := m.Provider.Native(m.Model)
		if n == "" {
			continue
		}
		if !slices.Contains(relayed(m.Provider, m.Model), Responses) {
			return ""
		}
		best = best || n == Responses
	}
	if best {
		return Responses
	}
	return ""
}
