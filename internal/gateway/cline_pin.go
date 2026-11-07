package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// clinePin is body with its model pinned to its maker's own API on the
// Cline API, when the provider asks it (provider.PinUpstream) and Cline
// still takes the pin (provider.ClinePin): glm-5.3-flash, which Cline
// sends through OpenRouter, gets OpenRouter's own provider.only
// (sdytman on Discord). providerOptions, the field that pinned a model
// Cline sends through Vercel's AI Gateway, is never written: Cline
// answers it with 403 since 2026-11-06 (#5442). A body naming provider
// or providerOptions itself is left as the client sent it. The field
// goes at the end, so the prefix a cache keys on doesn't change.
func clinePin(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if to != provider.Chat {
		return body
	}
	up, openRouter := p.ClinePin(gjson.GetBytes(body, "model").String())
	if up == "" || !openRouter || gjson.GetBytes(body, "providerOptions").Exists() || gjson.GetBytes(body, "provider").Exists() {
		return body
	}
	end := bytes.LastIndexByte(body, '}')
	start := bytes.IndexByte(body, '{')
	if end < 0 || start < 0 || start > end {
		return body
	}
	val, _ := json.Marshal(map[string]any{"only": []string{up}})
	out := make([]byte, 0, len(body)+len(val)+24)
	out = append(out, body[:end]...)
	if len(bytes.TrimSpace(body[start+1:end])) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"provider":`...)
	out = append(out, val...)
	return append(out, body[end:]...)
}
