package gui

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The tray panel's Allowances tab leaves out the subscriptions set hidden
// there (H20 on Discord), kept beside the Usage page's order: each is saved
// on its own, the other left as it was, and both outlast a save of the
// Settings page.
func TestUsageArrangePanelHidden(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	want := func(order, hidden []string) {
		t.Helper()
		s := settings.Load()
		if !slices.Equal(s.UsageOrder, order) || !slices.Equal(s.PanelUsageHidden, hidden) {
			t.Fatalf("order %v hidden %v, want %v %v", s.UsageOrder, s.PanelUsageHidden, order, hidden)
		}
	}
	post("/api/usage/arrange", `{"order":["kimi","codex"]}`)
	want([]string{"kimi", "codex"}, nil)
	post("/api/usage/arrange", `{"panelHidden":["deepseek"," deepseek","claude"]}`)
	want([]string{"kimi", "codex"}, []string{"deepseek", "claude"})
	post("/api/usage/arrange", `{"order":["codex","kimi"]}`)
	want([]string{"codex", "kimi"}, []string{"deepseek", "claude"})
	post("/api/settings", `{"theme":"dark"}`)
	want([]string{"codex", "kimi"}, []string{"deepseek", "claude"})
	post("/api/usage/arrange", `{"panelHidden":[]}`)
	want([]string{"codex", "kimi"}, nil)
}

// The Usage page's Agents list leaves out the clients hidden there
// (ikunbots on X: curl, Go-http-client, python-httpx beside the agents in
// use): saved on its own, beside the panel's, and kept through a save of
// the Settings page.
func TestUsageArrangeAgentsHidden(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	want := func(agents, panel []string) {
		t.Helper()
		s := settings.Load()
		if !slices.Equal(s.UsageAgentsHidden, agents) || !slices.Equal(s.PanelUsageHidden, panel) {
			t.Fatalf("agents hidden %v panel hidden %v, want %v %v", s.UsageAgentsHidden, s.PanelUsageHidden, agents, panel)
		}
	}
	post("/api/usage/arrange", `{"panelHidden":["deepseek"]}`)
	post("/api/usage/arrange", `{"agentsHidden":["curl","Go-http-client"," curl"]}`)
	want([]string{"curl", "Go-http-client"}, []string{"deepseek"})
	post("/api/settings", `{"theme":"dark"}`)
	want([]string{"curl", "Go-http-client"}, []string{"deepseek"})
	post("/api/usage/arrange", `{"agentsHidden":["Go-http-client"]}`)
	want([]string{"Go-http-client"}, []string{"deepseek"})
	post("/api/usage/arrange", `{"agentsHidden":[]}`)
	want(nil, []string{"deepseek"})
}

// The allowance cards the Usage page's Overview leaves out
// (whqtian_07012 on Discord: StepFun's balance card, not its plan card)
// are saved on their own, trimmed and once each, leave the other hidden
// lists as they were, and are kept through a save of the Settings page.
func TestUsageArrangeCardsHidden(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	want := func(cards []string) {
		t.Helper()
		s := settings.Load()
		if !slices.Equal(s.UsageCardsHidden, cards) {
			t.Fatalf("cards hidden %v, want %v", s.UsageCardsHidden, cards)
		}
		if !slices.Equal(s.PanelUsageHidden, []string{"deepseek"}) || !slices.Equal(s.UsageAgentsHidden, []string{"curl"}) {
			t.Fatalf("panel hidden %v agents hidden %v, want them kept", s.PanelUsageHidden, s.UsageAgentsHidden)
		}
	}
	post("/api/usage/arrange", `{"panelHidden":["deepseek"],"agentsHidden":["curl"]}`)
	post("/api/usage/arrange", `{"cardsHidden":["stepfun|balance"," stepfun|balance","kimi|me@x|plan"]}`)
	want([]string{"stepfun|balance", "kimi|me@x|plan"})
	post("/api/settings", `{"theme":"dark"}`)
	want([]string{"stepfun|balance", "kimi|me@x|plan"})
	post("/api/usage/arrange", `{"order":["kimi","stepfun"]}`)
	want([]string{"stepfun|balance", "kimi|me@x|plan"})
	post("/api/usage/arrange", `{"cardsHidden":[]}`)
	want(nil)
}

// The quota rows of a card that the Overview leaves out (wangshaowei39 on
// X: Antigravity's Claude & GPT) are saved on their own, trimmed and once
// each, leave the card list as it was, and are kept through a save of the
// Settings page.
func TestUsageArrangeWindowsHidden(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	want := func(rows []string) {
		t.Helper()
		s := settings.Load()
		if !slices.Equal(s.UsageWindowsHidden, rows) {
			t.Fatalf("windows hidden %v, want %v", s.UsageWindowsHidden, rows)
		}
		if !slices.Equal(s.UsageCardsHidden, []string{"stepfun|balance"}) {
			t.Fatalf("cards hidden %v, want them kept", s.UsageCardsHidden)
		}
	}
	post("/api/usage/arrange", `{"cardsHidden":["stepfun|balance"]}`)
	post("/api/usage/arrange", `{"windowsHidden":["antigravity|Claude & GPT"," antigravity|Claude & GPT","claude|7 days"]}`)
	want([]string{"antigravity|Claude & GPT", "claude|7 days"})
	post("/api/settings", `{"theme":"dark"}`)
	want([]string{"antigravity|Claude & GPT", "claude|7 days"})
	post("/api/usage/arrange", `{"cardsHidden":["stepfun|balance"]}`)
	want([]string{"antigravity|Claude & GPT", "claude|7 days"})
	post("/api/usage/arrange", `{"windowsHidden":[]}`)
	want(nil)
}

// A client magpie doesn't know is counted as one of its agents from the
// Usage page's Agents list (#1958, NorethSea), saved on its own and kept
// through a save of the Settings page; As "" counts it on its own again.
// An agent magpie knows isn't counted as another, nor anything as
// magpie, nor as a client that isn't an agent.
func TestUsageArrangeCountClient(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec.Code
	}
	want := func(m map[string]string) {
		t.Helper()
		if got := settings.Load().ClientAgents; !maps.Equal(got, m) {
			t.Fatalf("clientAgents %v, want %v", got, m)
		}
	}
	if c := post("/api/usage/arrange", `{"count":{"client":"hermes-codex-thread-bridge","as":"codex"}}`); c != http.StatusOK {
		t.Fatalf("count: %d", c)
	}
	want(map[string]string{"hermes-codex-thread-bridge": "codex"})
	post("/api/settings", `{"theme":"dark"}`)
	want(map[string]string{"hermes-codex-thread-bridge": "codex"})
	for _, bad := range []string{
		`{"count":{"client":"claude-code","as":"codex"}}`,
		`{"count":{"client":"magpie-router","as":"codex"}}`,
		`{"count":{"client":"my-bridge","as":"magpie"}}`,
		`{"count":{"client":"my-bridge","as":"Python-urllib"}}`,
		`{"count":{"client":"","as":"codex"}}`,
	} {
		if c := post("/api/usage/arrange", bad); c == http.StatusOK {
			t.Fatalf("%s was taken", bad)
		}
	}
	want(map[string]string{"hermes-codex-thread-bridge": "codex"})
	post("/api/usage/arrange", `{"count":{"client":"Hermes-Codex-Thread-Bridge","as":""}}`)
	want(nil)
}
