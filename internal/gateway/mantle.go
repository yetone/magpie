package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Amazon Bedrock's Mantle endpoint (@marktoown on X) serves its OpenAI APIs
// under two prefixes, each model at one: most at /v1, OpenAI's closed GPT
// models and xAI's Grok at /openai/v1, as each model's card says. A model
// asked at the other is turned away with "model `x` isn't supported on
// this route". provider.MantleBase picks the prefix by the model's id;
// a model it guessed wrong (a maker the rule doesn't know yet) is asked
// again at the other one, and from then on there.

// mantleRoutes is whether a request of p's on to is placed by MantleBase:
// Mantle's chat completions and Responses. Its Messages are at /anthropic
// alone.
func mantleRoutes(p provider.Provider, to provider.Protocol) bool {
	return (to == provider.Chat || to == provider.Responses) && p.IsBedrockMantle()
}

// mantleRouteKey is how unfit remembers that Mantle serves model at the
// prefix MantleBase doesn't guess for it.
func mantleRouteKey(model string) string { return "mantle-route:" + model }

// mantleWrongRoute is Mantle's refusal of a model asked at the prefix that
// doesn't serve it.
var mantleWrongRoute = lazyre.New(`(?i)(?:isn't|is not|not) supported on this route`)

// mantleReroute asks a request Mantle refused for its route again at the
// other prefix, once, and remembers that prefix for the model when it is
// taken there. Any other answer is given back as it came.
func (s *Server) mantleReroute(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header, res *http.Response) (*http.Response, error) {
	model := gjson.GetBytes(body, "model").String()
	if res.StatusCode < 400 || res.StatusCode >= 500 || model == "" || !s.fits(p.ID, mantleRouteKey(model), to) {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if !mantleWrongRoute.Match(b) {
		return res, nil
	}
	s.markUnfit(p.ID, mantleRouteKey(model), to)
	again, err := s.forwardOnce(ctx, p, to, path, body, in)
	if err == nil && again.StatusCode < 400 {
		return again, nil
	}
	var ab []byte
	if err == nil {
		ab, _ = io.ReadAll(io.LimitReader(again.Body, 1<<20))
		again.Body.Close()
		again.Body = io.NopCloser(bytes.NewReader(ab))
	}
	if err == nil && !mantleWrongRoute.Match(ab) {
		// served there, and refused for something else: that answer
		return again, nil
	}
	// served at neither prefix (a model this account or region hasn't):
	// the guess stays, and the client gets the first refusal
	s.mu.Lock()
	delete(s.unfit, p.ID+"\x00"+mantleRouteKey(model)+"\x00"+string(to))
	s.mu.Unlock()
	return res, nil
}
