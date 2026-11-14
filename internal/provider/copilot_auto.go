package provider

// Copilot's Auto: the model Copilot picks for the account, the only one a
// Student plan (Copilot Student / edu) may choose. Copilot's /models doesn't
// list it; its clients add it to their pickers themselves (Copilot CLI
// 1.0.79: {id:"auto", name:"Auto"} first, its AUTO_MODEL_ID) and, when it is
// picked, ask POST {api}/models/session with
// {"auto_mode":{"model_hints":["auto"]}} for a session: a session_token, the
// selected_model the requests then name, the available_models and when it
// expires_at (unix seconds). Every request of the session carries the token
// in Copilot-Session-Token, with the selected model in its body.
//
// VS Code's Copilot Chat asks the same (automodeService.ts, through
// @vscode/copilot-api's RequestType.AutoModels: {endpoints.api}/models/session)
// with an API version, X-GitHub-Api-Version 2025-10-01, which the editors'
// requests otherwise leave out: without it Copilot answers 404 (#256). Its
// answer has no selected_model: the client takes the first of
// available_models it knows.
//
// Copilot Chat 0.69 (now in microsoft/vscode, extensions/copilot:
// automodeService.ts, autoV2Fetcher.ts) asks neither: POST {api}/auto with
// the turn's prompt, {"prompt": …}, at X-GitHub-Api-Version 2026-08-01
// (another, or none, is 404 "invalid apiVersion"), which answers a
// session_token, the selected_model (its id, supported_endpoints and
// capabilities) and expires_at, a day on. Its pick is made among the
// models the account is served, where /models/session still names the
// same few for every plan (gpt-5.3-codex, gpt-5.4, gpt-5.4-mini,
// claude-haiku-4.5): a Student plan was refused each of those (#256), and
// an Individual one is picked gpt-6-luna. magpie asks /auto first, at
// Copilot Chat's default tier ("balance"), and /models/session only when
// /auto doesn't answer: a pick of /auto's the account was refused has the
// model it may pick by hand stand in. Each pick a request is sent with is
// noted on its try in the route (AutoPick).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// CopilotAuto is the model id that has Copilot pick the model.
const CopilotAuto = "auto"

// copilotAutoVersion is the API version VS Code's Copilot Chat asks for an
// Auto session with; the Copilot CLI's headers name their own.
const copilotAutoVersion = "2025-10-01"

// copilotAutoV2Version is the one POST /auto takes, as Copilot Chat 0.69
// (@vscode/copilot-api 0.5) sends it.
const copilotAutoV2Version = "2026-08-01"

// copilotAutoReuse is how long an Auto session is used for: /auto's lasts a
// day, which Copilot Chat keeps for one conversation; magpie, one for the
// account, has Copilot pick again within the hour.
const copilotAutoReuse = time.Hour

// copilotAutoModel is Auto as magpie lists it.
var copilotAutoModel = catalog.Model{ID: CopilotAuto, Name: "Auto"}

type copilotAutoSession struct {
	Token     string
	Model     string // the model Copilot picked
	ExpiresAt int64
	// Via: where the pick came from, "/auto", "/models/session" or
	// AutoFallback; Skipped: why /auto's wasn't taken, when it isn't
	Via, Skipped string
}

// AutoFallback is an Auto pick that is the model the account may pick by
// hand, Copilot having no Auto session to give it (copilotAutoFallback).
const AutoFallback = "fallback"

// AutoPick is a model Copilot's Auto picked for a try of a request: where
// the pick came from, why not from /auto when it didn't, and Copilot's
// refusal of it, which the route shows (#256: a Student plan's picks were
// refused, the tries logged as "auto"). API is the path it was sent to
// (/responses, /chat/completions), Session whether Auto's session token
// went with it.
type AutoPick struct {
	Model   string `json:"model"`
	Via     string `json:"via"`
	Skipped string `json:"skipped,omitempty"`
	Refused string `json:"refused,omitempty"`
	API     string `json:"api,omitempty"`
	Session bool   `json:"session,omitempty"`
}

type autoPicks struct {
	mu     sync.Mutex
	list   []AutoPick
	onPick func(model string)
}

type autoPicksKey struct{}

// WithAutoPicks has the Auto picks a request is sent with noted: the func
// returns them. onPick, when set, is told each model the request goes as,
// for the reply to name the one it went as last.
func WithAutoPicks(ctx context.Context, onPick func(model string)) (context.Context, func() []AutoPick) {
	p := &autoPicks{onPick: onPick}
	return context.WithValue(ctx, autoPicksKey{}, p), func() []AutoPick {
		p.mu.Lock()
		defer p.mu.Unlock()
		return slices.Clone(p.list)
	}
}

