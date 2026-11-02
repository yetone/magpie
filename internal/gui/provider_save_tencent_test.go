package gui

// Tencent Cloud's Token Plan 企业版 is read with the account's SecretId and
// SecretKey (#1574): the listing tells the editor the Tencent Cloud preset
// takes one at its plan regions alone (China's, which Token Plan 个人版
// shares with 企业版 since Tencent moved it there, ysm119; and 企业版's
// in Singapore), not at pay as you go, and whose it is, and a
// provider of it, at whatever region, its SecretId and whether a
// SecretKey is saved, never the SecretKey. Volcengine Ark's take one at
// all of theirs; another vendor's at none.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestProviderListTellsTencentAccessKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)

	at := func(region string) provider.Provider {
		t.Helper()
		p, err := provider.FromPreset("tencent-cloud")
		if err != nil {
			t.Fatal(err)
		}
		pr := provider.Preset("tencent-cloud")
		for _, r := range pr.Regions {
			if r.ID == region {
				p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
			}
		}
		return p
	}
	team := at("enterprise-intl")
	team.Key, team.AccessKeyID, team.SecretAccessKey = "sk-tp-1", "AKID-1", "SK-1"
	if err := provider.Save(team); err != nil {
		t.Fatal(err)
	}
	personal := at("plan")
	personal.ID, personal.Name, personal.Key = "tc-personal", "Tencent personal", "sk-tp-2"
	if err := provider.Save(personal); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "SK-1") {
		t.Error("the listing told the SecretKey")
	}
	var got struct {
		Providers []struct {
			ID        string         `json:"id"`
			AccessKey *accessKeyJSON `json:"accessKey"`
		} `json:"providers"`
		Presets []struct {
			ID               string   `json:"id"`
			AccessKey        bool     `json:"accessKey"`
			AccessKeyVendor  string   `json:"accessKeyVendor"`
			AccessKeyRegions []string `json:"accessKeyRegions"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ps := map[string]*accessKeyJSON{}
	for _, x := range got.Providers {
		ps[x.ID] = x.AccessKey
	}
	if a := ps["tencent-cloud"]; a == nil || a.Vendor != "tencent" || a.ID != "AKID-1" || !a.SecretSet {
		t.Errorf("enterprise: %+v", a)
	}
	// at China's plan, 个人版's and 企业版's both, it takes one too
	if a := ps["tc-personal"]; a == nil || a.Vendor != "tencent" || a.ID != "" || a.SecretSet {
		t.Errorf("personal: %+v", a)
	}
	seen := 0
	for _, pr := range got.Presets {
		switch pr.ID {
		case "tencent-cloud":
			seen++
			if !pr.AccessKey || pr.AccessKeyVendor != "tencent" || !slices.Equal(pr.AccessKeyRegions, []string{"plan", "enterprise-intl"}) {
				t.Errorf("tencent-cloud preset: %+v", pr)
			}
		case "volcengine":
			seen++
			if !pr.AccessKey || pr.AccessKeyVendor != "volcengine" || pr.AccessKeyRegions != nil {
				t.Errorf("volcengine preset: %+v", pr)
			}
		case "deepseek":
			seen++
			if pr.AccessKey || pr.AccessKeyVendor != "" {
				t.Errorf("deepseek preset: %+v", pr)
			}
		}
	}
	if seen != 3 {
		t.Errorf("presets seen: %d", seen)
	}
}
