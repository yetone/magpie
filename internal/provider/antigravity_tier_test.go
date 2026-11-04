package provider

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A Google AI Pro account as loadCodeAssist answers it (CLIProxyAPI
// #6186): on standard-tier, paying for g1-pro-tier, in aicode-consumers.
const proLoad = `{
	"currentTier":{"id":"standard-tier","name":"Antigravity","userDefinedCloudaicompanionProject":true,"usesGcpTos":true},
	"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro","availableCredits":[{"creditType":"GOOGLE_ONE_AI","minimumCreditAmountForUsage":"50"}]},
	"cloudaicompanionProject":"aicode-consumers",
	"ineligibleTiers":[{"tierId":"free-tier","reasonCode":"VALIDATION_REQUIRED","validationErrorMessage":"Verify your account to continue."}]}`

const (
	geminiOnly = `{"models":{
		"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
		"gemini-2.5-pro":{"displayName":"Gemini 2.5 Pro"},
		"tab_flash_lite_preview":{},
		"chat_20706":{}}}`
	withClaude = `{"models":{
		"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
		"claude-opus-5-5":{"displayName":"Claude Opus 5.5","maxTokens":250000,"quotaInfo":{"remainingFraction":1}},
		"claude-sonnet-5-5":{"displayName":"Claude Sonnet 5.5","maxTokens":250000,"disabled":true,"quotaInfo":{"remainingFraction":1}},
		"tab_flash_lite_preview":{}}}`
	tierRefused = `{"error":{"code":400,"message":"Precondition check failed.","status":"FAILED_PRECONDITION"}}`
)

// Code Assist as it serves an account on tier has: the plan's models when
// asked with that tier named, Code Assist Standard's without one, and a
// 400 for a tier the account hasn't.
func tierModels(has string) func(map[string]any) (int, string) {
	return func(body map[string]any) (int, string) {
		e, _ := body["entitlement"].(map[string]any)
		switch tier, _ := e["userTier"].(string); tier {
		case "":
			return 200, geminiOnly
		case has:
			return 200, withClaude
		default:
			return 400, tierRefused
		}
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func antigravityModelIDs(t *testing.T, agent string) []string {
	t.Helper()
	ms, err := googleLogins(agent)[0].acct.models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

// A Google AI Pro account's Antigravity has Claude (Discord: magpie 0.1.770
// listed a Pro account's models without any, while Antigravity's picker
// had Claude 5.5): fetchAvailableModels names the plan the account pays for
// in entitlement.userTier, as Antigravity's language server does, and the
// log says what Google answered and what magpie left out.
func TestAntigravityProAccountNamesItsTier(t *testing.T) {
	f := &fakeGoogle{load: proLoad, modelsFor: tierModels("g1-pro-tier")}
	googleSandbox(t, f)
	buf := captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-pro", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "pro@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	if got, want := antigravityModelIDs(t, "antigravity"), []string{"claude-opus-5-5", "gemini-3.8-flash-high"}; !slices.Equal(got, want) {
		t.Fatalf("models %v, want %v", got, want)
	}
	if len(f.fetches) != 1 || fmt.Sprint(f.fetches[0]["entitlement"]) != "map[userTier:g1-pro-tier]" || f.fetches[0]["project"] != "aicode-consumers" {
		t.Errorf("asked %v", f.fetches)
	}
	out := buf.String()
	for _, w := range []string{
		`loadCodeAssist at ` + codeAssistProd + `: current tier standard-tier (Antigravity) gcp-tos, paid tier g1-pro-tier (Google AI Pro), project "aicode-consumers", ineligible free-tier (VALIDATION_REQUIRED: Verify your account to continue.)`,
		`fetchAvailableModels at ` + codeAssistDaily + ` (antigravity/hub/3.1.4 `,
		`project "aicode-consumers", current tier "standard-tier", paid tier "g1-pro-tier", entitlement "g1-pro-tier"): 4 models, 2 kept; left out 1 not for chat [tab_flash_lite_preview], 0 completion-only []; disabled [claude-sonnet-5-5]; Claude [claude-opus-5-5]`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

// A tier Google won't take from the account is left out: the next is
// tried, then none, and the refusal is logged.
func TestAntigravityTierRefusedFallsBack(t *testing.T) {
	f := &fakeGoogle{load: proLoad, modelsFor: tierModels("standard-tier")}
	googleSandbox(t, f)
	buf := captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-pro2", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	addGoogleLogin("antigravity", "pro@example.com", "", auth)
	if got := antigravityModelIDs(t, "antigravity"); !slices.Contains(got, "claude-opus-5-5") {
		t.Fatalf("models %v", got)
	}
	var sent []string
	for _, b := range f.fetches {
		sent = append(sent, fmt.Sprint(b["entitlement"]))
	}
	if w := "map[userTier:g1-pro-tier] map[userTier:standard-tier]"; strings.Join(sent, " ") != w {
		t.Errorf("sent %v, want %s", sent, w)
	}
	if !strings.Contains(buf.String(), "with entitlement g1-pro-tier: ") || !strings.Contains(buf.String(), "Precondition check failed.") {
		t.Errorf("log:\n%s", buf.String())
	}

	// none taken: asked without one, as before
	f.modelsFor = tierModels("g1-ultra-tier")
	resetGoogleState()
	if got := antigravityModelIDs(t, "antigravity"); !slices.Equal(got, []string{"gemini-3.8-flash-high"}) {
		t.Fatalf("models %v", got)
	}
	if n := len(f.fetches); n != 5 || f.fetches[n-1]["entitlement"] != nil {
		t.Errorf("asked %v", f.fetches)
	}
}

// A free account names no tier (Google refuses "free-tier" there), as
// before; one whose project was kept with its sign-in still has its tiers
// asked for, so a Pro account imported with its project names its plan.
func TestAntigravityTierFreeAndKeptProject(t *testing.T) {
	f := &fakeGoogle{
		load:      `{"currentTier":{"id":"free-tier","name":"Antigravity"},"paidTier":{"id":"free-tier","name":"Antigravity Starter Quota"},"cloudaicompanionProject":"aicode-consumers"}`,
		modelsFor: tierModels("g1-pro-tier"),
	}
	googleSandbox(t, f)
	captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-free", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	addGoogleLogin("antigravity", "free@example.com", "", auth)
	antigravityModelIDs(t, "antigravity")
	if len(f.fetches) != 1 || f.fetches[0]["entitlement"] != nil {
		t.Errorf("a free account asked %v", f.fetches)
	}

	f.load, f.fetches = proLoad, nil
	resetGoogleState()
	if err := SetGoogleProject("antigravity", "free@example.com", "kept-proj"); err != nil {
		t.Fatal(err)
	}
	if got := antigravityModelIDs(t, "antigravity"); !slices.Contains(got, "claude-opus-5-5") {
		t.Fatalf("models %v", got)
	}
	if len(f.fetches) != 1 || f.fetches[0]["project"] != "kept-proj" || fmt.Sprint(f.fetches[0]["entitlement"]) != "map[userTier:g1-pro-tier]" {
		t.Errorf("asked %v", f.fetches)
	}
}

// A model Antigravity marks disabled isn't served: a real account's
// fetchAvailableModels had gemini-4-argon-disabled with disabled true,
// magpie listed it, DeepSeek Harness offered it, and asking for it was a
// 404 "Requested entity was not found" (kwanyu on Discord, 10-24). It is
// left out of the list for every client; the models beside it, and their
// ids, are as Google sent them.
func TestAntigravityDisabledModelLeftOut(t *testing.T) {
	f := &fakeGoogle{load: `{"currentTier":{"id":"free-tier","name":"Antigravity"},"cloudaicompanionProject":"aicode-consumers"}`, models: `{"models":{
		"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
		"gemini-4-argon-disabled":{"displayName":"Gemini 4 Argon","maxTokens":1048576,"disabled":true,"quotaInfo":{"remainingFraction":1}},
		"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)","maxTokens":200000,"quotaInfo":{"remainingFraction":0.5}},
		"gpt-oss-120b-medium":{"displayName":"GPT-OSS 120B (Medium)","maxTokens":131072,"disabled":false}}}`}
	googleSandbox(t, f)
	buf := captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-argon", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "argon@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	if got, want := antigravityModelIDs(t, "antigravity"), []string{"claude-opus-4-6-thinking", "gemini-3.8-flash-high", "gpt-oss-120b-medium"}; !slices.Equal(got, want) {
		t.Fatalf("models %v, want %v", got, want)
	}
	if w := "4 models, 3 kept; left out 0 not for chat [], 0 completion-only []; disabled [gemini-4-argon-disabled]; Claude [claude-opus-4-6-thinking]"; !strings.Contains(buf.String(), w) {
		t.Errorf("log lacks %q:\n%s", w, buf.String())
	}
}

// #1553 (AnxForever): a Google AI Pro account that loadCodeAssist has on
// free-tier, paying for g1-pro-tier, in aicode-consumers. Google answered
// its fetchAvailableModels with g1-pro-tier named "Request contains an
// invalid argument." magpie then asks again with {project} alone, which is
// all the open-source clients that serve Pro accounts send (CLIProxyAPI,
// sub2api, Antigravity-Manager), and lists what that gives. Should that
// fail too, the log says so and the account keeps the list it had.
func TestAntigravityProInvalidTierAsksWithoutOne(t *testing.T) {
	const load = `{"currentTier":{"id":"free-tier","name":"Antigravity"},
		"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"},
		"cloudaicompanionProject":"aicode-consumers"}`
	const invalid = `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`
	plain := 200
	f := &fakeGoogle{load: load, modelsFor: func(body map[string]any) (int, string) {
		if body["entitlement"] != nil {
			return 400, invalid
		}
		if plain != 200 {
			return plain, `{"error":{"code":503,"message":"The service is currently unavailable.","status":"UNAVAILABLE"}}`
		}
		return 200, withClaude
	}}
	googleSandbox(t, f)
	buf := captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-1553", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "paid@gmail.com", "", auth); err != nil {
		t.Fatal(err)
	}
	p, _ := googleAccountOf("antigravity")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || !slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "claude-opus-5-5" }) { // claude-sonnet-5-5 is disabled
		t.Fatalf("models %v", ms)
	}
	f.mu.Lock()
	asked := fmt.Sprint(f.fetches)
	f.mu.Unlock()
	if asked != "[map[entitlement:map[userTier:g1-pro-tier] project:aicode-consumers] map[project:aicode-consumers]]" {
		t.Errorf("asked %s", asked)
	}
	for _, w := range []string{
		`antigravity paid@gmail.com: loadCodeAssist at ` + codeAssistProd + `: current tier free-tier (Antigravity), paid tier g1-pro-tier (Google AI Pro), project "aicode-consumers"`,
		`antigravity paid@gmail.com: fetchAvailableModels with entitlement g1-pro-tier: Antigravity: Request contains an invalid argument.; asking with none next`,
		`project "aicode-consumers", current tier "free-tier", paid tier "g1-pro-tier", entitlement ""): 4 models, 2 kept`,
	} {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("log lacks %q:\n%s", w, buf.String())
		}
	}

	// asked without one and refused too: logged, the list it had is kept
	f.mu.Lock()
	plain = 503
	f.mu.Unlock()
	buf.Reset()
	ms, err = p.Fetch(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("kept: %v, %v", ms, err)
	}
	for _, w := range []string{
		`antigravity paid@gmail.com: fetchAvailableModels at ` + codeAssistDaily + ` (project "aicode-consumers", no entitlement): Antigravity: The service is currently unavailable.`,
		`antigravity paid@gmail.com: models not refreshed: Antigravity: The service is currently unavailable.; keeping the 2 it listed before`,
	} {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("log lacks %q:\n%s", w, buf.String())
		}
	}
}

// ferock4669 on Discord: an Antigravity subscription's Requests page filled
// with "Model list" rows for /v1internal:fetchAvailableModels, each a 400.
// A Pro account's list is asked first with its paid tier named, which
// Google answers 400 INVALID_ARGUMENT (#1553), then without one, which
// lists its models. That first ask was a row of its own on every list, and
// was sent again on every list and quota reading. Now the refused tier is
// a step no row is written for, it isn't named again for tierRefusedFor,
// and a list that fails without one is still written down.
func TestAntigravityRefusedTierAskedOnceAndNoRow(t *testing.T) {
	const load = `{"currentTier":{"id":"free-tier","name":"Antigravity"},
		"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"},
		"cloudaicompanionProject":"aicode-consumers"}`
	const invalid = `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`
	plain := 200
	f := &fakeGoogle{load: load, modelsFor: func(body map[string]any) (int, string) {
		if body["entitlement"] != nil {
			return 400, invalid
		}
		if plain != 200 {
			return plain, invalid
		}
		return 200, withClaude
	}}
	googleSandbox(t, f)
	captureLog(t)
	rows := probeRows(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ferock", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "paid@gmail.com", "", auth); err != nil {
		t.Fatal(err)
	}
	p, _ := googleAccountOf("antigravity")
	for range 3 {
		if ms, err := p.Fetch(context.Background()); err != nil || len(ms) != 2 {
			t.Fatalf("models %v, %v", ms, err)
		}
	}
	// the account's quota reading asks the same list
	g := googleLogins("antigravity")[0].acct
	if q := g.quota(context.Background(), ""); q.Error != "" {
		t.Fatalf("quota: %s", q.Error)
	}
	f.mu.Lock()
	var named int
	for _, b := range f.fetches {
		if b["entitlement"] != nil {
			named++
		}
	}
	asked := len(f.fetches)
	f.mu.Unlock()
	if named != 1 || asked != 5 {
		t.Errorf("asked %d times, %d with the refused tier named; want 5 and 1", asked, named)
	}
	for _, r := range rows() {
		if r.Status == 400 {
			t.Errorf("a refused tier written down: %+v", r)
		}
	}

	// a day on, the tier is named again
	googleState.Lock()
	for k := range googleState.refused {
		googleState.refused[k] = time.Now().Add(-tierRefusedFor - time.Minute)
	}
	googleState.Unlock()
	f.mu.Lock()
	f.fetches = nil
	f.mu.Unlock()
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.fetches) != 2 || f.fetches[0]["entitlement"] == nil {
		t.Errorf("a day on, asked %v", f.fetches)
	}
	f.mu.Unlock()

	// the list refused without a tier too is written down, once
	f.mu.Lock()
	plain = 400
	f.mu.Unlock()
	before := len(rows())
	p.Fetch(context.Background())
	var failed []string
	for _, r := range rows()[before:] {
		if r.Status != 200 {
			failed = append(failed, fmt.Sprint(r.Status, " ", r.Kind, " ", r.Endpoint))
		}
	}
	if len(failed) != 1 || failed[0] != "400 model_list /daily/v1internal:fetchAvailableModels" {
		t.Errorf("rows of the failed list %v", failed)
	}
}

