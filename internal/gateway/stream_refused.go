package gateway

import (
	"github.com/yetone/magpie/internal/provider"
)

// A backend that only streams (claude-relay-service's Responses endpoint,
// like Codex's own ChatGPT backend) turns a request that asks for one body
// away: "Stream must be set to true" (Cyrus_4433 on X). An agent that sent
// stream:false got that 400. The request is then translated on the same
// API, which always asks the upstream streamed and gives the client its
// whole reply (translate), and so from then on, as a custom tool turned
// away is (custom_tool.go).

// streamOnlyRefused is how unfit remembers model's backend answering only
// streamed requests.
func streamOnlyRefused(model string) string { return "stream only\x00" + model }

// refusesWhole is whether a 400's body says the request must ask for a
// stream, while the client's body asked for one whole reply.
func refusesWhole(proto provider.Protocol, body, reply []byte) bool {
	return (proto == provider.Chat || proto == provider.Responses) && !streamOf(body) && provider.StreamRequired(wordsOf(reply))
}