// notePick notes req is sent with a: once, while it isn't refused.
func notePick(ctx context.Context, a copilotAutoSession, req *http.Request) {
	p, ok := ctx.Value(autoPicksKey{}).(*autoPicks)
	if !ok || a.Model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pick := AutoPick{Model: a.Model, Via: a.Via, Skipped: a.Skipped, API: req.URL.Path, Session: req.Header.Get("Copilot-Session-Token") != ""}
	if n := len(p.list); n > 0 && p.list[n-1].Model == a.Model && p.list[n-1].Via == a.Via && p.list[n-1].API == pick.API && p.list[n-1].Refused == "" {
		return
	}
	p.list = append(p.list, pick)
	if p.onPick != nil {
		p.onPick(a.Model)
	}
	if a.Skipped != "" {
		log.Printf("copilot auto: %s picked %s (/auto: %s)", a.Via, a.Model, a.Skipped)
	}
}

// pickRefused notes Copilot refused the pick of model, saying said.
func pickRefused(ctx context.Context, model, said string) {
	p, ok := ctx.Value(autoPicksKey{}).(*autoPicks)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.list) - 1; i >= 0; i-- {
		if p.list[i].Model == model {
			if p.list[i].Refused == "" {
				p.list[i].Refused = said
				log.Printf("copilot auto: %s (%s) refused: %s", model, p.list[i].Via, said)
			}
			return
		}
	}
}

var (
	copilotAutoMu       sync.Mutex
	copilotAutoSessions = map[string]copilotAutoSession{} // by GitHub token
)

type copilotAutoKey struct{}

type copilotAutoPromptKey struct{}

// WithAutoPrompt carries the prompt of the request Copilot's Auto picks a
// model for: POST /auto picks for a prompt.
func WithAutoPrompt(ctx context.Context, prompt string) context.Context {
	return context.WithValue(ctx, copilotAutoPromptKey{}, prompt)
}

