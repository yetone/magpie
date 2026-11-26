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

// A rate limit or quota whose rest ended before the limit did is in the
// state too, as a notice (@eachg0 on X: the red line went with the rest
// while the vendor's "try again in 6 hours" stood): on the account's row
// beside its rests, and on a key's row while the key rests no more. A
// key or model resting shows its rest, not its notice.
func TestLimitNoticesInProviderState(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	at, end := time.Now().Add(-2*time.Hour).Truncate(time.Second), time.Now().Add(4*time.Hour).Truncate(time.Second)
	wasRests, wasUnder, wasOf, wasKey := restsUnder, limitNoticesUnder, limitNoticeOf, keyRestOf
	defer func() { restsUnder, limitNoticesUnder, limitNoticeOf, keyRestOf = wasRests, wasUnder, wasOf, wasKey }()
	restsUnder = func(prefix string) []gateway.Rest {
		return []gateway.Rest{{Why: "rate", Status: 429, Until: end, By: "cooldown", Key: "workbuddy-ai@me@example.com/glm-5.3"}}
	}
	limitNoticesUnder = func(prefix string) []gateway.LimitNotice {
		if prefix != "workbuddy-ai@" {
			return nil
		}
		return []gateway.LimitNotice{
			{Key: "workbuddy-ai@me@example.com", Why: "rate", Status: 429, Error: "try again in 6 hours", At: at, Until: end},
			// resting still: its rest is shown, not this
			{Key: "workbuddy-ai@me@example.com/glm-5.3", Why: "rate", Status: 429, At: at},
		}
	}
	p := provider.Provider{ID: "workbuddy-ai", Name: "WorkBuddy AI", Account: &provider.Account{Agent: "workbuddy-ai", User: "Me@Example.com"}}
	m := providerInfo(p, nil).AccountRests["me@example.com"]
	if len(m) != 2 {
		t.Fatalf("rests %+v, want the model's rest and the account's notice", m)
	}
	if r := m[0]; r.Notice || r.Model != "glm-5.3" || r.By != "cooldown" {
		t.Fatalf("the model's: %+v, want its rest", r)
	}
	if n := m[1]; !n.Notice || n.Model != "" || !n.Until.Equal(end) || !n.At.Equal(at) || n.Error != "try again in 6 hours" || n.Key != "workbuddy-ai@me@example.com" {
		t.Fatalf("the account's: %+v, want its notice", n)
	}

	// a key: its notice while it rests no more, its rest while it does
	keyed := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://example.invalid/v1", Key: "k"}
	key := keyed.RestKey(keyed.KeyList()[0])
	resting := false
	keyRestOf = func(k string) (gateway.Rest, bool) {
		return gateway.Rest{Why: "rate", Status: 429, Until: end, By: "cooldown"}, resting && k == key
	}
	limitNoticeOf = func(k string) (gateway.LimitNotice, bool) {
		return gateway.LimitNotice{Key: k, Why: "quota", Status: 429, At: at}, k == key
	}
	if r := providerInfo(keyed, nil).KeyList[0].Rest; r == nil || !r.Notice || r.Why != "quota" || r.Key != key || !r.Until.IsZero() {
		t.Fatalf("a key resting no more: %+v, want its notice", r)
	}
	resting = true
	if r := providerInfo(keyed, nil).KeyList[0].Rest; r == nil || r.Notice || r.By != "cooldown" {
		t.Fatalf("a key resting: %+v, want its rest", r)
	}
}

// xzhih on X, #7773: an OpenCode Go key's paid model rests alone since
// v0.1.4818, and its row showed only a whole key's rest, so it said
// nothing of which models were out. A key's row now has its models' own
// rests, then the notices of those resting no more, each by its model,
// and the key's own Rest stays nil: the key still serves its others. A
// key of another provider whose id the first starts with, and an account,
// add nothing to it.
func TestKeyModelRestsInProviderState(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	at, end := time.Now().Add(-2*time.Hour).Truncate(time.Second), time.Now().Add(4*time.Hour).Truncate(time.Second)
	wasRests, wasUnder, wasOf, wasKey := restsUnder, limitNoticesUnder, limitNoticeOf, keyRestOf
	defer func() { restsUnder, limitNoticesUnder, limitNoticeOf, keyRestOf = wasRests, wasUnder, wasOf, wasKey }()
	keyRestOf = func(string) (gateway.Rest, bool) { return gateway.Rest{}, false }
	limitNoticeOf = func(string) (gateway.LimitNotice, bool) { return gateway.LimitNotice{}, false }
	p := provider.Provider{ID: "go", Name: "OpenCode Go", Chat: "https://example.invalid/v1", Key: "k"}
	key := p.RestKey(p.KeyList()[0])
	restsUnder = func(string) []gateway.Rest {
		return []gateway.Rest{
			{Why: "quota", Status: 429, Until: end, By: "window", Key: key + "/glm-5.3", Model: "glm-5.3"},
			{Why: "quota", Status: 429, Until: end, By: "window", Key: "google/gemini-x"},
			{Why: "rate", Status: 429, Until: end, Key: "go@me/kimi"},
		}
	}
	limitNoticesUnder = func(string) []gateway.LimitNotice {
		return []gateway.LimitNotice{
			{Key: key + "/kimi-k3", Why: "quota", Status: 429, Error: "usage limit reached", At: at, Until: end},
			// resting still: its rest is shown, not this
			{Key: key + "/glm-5.3", Why: "quota", Status: 429, At: at},
		}
	}
	k := providerInfo(p, nil).KeyList[0]
	if k.Rest != nil {
		t.Fatalf("the key rests as a whole: %+v", k.Rest)
	}
	if len(k.Rests) != 2 {
		t.Fatalf("rests %+v, want glm-5.3's rest and kimi-k3's notice", k.Rests)
	}
	if r := k.Rests[0]; r.Notice || r.Model != "glm-5.3" || r.By != "window" || r.Key != key+"/glm-5.3" {
		t.Fatalf("glm-5.3's: %+v, want its rest", r)
	}
	if n := k.Rests[1]; !n.Notice || n.Model != "kimi-k3" || !n.Until.Equal(end) || n.Error != "usage limit reached" {
		t.Fatalf("kimi-k3's: %+v, want its notice", n)
	}
	// and the page is sent them
	b, _ := json.Marshal(providerInfo(p, nil).KeyList[0])
	if !strings.Contains(string(b), `"rests":[`) {
		t.Fatalf("not sent to the page: %s", b)
	}
}
