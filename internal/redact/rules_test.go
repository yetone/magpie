package redact

import (
	"strings"
	"testing"
)

// a gateway's key, synthetic, as #195 has it
const ocKey = "oc_sk_AAAABBBBccccDDDD1234eeeeFFFF5678"

// every row of #195's table, and what must stay alone as key is let in
func TestMaskGatewayKey(t *testing.T) {
	cases := []struct {
		in   string
		gone bool
		kind string
	}{
		{in: `"key": "` + ocKey + `"`, gone: true, kind: "API_KEY"},
		{in: `\"key\": \"` + ocKey + `\"`, gone: true, kind: "API_KEY"},
		{in: `"apiKey": "` + ocKey + `"`, gone: true, kind: "API_KEY"},
		{in: `"token": "` + ocKey + `"`, gone: true, kind: "API_KEY"},
		{in: "bare " + ocKey + " value", gone: true, kind: "API_KEY"},
		{in: "or_sk_0123456789abcdefghijKLMNOP", gone: true, kind: "API_KEY"},
		// key alone, a value that is no API key
		{in: `{"id": "relay", "key": "Zq81nVx0pL2mWe7RtY4u"}`, gone: true, kind: "SECRET"},
		{in: `{\"key\": \"Zq81nVx0pL2mWe7RtY4u\"}`, gone: true, kind: "SECRET"},
		{in: "key: Zq81nVx0pL2mWe7RtY4u", gone: true, kind: "SECRET"},
		{in: `"api-key": "Zq81nVx0pL2mWe7RtY4u"`, gone: true, kind: "SECRET"},
		{in: `"apikey": "Zq81nVx0pL2mWe7RtY4u"`, gone: true, kind: "SECRET"},
		{in: `\"apiKey\": \"Zq81nVx0pL2mWe7RtY4u\"`, gone: true, kind: "SECRET"},
		// and key as code says it
		{in: `{"key": "value", "sort_key": "xyz"}`},
		{in: `key = "someIdentifier"`},
		{in: `key = "someVeryLongIdentifierName"`},
		{in: `sort_key: created_at_2024_rows`},
		{in: `monkey: banana1234567890abc`},
		{in: `obj.key = "abc123"`},
		{in: `"key": "row-12"`},
		{in: "the task_sk_something_long_name_here module"}, // no digits
		{in: "abcde_sk_0123456789abcdefghijKL"},             // five letters is a word
		{in: `"key": "${OPENCODE_API_KEY_0123456789}"`},
	}
	for _, c := range cases {
		out, n := Mask(c.in, Options{Secrets: true})
		if !c.gone {
			if n != 0 || out != c.in {
				t.Errorf("%q masked: %q", c.in, out)
			}
			continue
		}
		if n != 1 || !strings.Contains(out, "{{"+c.kind+"_") {
			t.Errorf("%q: %d masked, want one %s: %q", c.in, n, c.kind, out)
		}
		if back := Restore(out, false); back != c.in {
			t.Errorf("round trip: %q → %q → %q", c.in, out, back)
		}
	}
	// the same key, the same placeholder, whatever field it is in
	a, _ := Mask(`"key": "`+ocKey+`"`, Options{Secrets: true})
	b, _ := Mask("OC_API_KEY="+ocKey, Options{Secrets: true})
	c, _ := Mask(ocKey, Options{Secrets: true})
	if pa, pb, pc := placeholderRe.FindString(a), placeholderRe.FindString(b), placeholderRe.FindString(c); pa == "" || pa != pb || pa != pc {
		t.Fatalf("placeholders differ: %q %q %q", a, b, c)
	}
}

// the markers of a rule's names, made from its list, are in any name it takes
func TestSecretMarkers(t *testing.T) {
	for _, name := range []string{"DB_PASSWORD", "passwd", "client_secret", "Token", "apiKey", "API-KEY", "AccessKey", "private_key", "CREDENTIAL", "Credentials"} {
		if !containsAny(name, secretNames.markers()) {
			t.Errorf("%s: no marker", name)
		}
	}
}

