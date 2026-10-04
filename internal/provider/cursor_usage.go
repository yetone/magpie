package provider

// PLUGIN-SERVED (see AGENTS.md): Cursor ("cursor") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-cursor-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/cursor) and raise the
// mover's min in internal/provider/migrate_side.go.

// How much of a Cursor plan's included usage is gone, as the CLI's own
// usage view reads it: the dashboard's current period, split into the
// Cursor Models and Other Models pools.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// cursorBase is Cursor's API; a var so tests can point it elsewhere.
var cursorBase = "https://api2.cursor.sh"

// cursorKeychain reads cursor-agent's token from the macOS Keychain; a var
// so tests stay off the real one.
var cursorKeychain = runtime.GOOS == "darwin"

// cursorToken is the access token cursor-agent signed in with: in the
// Keychain on a Mac, in its auth.json elsewhere.
func cursorToken() (string, error) {
	if cursorKeychain {
		if tok := cursorKeychainToken(false); tok != "" {
			return tok, nil
		}
	}
	var auth struct {
		AccessToken string `json:"accessToken"`
	}
	if path := cursorAuthPath(); path != "" && readJSON(path, &auth) && auth.AccessToken != "" {
		return auth.AccessToken, nil
	}
	return "", errorf("cursor-agent is not signed in")
}

// cursorAuthPath is where cursor-agent keeps its sign-in outside the Keychain.
func cursorAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		dir := appdir.Getenv("APPDATA")
		if dir == "" {
			dir = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(dir, "Cursor", "auth.json")
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	}
	dir := appdir.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cursor", "auth.json")
}

func cursorSubscriptionUsage(ctx context.Context, plan string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "cursor", Name: "Cursor", Icon: "cursor", Plan: plan, Windows: []QuotaWindow{}}
	tok, err := cursorToken()
	if err == nil {
		q.Windows, err = cursorWindows(ctx, tok)
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// cursorWindows: the plan's included usage this billing period. An
// enterprise plan reports spend instead, and gets no windows.
func cursorWindows(ctx context.Context, token string) ([]QuotaWindow, error) {
	var data struct {
		BillingCycleEnd  string   `json:"billingCycleEnd"` // epoch millis
		AutoBucketModels []string `json:"autoBucketModels"`
		PlanUsage        *struct {
			Auto  float64 `json:"autoPercentUsed"`
			API   float64 `json:"apiPercentUsed"`
			Total float64 `json:"totalPercentUsed"`
		} `json:"planUsage"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorBase+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", bytes.NewReader([]byte("{}")))
	if err != nil {
		return []QuotaWindow{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return []QuotaWindow{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return []QuotaWindow{}, &accountStatusError{status: res.StatusCode}
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return []QuotaWindow{}, err
	}
	if data.PlanUsage == nil {
		return []QuotaWindow{}, nil
	}
	var resets *time.Time
	if ms, err := strconv.ParseInt(data.BillingCycleEnd, 10, 64); err == nil && ms > 0 {
		t := time.UnixMilli(ms)
		resets = &t
	}
	u := data.PlanUsage
	inCursorPool := func(model string) bool {
		model = cursorPoolBase(model)
		if model == "auto" {
			model = "default" // the CLI's Auto is default in Cursor's API
		}
		// the server names a family (grok-4.8); the CLI asks for one at an
		// effort or speed (grok-4.8-high-fast)
		return slices.ContainsFunc(data.AutoBucketModels, func(m string) bool { return cursorPoolBase(m) == model }) || cursorFirstPartyModel(model)
	}
	// the two pools fit the line; the total goes in its tooltip
	return []QuotaWindow{
		{Name: "Cursor Models", Used: u.Auto, ResetsAt: resets, matches: inCursorPool},
		{Name: "Other Models", Used: u.API, ResetsAt: resets, matches: func(model string) bool { return !inCursorPool(model) }},
		{Name: "Total", Used: u.Total, ResetsAt: resets, Aside: true},
	}, nil
}

// cursorPoolBase names a model's family: lower case, without cursor- and
// the effort and speed the CLI adds to it.
func cursorPoolBase(model string) string {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "cursor-")
	for {
		b := cursorVariant.ReplaceAllString(model, "")
		if b == model {
			return model
		}
		model = b
	}
}

// Cursor's autoBucketModels can lag model releases: it still omitted Grok
// 4.6/4.7 when the published Cursor Models pool already included them.
// Keep those documented families alongside the server's exact model list.
// See https://cursor.com/docs/models-and-pricing.
func cursorFirstPartyModel(model string) bool {
	model = strings.TrimPrefix(model, "cursor-")
	if model == "default" || model == "composer" || strings.HasPrefix(model, "composer-") {
		return true
	}
	for _, base := range []string{"grok-4.5", "grok-4.6", "grok-4.7"} {
		if model == base || strings.HasPrefix(model, base+"-") {
			return true
		}
	}
	return false
}
