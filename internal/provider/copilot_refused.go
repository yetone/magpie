package provider

// A model Copilot's /models offers an account that the account still can't
// call. A Copilot Student plan (#371, #256) is listed models it is answered
// 400 "The requested model is not supported." (code model_not_supported)
// for, policy "enabled" or not: of the seven its list had enabled, gpt-4.1
// alone was served. Neither its policy nor anything else in the list tells
// them apart, so Copilot's answer does: a model refused so is left out of
// the account's models for a while, an agent's pick of it too, and Auto,
// whose pick it was, picks another and the request is sent again.

import (
	"context"
	"regexp"
	"sync"
	"time"
)

// copilotRefusedFor is how long a refused model stays left out: a plan
// changed (a Student account upgraded) is seen again within it.
const copilotRefusedFor = 6 * time.Hour

var (
	copilotRefusedMu sync.Mutex
	copilotRefusedAt = map[string]map[string]time.Time{} // by GitHub token: model → when refused
)

// copilotNotServed is how Copilot says the account may not call the model —
// not that it is asked on the wrong endpoint ("not accessible via the
// /chat/completions endpoint", unsupported_api_for_model), which the
// gateway takes to another of the model's endpoints.
var copilotNotServed = regexp.MustCompile(`(?i)"model_not_supported"|requested model is not supported`)

// copilotRefused notes a refusal of model for the account, and says whether
// the request is worth sending again: when Auto picked the model, as Auto
// then picks another.
func copilotRefused(ctx context.Context, app copilotApp, model string, status int, body []byte) bool {
	if status != 400 || !copilotNotServed.Match(body) {
		return false
	}
	a, ok := ctx.Value(copilotAutoKey{}).(copilotAutoSession)
	// Auto's: the request the gateway resolved for it, or a model test
	// asking for "auto" itself
	auto := ok && a.Model == model || model == CopilotAuto
	if auto && (model == CopilotAuto || copilotRefuses(app.Token, model)) {
		// sent as the pick asked for in its stead (copilotAutoSign)
		copilotAutoMu.Lock()
		cur, ok := copilotAutoSessions[app.Token]
		copilotAutoMu.Unlock()
		if !ok {
			return false
		}
		model = cur.Model
	}
	if model == "" || model == CopilotAuto {
		return false
	}
	copilotRefusedMu.Lock()
	if copilotRefusedAt[app.Token] == nil {
		copilotRefusedAt[app.Token] = map[string]time.Time{}
	}
	copilotRefusedAt[app.Token][model] = time.Now()
	copilotRefusedMu.Unlock()
	if !auto {
		return false
	}
	// Auto's session, or the model standing in for it, is asked anew
	copilotAutoMu.Lock()
	if cur, ok := copilotAutoSessions[app.Token]; ok && cur.Model == model {
		delete(copilotAutoSessions, app.Token)
	}
	copilotAutoMu.Unlock()
	return true
}

// copilotRefuses reports whether the account was refused model lately.
func copilotRefuses(token, model string) bool {
	copilotRefusedMu.Lock()
	defer copilotRefusedMu.Unlock()
	at, ok := copilotRefusedAt[token][model]
	return ok && time.Since(at) < copilotRefusedFor
}