func TestRuleKind(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "CUSTOM",
		"api key":                            "API_KEY",
		"oc-sk":                              "OC_SK",
		"{{x}}":                              "X",
		"9lives":                             "CUSTOM_9LIVES",
		"数据库":                                "CUSTOM",
		"a_very_long_kind_name_that_goes_on": "A_VERY_LONG_KIND_NAME_TH",
	} {
		if got := RuleKind(in); got != want {
			t.Errorf("RuleKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckRules(t *testing.T) {
	got, err := CheckRules([]Rule{{Kind: " gw key ", Prefix: " acme_ "}, {Regex: "  "}, {Kind: "GW_KEY", Prefix: "acme_"}, {Kind: "ns", Regex: `ns-([0-9a-f]{12})`}})
	if err != nil || len(got) != 2 || got[0] != (Rule{Kind: "GW_KEY", Prefix: "acme_"}) || got[1].Kind != "NS" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range [][]Rule{
		{{Regex: `ns-([0-9a-f`}},                   // doesn't compile
		{{Regex: `(?P<x>a)(?<!b)`}},                // RE2 has no lookbehind
		{{Regex: `x*`}},                            // matches nothing at all
		{{Prefix: "ab"}},                           // too short a prefix
		{{Prefix: "acme_", Regex: "acme_[a-z]+"}},  // both
		{{Regex: strings.Repeat("a", maxRegex+1)}}, // too long
	} {
		if _, err := CheckRules(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	many := make([]Rule, MaxRules+1)
	for i := range many {
		many[i] = Rule{Prefix: "p" + strings.Repeat("x", i+2)}
	}
	if _, err := CheckRules(many); err == nil {
		t.Error("too many rules accepted")
	}
}

func TestCustomRules(t *testing.T) {
	rules := []Rule{
		{Kind: "GW_KEY", Prefix: "acme-"},
		{Kind: "NS_TOKEN", Regex: `nstok:([0-9a-f]{12})`},
		{Kind: "BROKEN", Regex: `x(`}, // a settings file written by hand: left out
	}
	o := Options{Secrets: true, Rules: rules}
	in := "use acme-Zx9_ab-12cd then nstok:0123456789ab and " + openaiKey
	out, n := Mask(in, o)
	if n != 3 || strings.Contains(out, "acme-Zx9") || strings.Contains(out, "0123456789ab") ||
		!strings.Contains(out, "{{GW_KEY_") || !strings.Contains(out, "nstok:{{NS_TOKEN_") || !strings.Contains(out, "{{API_KEY_") {
		t.Fatalf("%d: %q", n, out)
	}
	if back := Restore(out, false); back != in {
		t.Fatalf("restored %q", back)
	}
	// the same placeholder turn after turn
	again, _ := Mask("again: acme-Zx9_ab-12cd", o)
	if p1, p2 := placeholderRe.FindString(out), placeholderRe.FindString(again); p1 == "" || p1 != p2 {
		t.Fatalf("placeholders differ: %q %q", out, again)
	}
	// a prefix wants 8 more characters after it
	if _, n := Mask("acme-short", o); n != 0 {
		t.Fatal("short value masked")
	}
	// only with the secrets
	if _, n := Mask(in, Options{Rules: rules}); n != 0 {
		t.Fatal("masked with secrets off")
	}
	// where the user's and magpie's find the same value, the user's name
	// wins; where they overlap, the first to start and the longest
	o = Options{Secrets: true, Rules: []Rule{{Kind: "OC", Prefix: "oc_sk_"}}}
	if out, _ := Mask(`"key": "`+ocKey+`"`, o); !strings.Contains(out, "{{OC_") || strings.Contains(out, "API_KEY") {
		t.Fatalf("user's kind lost: %q", out)
	}
	o = Options{Secrets: true, Rules: []Rule{{Kind: "TAIL", Regex: `BBBBcccc[A-Za-z0-9]+`}}}
	if out, n := Mask(ocKey, o); n != 1 || !strings.HasPrefix(out, "{{API_KEY_") {
		t.Fatalf("overlap: %q", out)
	}
	// a kind with digits restores, in a stream too
	o = Options{Secrets: true, Rules: []Rule{{Kind: "K8S", Prefix: "k8s-tok-"}}}
	out, _ = Mask("k8s-tok-abcdef123456", o)
	if !strings.HasPrefix(out, "{{K8S_") || Restore(out, false) != "k8s-tok-abcdef123456" || partialTail(out[:len(out)-3]) == 0 {
		t.Fatalf("digits in kind: %q", out)
	}
}
