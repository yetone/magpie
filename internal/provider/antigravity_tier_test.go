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
	if got, want := antigravityModelIDs(t, "antigravity"), []string{"claude-opus-5-5", "claude-sonnet-5-5", "gemini-3.8-flash-high"}; !slices.Equal(got, want) {
		t.Fatalf("models %v, want %v", got, want)
	}
	if len(f.fetches) != 1 || fmt.Sprint(f.fetches[0]["entitlement"]) != "map[userTier:g1-pro-tier]" || f.fetches[0]["project"] != "aicode-consumers" {
		t.Errorf("asked %v", f.fetches)
	}
	out := buf.String()
	for _, w := range []string{
		`loadCodeAssist at ` + codeAssistProd + `: current tier standard-tier (Antigravity) gcp-tos, paid tier g1-pro-tier (Google AI Pro), project "aicode-consumers", ineligible free-tier (VALIDATION_REQUIRED: Verify your account to continue.)`,
		`fetchAvailableModels at ` + codeAssistDaily + ` (antigravity/hub/3.1.4 `,
		`project "aicode-consumers", current tier "standard-tier", paid tier "g1-pro-tier", entitlement "g1-pro-tier"): 4 models, 3 kept; left out 1 not for chat [tab_flash_lite_preview], 0 completion-only []; disabled [claude-sonnet-5-5]; Claude [claude-opus-5-5 claude-sonnet-5-5]`,
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