// copilotAutoResolve is the account's Auto session, asked again a couple of
// minutes before it expires, when fresh is set or once the account was
// refused its model.
func copilotAutoResolve(ctx context.Context, app copilotApp, fresh bool) (copilotAutoSession, error) {
	copilotAutoMu.Lock()
	defer copilotAutoMu.Unlock()
	if a, ok := copilotAutoSessions[app.Token]; ok && !fresh && (a.ExpiresAt == 0 || time.Until(time.Unix(a.ExpiresAt, 0)) > 2*time.Minute) && !copilotRefuses(app.Token, a.Model) {
		return a, nil
	}
	s, err := app.session(ctx)
	if err != nil {
		return copilotAutoSession{}, err
	}
	base := s.apiBase(app.Host)
	a, why, answered := copilotAutoRoute(ctx, app, s, base)
	if why == "" {
		copilotAutoSessions[app.Token] = a
		return a, nil
	}
	if answered {
		// /auto answered with a model the account was refused: Copilot
		// Chat asks nothing else, and /models/session names the same few
		// models for every plan, which a Student one is refused (#256)
		return copilotAutoFallback(ctx, app, "/auto "+why)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/session", strings.NewReader(`{"auto_mode":{"model_hints":["auto"]}}`))
	if err != nil {
		return copilotAutoSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-GitHub-Api-Version", copilotAutoVersion)
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return copilotAutoSession{}, errors.New("Copilot Auto: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var v struct {
		Token     string          `json:"session_token"`
		Selected  json.RawMessage `json:"selected_model"`
		Available []string        `json:"available_models"`
		ExpiresAt int64           `json:"expires_at"`
	}
	if res.StatusCode == http.StatusNotFound {
		return copilotAutoFallback(ctx, app, "/auto "+why+"; /models/session: "+APIError(b, res.Status))
	}
	if res.StatusCode/100 != 2 || json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotAutoSession{}, errors.New("Copilot Auto: " + APIError(b, res.Status))
	}
	// a model's id, or (Auto v2) the model itself
	var model string
	if json.Unmarshal(v.Selected, &model) != nil {
		var m struct {
			ID string `json:"id"`
		}
		json.Unmarshal(v.Selected, &m)
		model = m.ID
	}
	// one the account was refused is passed over for another available
	if model != "" && copilotRefuses(app.Token, model) {
		model = ""
	}
	available := slices.DeleteFunc(slices.Clone(v.Available), func(id string) bool { return copilotRefuses(app.Token, id) })
	// none named: the first available the account's list served, as VS
	// Code picks among the models it knows
	if model == "" {
		copilotSeenMu.Lock()
		for _, id := range available {
			if _, ok := copilotSeen[id]; ok {
				model = id
				break
			}
		}
		copilotSeenMu.Unlock()
	}
	if model == "" && len(available) > 0 {
		model = available[0]
	}
	if model == "" && len(v.Available) > 0 {
		// every model Auto offers the account was refused it: the one it
		// may pick by hand, as when it has no Auto
		return copilotAutoFallback(ctx, app, "/auto "+why+"; every model /models/session offers was refused")
	}
	if model == "" {
		return copilotAutoSession{}, errors.New("Copilot Auto picked no model")
	}
	a = copilotAutoSession{Token: v.Token, Model: model, ExpiresAt: v.ExpiresAt, Via: "/models/session", Skipped: why}
	copilotAutoSessions[app.Token] = a
	return a, nil
}

// copilotAutoRoute asks POST /auto for the model, as Copilot Chat does, at
// its default tier: why says what went wrong when Copilot doesn't answer it
// (an older API, Auto v2 not offered the account), answered being set when
// it did with a pick the account was refused.
func copilotAutoRoute(ctx context.Context, app copilotApp, s copilotSession, base string) (a copilotAutoSession, why string, answered bool) {
	prompt, _ := ctx.Value(copilotAutoPromptKey{}).(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = "hi" // a model test, or a turn of tool results alone
	}
	body, _ := json.Marshal(map[string]string{"prompt": prompt, "tier": "balance"})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auto", bytes.NewReader(body))
	if err != nil {
		return copilotAutoSession{}, err.Error(), false
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-GitHub-Api-Version", copilotAutoV2Version)
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return copilotAutoSession{}, err.Error(), false
	}
	defer res.Body.Close()
	var v struct {
		Token    string `json:"session_token"`
		Selected struct {
			ID        string   `json:"id"`
			Endpoints []string `json:"supported_endpoints"`
		} `json:"selected_model"`
		ExpiresAt int64 `json:"expires_at"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode/100 != 2:
		return copilotAutoSession{}, APIError(b, res.Status), false
	case json.Unmarshal(b, &v) != nil || v.Token == "" || v.Selected.ID == "":
		return copilotAutoSession{}, "picked no model: " + clipLine(QuotedReply(b)), false
	case copilotRefuses(app.Token, v.Selected.ID):
		return copilotAutoSession{}, "picked " + v.Selected.ID + ", which the account was refused", true
	}
	// the pick's endpoints, which the account's list may not give
	copilotSeenMu.Lock()
	if _, ok := copilotSeen[v.Selected.ID]; !ok || len(v.Selected.Endpoints) > 0 {
		copilotSeen[v.Selected.ID] = copilotAPIs(v.Selected.Endpoints)
	}
	copilotSeenMu.Unlock()
	exp := time.Now().Add(copilotAutoReuse).Unix()
	if v.ExpiresAt > 0 && v.ExpiresAt < exp {
		exp = v.ExpiresAt
	}
	return copilotAutoSession{Token: v.Token, Model: v.Selected.ID, ExpiresAt: exp, Via: "/auto"}, "", true
}

// copilotAutoFallback stands in for an Auto session Copilot has none of for
// the account (404): the model it may pick by hand likeliest served (see
// copilotModels) and not refused it, sent without a session, for a while;
// with none, an error that says so.
func copilotAutoFallback(ctx context.Context, app copilotApp, why string) (copilotAutoSession, error) {
	copilotTermsMu.Lock()
	picks, known := copilotPicks[app.Token]
	copilotTermsMu.Unlock()
	if !known {
		copilotModels(ctx, app)
		copilotTermsMu.Lock()
		picks = copilotPicks[app.Token]
		copilotTermsMu.Unlock()
	}
	picks = slices.DeleteFunc(slices.Clone(picks), func(id string) bool { return copilotRefuses(app.Token, id) })
	if len(picks) == 0 {
		return copilotAutoSession{}, errors.New("Copilot doesn't offer Auto to this account (" + why + "), and lists no model it may pick by hand that it serves; check the plan at github.com/settings/copilot")
	}
	a := copilotAutoSession{Model: picks[0], ExpiresAt: time.Now().Add(10 * time.Minute).Unix(), Via: AutoFallback, Skipped: why}
	copilotAutoSessions[app.Token] = a
	return a, nil
}

// ResolveAuto turns a request for a model the account picks itself
// (Copilot's Auto) into one for the model it picked: ctx then carries what
// the request is signed with. Any other model comes back as it was.
func (p Provider) ResolveAuto(ctx context.Context, model string) (context.Context, string, error) {
	if p.Account == nil || p.Account.auto == nil || model != CopilotAuto {
		return ctx, model, nil
	}
	a, err := p.Account.auto(p.Via(ctx))
	if err != nil {
		return ctx, model, err
	}
	return context.WithValue(ctx, copilotAutoKey{}, a), a.Model, nil
}

// AutoNext is the model Copilot's Auto picks now for a request ctx resolved
// for it (ResolveAuto), "" for any other: once Retry has said a refused
// pick is worth sending again, the one it goes as.
func (p Provider) AutoNext(ctx context.Context) string {
	if _, ok := ctx.Value(copilotAutoKey{}).(copilotAutoSession); !ok || p.Account == nil || p.Account.auto == nil {
		return ""
	}
	a, err := p.Account.auto(p.Via(ctx))
	if err != nil {
		return ""
	}
	return a.Model
}

// CopilotRefusal is Copilot saying the account may not call the model it
// was asked for, which Auto, having picked it, picks another for.
func CopilotRefusal(body []byte) bool { return copilotNotServed.Match(body) }

// copilotAutoSign puts the Auto session on a request: the one ctx carries
// when the body names its model, or, for a body asking for "auto" itself
// (a test of the model), a session asked for here with the body's model
// swapped for the one picked.
func copilotAutoSign(ctx context.Context, app copilotApp, req *http.Request, body []byte) (model string, err error) {
	model = bodyModel(body)
	a, auto := ctx.Value(copilotAutoKey{}).(copilotAutoSession)
	auto = auto && a.Model == model
	if auto && !copilotRefuses(app.Token, model) && copilotAutoHeld(app.Token, &a) {
		if a.Token != "" {
			req.Header.Set("Copilot-Session-Token", a.Token)
		}
		notePick(ctx, a, req)
		return model, nil
	}
	// Auto's pick sent again once the account was refused it
	// (copilotRefused) goes as Auto's next pick
	if model != CopilotAuto && !auto {
		return model, nil
	}
	a, err = copilotAutoResolve(ctx, app, false)
	if err != nil {
		return model, err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return model, errors.New("Copilot Auto: the request isn't JSON")
	}
	m["model"], _ = json.Marshal(a.Model)
	nb, _ := json.Marshal(m)
	SetBody(req, nb)
	if a.Token != "" {
		req.Header.Set("Copilot-Session-Token", a.Token)
	}
	notePick(ctx, a, req)
	return a.Model, nil
}

// copilotAutoHeld reports whether a's pick is still the account's Auto
// session's, a then being that session (asked anew after a was let go): one
// Copilot no longer took for its pick (copilotSessionGone) was let go, and
// the request goes with a session asked anew.
func copilotAutoHeld(token string, a *copilotAutoSession) bool {
	if a.Token == "" {
		return true
	}
	copilotAutoMu.Lock()
	defer copilotAutoMu.Unlock()
	cur, ok := copilotAutoSessions[token]
	if ok && cur.Model == a.Model {
		*a = cur
	}
	return ok && cur.Model == a.Model
}

// copilotSeen keeps the endpoints of every chat model Copilot's list
// named, listed by magpie or not: the model Auto picks may be one the
// account can't pick by hand, and is served on its own APIs all the same.
var (
	copilotSeenMu sync.Mutex
	copilotSeen   = map[string][]string{}
)

func copilotSeenAPIs(model string) []Protocol {
	copilotSeenMu.Lock()
	defer copilotSeenMu.Unlock()
	var out []Protocol
	for _, a := range copilotSeen[model] {
		out = append(out, Protocol(a))
	}
	return out
}

// ForgetCopilotForTest drops what magpie keeps of each Copilot account for
// the process's life — its session (and the API endpoint it named), Auto's
// session, the models it was refused, may pick or saw — so a test run
// again (-count) asks the fake GitHub it serves rather than the last run's.
func ForgetCopilotForTest() {
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{}
	copilotMu.Unlock()
	copilotAutoMu.Lock()
	copilotAutoSessions = map[string]copilotAutoSession{}
	copilotAutoMu.Unlock()
	copilotTermsMu.Lock()
	copilotTerms, copilotPicks = map[string]map[string]bool{}, map[string][]string{}
	copilotTermsMu.Unlock()
	copilotSeenMu.Lock()
	copilotSeen = map[string][]string{}
	copilotSeenMu.Unlock()
	copilotRefusedMu.Lock()
	copilotRefusedAt = map[string]map[string]time.Time{}
	copilotRefusedMu.Unlock()
}
