package gateway

// The service tier a request asked for, what went out on each try and
// what the vendor said it ran at, for the usage log, its CSV and the
// route (#1784). Fast's and Ultrafast's priority processing bills at
// more than the default, and the tier a request went at could change on
// the way: a fast pick asks for priority, and an account whose plan
// doesn't offer Ultrafast is asked for priority in its place.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// servedTierAt is the served tier a reply from host says, as the log keeps
// it. The ChatGPT backend's (CodexBase's host) "auto", in response.created,
// and "default", in response.completed, are what it says whether priority
// ran or not: on gpt-6.1-sol with a Plus account a Fast turn decoded at
// about 200 tok/s and one without at about 110, both "default" (jjt on
// Discord). There they tell nothing, and are kept as unknown (""), never
// as the default tier. OpenAI's API means them, and they are kept as said.
func servedTierAt(host, t string) string {
	if (t == "auto" || t == "default") && host != "" && host == provider.HostOf(provider.CodexBase) {
		return ""
	}
	return t
}

// bodyTier is the service tier a request body in proto asks for, in
// OpenAI's words (service_tier: "priority", "ultrafast", "flex" …);
// Claude's speed "fast" reads as "fast". "" when it asks for none.
func bodyTier(proto provider.Protocol, body []byte) string {
	switch proto {
	case provider.Anthropic:
		if topValue(body, "speed").String() == "fast" {
			return "fast"
		}
		fallthrough
	case provider.Chat, provider.Responses:
		if t := topValue(body, "service_tier"); t.Type == gjson.String {
			return tierWord(t.String())
		}
	}
	return ""
}

// topValue is gjson's value of body's top-level member key. A long body is
// asked through its split (topMember), made once a request: gjson walked
// the whole conversation for a member Codex sends after it (#3394).
func topValue(body []byte, key string) gjson.Result {
	if len(body) >= validMemoMin {
		if v, found, ok := topMember(body, key); ok {
			if !found {
				return gjson.Result{}
			}
			return gjson.ParseBytes(v)
		}
	}
	return gjson.GetBytes(body, key)
}

// tierWord is a tier as the log keeps it: short, and nothing else.
func tierWord(t string) string {
	t = strings.TrimSpace(t)
	if len(t) > 32 {
		return ""
	}
	return t
}

// noteWireTier leaves, where the request's context has a place for it
// (wireAt), the tier body goes out to the vendor at: the last request a
// try sent is the one that answered or failed.
func noteWireTier(ctx context.Context, to provider.Protocol, body []byte) {
	if w, _ := ctx.Value(wireAtKey{}).(*wireAt); w != nil {
		w.tier = bodyTier(to, body)
	}
}

// upstreamTier is the service tier a reply (a JSON body, or one event of
// its stream) says it ran at: a top-level service_tier (a Chat body and
// its chunks, a Responses body), a Responses stream's response.service_tier,
// an Anthropic usage's service_tier. "" when it says none, which is never
// read as the tier that was asked.
func upstreamTier(b []byte) string {
	if !bodyHas(b, `"service_tier"`) {
		return ""
	}
	var v struct {
		Tier     json.RawMessage `json:"service_tier"`
		Response struct {
			Tier json.RawMessage `json:"service_tier"`
		} `json:"response"`
		Usage struct {
			Tier json.RawMessage `json:"service_tier"`
		} `json:"usage"`
		Message struct {
			Usage struct {
				Tier json.RawMessage `json:"service_tier"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{v.Tier, v.Response.Tier, v.Usage.Tier, v.Message.Usage.Tier} {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = tierWord(s); s != "" {
				return s
			}
		}
	}
	return ""
}