// A tier refused for a reason other than a 400 (Google down) is named
// again on the next list.
func TestAntigravityTierNotRestedForOtherFailures(t *testing.T) {
	f := &fakeGoogle{load: proLoad, modelsFor: func(body map[string]any) (int, string) {
		if body["entitlement"] != nil {
			return 503, `{"error":{"code":503,"message":"The service is currently unavailable.","status":"UNAVAILABLE"}}`
		}
		return 200, geminiOnly
	}}
	googleSandbox(t, f)
	captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-503", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	addGoogleLogin("antigravity", "pro@example.com", "", auth)
	antigravityModelIDs(t, "antigravity")
	antigravityModelIDs(t, "antigravity")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fetches) != 6 { // g1-pro-tier, standard-tier, none; twice
		t.Errorf("asked %v", f.fetches)
	}
}

// An account that becomes Google AI Ultra while magpie runs gets Ultra's
// models on the next list, without a restart (rick_49551 on Discord: an
// Ultra account without Gemini 4). The tiers loadCodeAssist gave were kept
// for as long as magpie ran, so every hourly list went on naming none, and
// Google went on listing gemini-4-argon-disabled, as it does a free
// account (magpie's own log on 11-04: "disabled [gemini-4-argon-disabled]").
// Now they are asked again once an hour old, as Gemini CLI's experiments
// are, and a list names the tier the account has now.
func TestAntigravityTierAskedAgainAfterUpgrade(t *testing.T) {
	const free = `{"currentTier":{"id":"free-tier","name":"Antigravity"},"cloudaicompanionProject":"aicode-consumers"}`
	const ultra = `{"currentTier":{"id":"standard-tier","name":"Antigravity","usesGcpTos":true},
		"paidTier":{"id":"g1-ultra-tier","name":"Google AI Ultra"},"cloudaicompanionProject":"aicode-consumers"}`
	f := &fakeGoogle{load: free, modelsFor: func(body map[string]any) (int, string) {
		e, _ := body["entitlement"].(map[string]any)
		switch tier, _ := e["userTier"].(string); tier {
		case "":
			return 200, `{"models":{
				"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
				"gemini-4-argon-disabled":{"displayName":"Gemini 4 Argon","maxTokens":1048576,"disabled":true,"quotaInfo":{"remainingFraction":1}}}}`
		case "g1-ultra-tier":
			return 200, `{"models":{
				"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}},
				"gemini-4-argon":{"displayName":"Gemini 4 Argon","maxTokens":1048576,"quotaInfo":{"remainingFraction":1}}}}`
		default:
			return 400, tierRefused
		}
	}}
	googleSandbox(t, f)
	buf := captureLog(t)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "rt-ultra", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "ultra@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	if got, want := antigravityModelIDs(t, "antigravity"), []string{"gemini-3.8-flash-high"}; !slices.Equal(got, want) {
		t.Fatalf("free: models %v, want %v", got, want)
	}

	// the account becomes Ultra; within the hour the tiers stand
	f.mu.Lock()
	f.load = ultra
	loads := len(f.loads)
	f.mu.Unlock()
	if got := antigravityModelIDs(t, "antigravity"); slices.Contains(got, "gemini-4-argon") {
		t.Fatalf("tiers asked again within the hour: %v", got)
	}
	f.mu.Lock()
	if len(f.loads) != loads {
		t.Errorf("loadCodeAssist asked %d times within the hour", len(f.loads)-loads)
	}
	f.mu.Unlock()

	// an hour on, the list names Ultra and has Gemini 4 Argon
	googleState.Lock()
	for k, p := range googleState.projects {
		p.at = p.at.Add(-googleTiersFor)
		googleState.projects[k] = p
	}
	googleState.Unlock()
	if got, want := antigravityModelIDs(t, "antigravity"), []string{"gemini-3.8-flash-high", "gemini-4-argon"}; !slices.Equal(got, want) {
		t.Fatalf("Ultra: models %v, want %v", got, want)
	}
	f.mu.Lock()
	last := f.fetches[len(f.fetches)-1]
	f.mu.Unlock()
	if fmt.Sprint(last["entitlement"]) != "map[userTier:g1-ultra-tier]" || last["project"] != "aicode-consumers" {
		t.Errorf("asked %v", last)
	}
	if w := `paid tier g1-ultra-tier (Google AI Ultra), project "aicode-consumers" (was current tier "free-tier", paid tier "")`; !strings.Contains(buf.String(), w) {
		t.Errorf("log lacks %q:\n%s", w, buf.String())
	}
	if q := googleLogins("antigravity")[0].acct.quota(context.Background(), ""); q.Plan != "Google AI Ultra" {
		t.Errorf("plan %q", q.Plan)
	}

	// loadCodeAssist failing keeps the tiers it gave
	f.mu.Lock()
	f.load = `not json`
	f.mu.Unlock()
	googleState.Lock()
	for k, p := range googleState.projects {
		p.at = p.at.Add(-googleTiersFor)
		googleState.projects[k] = p
	}
	googleState.Unlock()
	if got := antigravityModelIDs(t, "antigravity"); !slices.Contains(got, "gemini-4-argon") {
		t.Fatalf("tiers lost on a failed loadCodeAssist: %v", got)
	}
}
