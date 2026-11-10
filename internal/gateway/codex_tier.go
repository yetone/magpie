package gateway

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// codexTiered is the body of a request of Codex's with the service tier
// the user set for Codex (provider.CodexServiceTier, JackyFill on X) in
// place of the one Codex asked: none for the standard tier, "priority"
// (Codex's Fast) or "ultrafast" (its Ultra Fast). Unset, or another
// agent's request, goes as it came. The tier is then served as Codex's own
// ask would be: an account whose plan has no Ultrafast is put after one
// that has, or asked for priority (serve), a provider added by its address
// that turns a tier away is not sent it again (fits). One of Codex's own
// models no account in magpie lists Ultrafast on is asked for priority
// instead, as it may go on to Codex's own sign-in, whose plan magpie
// doesn't know.
func codexTiered(r *http.Request, body []byte, model string) []byte {
	set := provider.CodexServiceTier()
	if set == "" || !fromCodex(r.Header) && agentOf(r) != "codex" {
		return body
	}
	if set == "ultrafast" && !strings.Contains(model, "/") && !provider.UltraModels()[model] {
		set = "priority"
	}
	if set == settings.CodexTierStandard {
		set = ""
	}
	return withServiceTier(body, set)
}

// withServiceTier is body with its top-level service_tier set to tier, or
// without one for "", every other byte as it came. A body that isn't a
// JSON object goes as it came.
func withServiceTier(body []byte, tier string) []byte {
	cur, found, ok := topMember(body, tierField)
	if !ok || !found && tier == "" {
		return body
	}
	want := []byte(`"` + tier + `"`)
	if found && bytes.Equal(bytes.TrimSpace(cur), want) {
		return body
	}
	b := withoutTopMember(body, tierField)
	if tier == "" {
		return b
	}
	i := bytes.IndexByte(b, '{')
	if i < 0 {
		return body
	}
	sep := ","
	if j := skipJSONSpace(b, i+1); j < len(b) && b[j] == '}' {
		sep = ""
	}
	return insertAt(b, i+1, `"`+tierField+`":`+string(want)+sep)
}
