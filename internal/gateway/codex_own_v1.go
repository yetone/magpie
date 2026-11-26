package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexSignInToken is Codex's ChatGPT sign-in as Codex keeps it, which
// tests set.
var codexSignInToken = provider.CodexSignInAsKept

// codexOwnOffMagpie is the request a Codex on this computer sent to /v1
// for one of its own models that magpie serves nothing for, put on the
// ChatGPT backend's route (CodexPath) and signed with Codex's own ChatGPT
// sign-in, as Codex's built-in provider would have sent it. A thread
// keeps the provider it was started on: one started while magpie was
// Codex's provider (its API, or the takeover of a used-up account) goes
// on sending to /v1 after Codex is disconnected or set back to ChatGPT,
// and an official model picked in it (gpt-6.1-sol) met "magpie knows no
// model" whenever no ChatGPT account in magpie serves it (iuengei on
// Discord). nil when that isn't the case: a model magpie serves, switched
// off, disabled or a group; a model not OpenAI's; a request from another
// computer or held by its gateway key. Codex's sign-in not to be had
// (subscription accounts off in magpie, signed out, expired), why is what
// the request is answered instead of the bare 404: what sends it here and
// what to do.
func codexOwnOffMagpie(r *http.Request, body []byte, model string) (to *http.Request, why string) {
	if model == "" || strings.Contains(model, "/") || !local(r) {
		return nil, ""
	}
	// one of Codex's hidden side calls on its own model that a setting
	// sends elsewhere — a thread's title or description, a composer
	// prediction, the app's prompt suggestions — is magpie's to answer where it stands (codexTitlesTo
	// and its siblings, further down handle), never to put on Codex's
	// ChatGPT sign-in: with no sign-in to be had the request was answered
	// 404 and Codex named the thread after its first message, whatever the
	// settings said (#4590). With no setting magpie has nothing of its own
	// to answer it with, and it goes on the sign-in as the thread's turns do.
	if codexSideSet(requestCallKind(r.Header, requestSessionMetadata(r.Header, body))) {
		return nil, ""
	}
	if !fromCodex(r.Header) && agentOf(r) != "codex" {
		return nil, ""
	}
	if _, _, ok := provider.Resolve(model); ok {
		return nil, ""
	}
	if provider.IsGroupRef(model) {
		return nil, ""
	}
	if _, ok := provider.GroupFor(model); ok {
		return nil, ""
	}
	if _, ok := provider.AutoStandIn(model); ok {
		return nil, ""
	}
	if _, off := idOff(model); off {
		return nil, ""
	}
	if _, off := provider.SwitchedOff(model); off {
		return nil, ""
	}
	if _, keyHeld := keyHolds(r); keyHeld {
		return nil, ""
	}
	if accountHoldsOn(r, "codex") {
		return nil, ""
	}
	if !codexOwnModel(model) {
		return nil, ""
	}
	tok, acct, err := codexSignInToken()
	if err != nil || tok == "" {
		if err == nil {
			err = errors.New("Codex is signed out")
		}
		return nil, fmt.Sprintf("%q is one of Codex's own models, and this Codex thread was started while magpie was Codex's model provider, so it is still sent to magpie whatever Codex is set to now. "+
			"No ChatGPT account in magpie serves it, and magpie can't send it on Codex's own ChatGPT sign-in: %v. "+
			"Start a new thread in Codex for its own models, or add Codex's ChatGPT account in magpie (Providers)", model, err)
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = CodexPath + "/responses"
	r2.URL.RawPath = ""
	r2.RequestURI = ""
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	r2.Header.Del("Content-Encoding")
	r2.Header.Del("Content-Length")
	// magpie's key and the header that names magpie Codex's provider are
	// magpie's own, never sent on to OpenAI
	r2.Header.Del("X-Api-Key")
	r2.Header.Del("X-Openai-Actor-Authorization")
	r2.Header.Set("Authorization", "Bearer "+tok)
	if acct != "" {
		r2.Header.Set("Chatgpt-Account-Id", acct)
	}
	return r2, ""
}

// codexOwnModel: model is one of Codex's own, as the ChatGPT backend last
// listed it to Codex (models_cache.json, magpie's entries left out) or as
// the catalog has it for OpenAI.
func codexOwnModel(model string) bool {
	if _, ok := codexcat.CacheEntries()[model]; ok {
		return true
	}
	for _, m := range catalog.Provider("openai") {
		if m.ID == model {
			return true
		}
	}
	return false
}

// codexSideSet: a side call of this kind has a setting naming where it
// goes (a model, or "off"). A thread's summary has none.
func codexSideSet(kind string) bool {
	st := settings.Load()
	switch {
	case isTitleKind(kind):
		return st.CodexTitles != ""
	case isDescriptionKind(kind):
		return st.CodexDescriptions != ""
	case isPredictionKind(kind):
		return st.CodexPredictions != ""
	case usage.PurposeOf(kind) == "kind:ambient_suggestions":
		return st.CodexSuggestions != ""
	}
	return false
}
