package gateway

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Betas an agent asks of Anthropic's messages (the anthropic-beta header,
// or an anthropic_beta list in the body) go on to the provider, save those
// it is known to turn away: a provider that checks them by name (Bedrock's
// runtime: 400 Unexpected value(s) `x`, `y` for the `anthropic-beta`
// header, #176) refuses the whole request for one it doesn't know.

// bedrockBetas are betas of Anthropic's own API that Bedrock names in
// another way: Claude Code asks tool search as advanced-tool-use-2025-11-20
// of Anthropic and as tool-search-tool-2025-10-19 of Bedrock and Vertex.
var bedrockBetas = map[string]string{
	"advanced-tool-use-2025-11-20": "tool-search-tool-2025-10-19",
}

// bedrockRefuses are betas Bedrock has turned away (#176), left out from
// the first request rather than learned from a refusal.
var bedrockRefuses = []string{
	"prompt-caching-scope-2026-01-05",
	"redact-thinking-2026-02-12",
}

// askedBetas are the betas an agent asked in its anthropic-beta header,
// but for its own sign-in's: Claude Code signed in to claude.ai asks
// oauth-2025-04-20, which names a credential that never leaves magpie, so
// a provider gets the request as magpie's key sends it.
func askedBetas(in http.Header) []string {
	var out []string
	for _, v := range in.Values("anthropic-beta") {
		for _, b := range strings.Split(v, ",") {
			if b = strings.TrimSpace(b); b != "" && !strings.HasPrefix(b, "oauth-") {
				out = append(out, b)
			}
		}
	}
	return out
}

// betaKey is a beta a provider refused, as remembered in unfit.
func betaKey(beta string) string { return "anthropic-beta\x00" + beta }

// betas is the list asked (header values, each maybe comma-separated),
// fitted to what p takes: renamed and left out as Bedrock wants, without
// those p has refused, and without repeats.
func (s *Server) betas(p provider.Provider, asked []string) []string {
	var out []string
	for _, v := range asked {
		for _, b := range strings.Split(v, ",") {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			if p.IsBedrock() {
				if to, ok := bedrockBetas[b]; ok {
					b = to
				}
				if slices.Contains(bedrockRefuses, b) {
					continue
				}
			}
			if !s.fits(p.ID, betaKey(b), provider.Anthropic) || slices.Contains(out, b) {
				continue
			}
			out = append(out, b)
		}
	}
	return out
}

// fitUserBetas fits the anthropic-beta a signed request carries when the
// provider has one of the user's own (header.anthropic-beta), which Sign
// added after the agent's: a beta of the user's is left out only once the
// provider has refused it, as the agent's are, and the retry goes without.
// The header keeps the name as the user wrote it.
func (s *Server) fitUserBetas(p provider.Provider, h http.Header) {
	if !slices.ContainsFunc(slices.Collect(maps.Keys(p.Headers)), provider.ListHeader) {
		return
	}
	for k, vs := range h {
		if !provider.ListHeader(k) {
			continue
		}
		if bs := s.betas(p, vs); len(bs) > 0 {
			h[k] = []string{strings.Join(bs, ",")}
		} else {
			delete(h, k)
		}
	}
}

// bodyBetas fits a body's anthropic_beta list, as Bedrock's InvokeModel
// takes betas, the way betas fits the header's.
func (s *Server) bodyBetas(p provider.Provider, body []byte) []byte {
	if !bytes.Contains(body, []byte(`"anthropic_beta"`)) {
		return body
	}
	list := gjson.GetBytes(body, "anthropic_beta")
	if !list.IsArray() {
		return body
	}
	var asked []string
	for _, b := range list.Array() {
		asked = append(asked, b.String())
	}
	kept := s.betas(p, asked)
	if slices.Equal(kept, asked) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	if len(kept) == 0 {
		delete(m, "anthropic_beta")
	} else {
		m["anthropic_beta"] = kept
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// unexpectedBetas is the error of a provider that turns away betas it
// doesn't know: Unexpected value(s) `a`, `b` for the `anthropic-beta`
// header (Bedrock's), and its word for the body's anthropic_beta.
var unexpectedBetas = regexp.MustCompile("(?i)unexpected values?(?:\\(s\\))?\\s+((?:`[^`]+`[,\\s]*(?:and\\s+)?)+)for the\\s+`anthropic[-_]beta`")

var backquoted = regexp.MustCompile("`([^`]+)`")

// refuseBetas reads the betas an error from p names as unknown, remembers
// them so p isn't asked them again, and returns those it hadn't already.
func (s *Server) refuseBetas(p provider.Provider, errBody []byte) []string {
	m := unexpectedBetas.FindSubmatch(errBody)
	if m == nil {
		return nil
	}
	var fresh []string
	for _, q := range backquoted.FindAllSubmatch(m[1], -1) {
		b := strings.TrimSpace(string(q[1]))
		if b != "" && s.fits(p.ID, betaKey(b), provider.Anthropic) {
			s.markUnfit(p.ID, betaKey(b), provider.Anthropic)
			fresh = append(fresh, b)
		}
	}
	return fresh
}
