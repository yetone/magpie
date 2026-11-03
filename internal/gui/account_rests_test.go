package gui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// A subscription account the gateway rests, or one of its models, is in
// the Providers page's state by the account's user in lower case, with
// the model, until when and the vendor's words, and the key Try now lifts
// it by (maxleebeta on Discord: WorkBuddy AI's 429 "将在 … 重置" rested a
// model with 82% of the credits left, and the page said nothing). Another
// provider's rests, and a provider with no rest, add nothing.
func TestAccountRestsInProviderState(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	until := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	said := "WorkBuddy: 您的使用量已超出频率限制，将在 2026-11-03 23:57:19 UTC+8 重置，您也可以切换其他模型继续使用。"
	was := restsUnder
	asked := []string{}
	restsUnder = func(prefix string) []gateway.Rest {
		asked = append(asked, prefix)
		if prefix != "workbuddy-ai@" {
			return nil
		}
		return []gateway.Rest{
			{Why: "quota", Status: 429, Until: until, By: "resets", Key: "workbuddy-ai@max.lee@example.com/gpt-6-astra", Model: "gpt-6-astra", Error: said},
			{Why: "rate", Status: 429, Until: until, By: "cooldown", Key: "workbuddy-ai@other@example.com"},
		}
	}
	defer func() { restsUnder = was }()

	p := provider.Provider{ID: "workbuddy-ai", Name: "WorkBuddy AI", Account: &provider.Account{Agent: "workbuddy-ai", User: "Max.Lee@Example.com"}}
	out := providerInfo(p, nil)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		AccountRests map[string][]provider.KeyRest `json:"accountRests"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	m := got.AccountRests["max.lee@example.com"]
	if len(m) != 1 || m[0].Model != "gpt-6-astra" || m[0].Key != "workbuddy-ai@max.lee@example.com/gpt-6-astra" || !m[0].Until.Equal(until) || m[0].Status != 429 || m[0].By != "resets" || m[0].Error != said {
		t.Fatalf("max: %+v (%s)", m, b)
	}
	if o := got.AccountRests["other@example.com"]; len(o) != 1 || o[0].Model != "" || o[0].Key != "workbuddy-ai@other@example.com" || o[0].Why != "rate" {
		t.Fatalf("other: %+v", o)
	}

	// a provider with keys, none resting: nothing, and not asked by "@"
	asked = asked[:0]
	k := providerInfo(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://example.invalid/v1", Key: "k"}, nil)
	if k.AccountRests != nil || strings.Contains(strings.Join(asked, ","), "relay@") {
		t.Fatalf("a key provider: %+v, asked %v", k.AccountRests, asked)
	}
}
