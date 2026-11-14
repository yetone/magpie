package gateway

// The service tier a request asked for, what went out on each try and
// what the vendor said it ran at, for the usage log, its CSV and the
// route (#1784). Fast's and Ultrafast's priority processing bills at
// more than the default, and the tier a request went at could change on
// the way: a fast pick asks for priority, and an account whose plan
// doesn't offer Ultrafast is asked for priority in its place.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
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

// A reply passed back to another magpie (a Remote magpie provider there)
// says the tier the request went to its vendor at here, and whether the
// vendor's own word in the body tells anything. That magpie records them
// as its own: its request goes to this one at the tier it asked, this
// one's Fast or a key's tier may send another, and the body's
// "default" from the ChatGPT backend is no tier at all (JackyFill on X: a
// Codex turn sent as Fast by the remote magpie was recorded "default" on
// the magpie Codex asked).
const (
	// tierSentHeader is the tier the reply's request went to the vendor
	// at, as the log keeps it (Record.TierSent); tierNone for none
	tierSentHeader = "X-Magpie-Tier-Sent"
	// tierUnsaidHeader is "1" when the reply's "auto"/"default" tell
	// nothing (servedTierAt)
	tierUnsaidHeader = "X-Magpie-Tier-Unsaid"
	tierNone         = "none"
)

// stampTier says on h, a try's reply header to another magpie, the tier
// sent went to p at, and whether p's "auto"/"default" tell nothing. Those
// p is a remote magpie that said them stand: they are its vendor's.
func stampTier(h http.Header, p provider.Provider, sent string) {
	if !p.IsRemoteMagpie() || h.Get(tierSentHeader) == "" {
		h.Set(tierSentHeader, cmp.Or(sent, tierNone))
	}
	if !p.IsRemoteMagpie() {
		h.Del(tierUnsaidHeader)
	}
	if servedTierAt(p.VendorHost(), "default") == "" {
		h.Set(tierUnsaidHeader, "1")
	}
}

// tierThere adds to passed, the headers of a remote magpie's reply that go
// to the try, what it said of its tier (stampTier). Only a remote magpie's
// are read: a vendor's X-Magpie- headers are never taken.
func tierThere(p provider.Provider, got, passed http.Header) http.Header {
	if !p.IsRemoteMagpie() {
		return passed
	}
	sent, unsaid := tierWord(got.Get(tierSentHeader)), got.Get(tierUnsaidHeader) == "1"
	if sent == "" && !unsaid {
		return passed
	}
	if passed == nil {
		passed = http.Header{}
	}
	if sent != "" {
		passed.Set(tierSentHeader, sent)
	}
	if unsaid {
		passed.Set(tierUnsaidHeader, "1")
	}
	return passed
}

// sentThere is the tier a remote magpie's reply says it sent its vendor
// (tierThere); ok false when it said none, as a magpie before it didn't.
func (h *holdWriter) sentThere() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.header.Get(tierSentHeader)
	if v == tierNone {
		return "", true
	}
	return v, v != ""
}

// servedThere is t, the tier a reply from p said, unknown ("") where p is
// a remote magpie whose vendor's "auto"/"default" tell nothing.
func servedThere(w http.ResponseWriter, p provider.Provider, t string) string {
	if t != "auto" && t != "default" || !p.IsRemoteMagpie() {
		return t
	}
	if hw, ok := w.(*holdWriter); ok {
		hw.mu.Lock()
		unsaid := hw.header.Get(tierUnsaidHeader) == "1"
		hw.mu.Unlock()
		if unsaid {
			return ""
		}
	}
	return t
}
