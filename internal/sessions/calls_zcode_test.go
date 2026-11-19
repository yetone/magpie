package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// zcodeCalls are the ZCode calls Calls gives, by message id.
func zcodeCalls(t *testing.T) map[string]Call {
	t.Helper()
	out := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "zcode" {
			if _, dup := out[c.Msg]; dup {
				t.Fatalf("message %s counted twice", c.Msg)
			}
			out[c.Msg] = c
		}
	}
	return out
}

// ZCode's calls reach the usage ledger too: the Sessions page read its
// database all along, and the Usage page showed none of it.
func TestZCodeCalls(t *testing.T) {
	setupAgents(t)
	cs := zcodeCalls(t)
	// ses_zcA's own reply went through magpie's gateway (the route magpie
	// writes names it "magpie"), so it is the gateway's record to keep; the
	// others are read here: ses_zcB's (a subagent, counted under the session
	// it ran in) and ses_zcC's
	if len(cs) != 2 {
		t.Fatalf("want 2 ZCode calls, got %d: %+v", len(cs), cs)
	}
	if _, dup := cs["msg_z3"]; dup {
		t.Fatalf("a call made to magpie's gateway was read from the database too: %+v", cs["msg_z3"])
	}
	if b := cs["msg_z4"]; b.Model != "glm-5.1-air" || b.Tokens != (Tokens{50, 5, 0, 0, 0}) {
		t.Fatalf("the subagent's reply: %+v", b)
	}
	if c := cs["msg_z5"]; c.Tokens != (Tokens{10, 1, 0, 0, 0}) {
		t.Fatalf("ses_zcC's reply: %+v", c)
	}
}

// jiangshangmuyun on X: a provider the user added in ZCode by hand
// ("new-provider"), pointed at this magpie's gateway, is the gateway's as
// "magpie" is. Its calls were listed again as a local session's beside the
// gateway's rows for the same calls. A provider elsewhere, or on this
// machine at another port, keeps its calls.
func TestZCodeHandAddedGatewayProvider(t *testing.T) {
	home := setupAgents(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3499")
	dir := filepath.Join(home, ".zcode", "v2")
	os.MkdirAll(dir, 0o755)
	// ZCode 3.14 and later: provider_config.json's rules
	os.WriteFile(filepath.Join(dir, "provider_config.json"), []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[
		{"providerId":"new-provider","providerName":"new-provider","enabled":true,"config":{"api":{"type":"anthropic-messages","baseUrl":"http://127.0.0.1:3499"}}},
		{"providerId":"zai","providerName":"Z.ai","enabled":true,"config":{"api":{"type":"anthropic-messages","baseUrl":"https://open.bigmodel.cn/api/anthropic"}}},
		{"providerId":"ollama","providerName":"Ollama","enabled":true,"config":{"api":{"type":"openai-chat-completions","baseUrl":"http://127.0.0.1:11434/v1"}}}]}}}`), 0o600)
	// an older ZCode's config.json
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"provider":{"mine":{"name":"mine","kind":"openai-compatible","options":{"baseURL":"http://localhost:3499/v1"}}}}`), 0o600)
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, p := range []string{"new-provider", "mine", "zai", "ollama"} {
		at := int64(1790496200000 + i*10000)
		ocInsert(t, db, "ses_zcC", "msg_h"+p, at, at+5000, `{"id":"msg_h`+p+`","role":"assistant","modelId":"trae-cn/deepseek-v4.1-flash","providerID":"`+p+`","tokens":{"input":20,"output":2,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":`+strconv.FormatInt(at, 10)+`,"completed":`+strconv.FormatInt(at+5000, 10)+`}}`)
	}
	Reset()
	cs := zcodeCalls(t)
	for _, gone := range []string{"msg_hnew-provider", "msg_hmine"} {
		if c, ok := cs[gone]; ok {
			t.Fatalf("a call to a provider at the gateway's address was read as a local session's: %+v", c)
		}
	}
	for _, kept := range []string{"msg_hzai", "msg_hollama"} {
		if _, ok := cs[kept]; !ok {
			t.Fatalf("%s, to a provider elsewhere, was left out: %+v", kept, cs)
		}
	}
}

func TestGatewayBase(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3499")
	for base, want := range map[string]bool{
		"http://127.0.0.1:3499":     true,
		"http://localhost:3499/v1":  true,
		"https://127.0.0.1:3499/":   true,
		"http://[::1]:3499":         true,
		"http://0.0.0.0:3499":       true,
		"http://127.0.0.1:11434/v1": false,
		"https://open.bigmodel.cn":  false,
		"http://example.com:3499":   false,
		"ws://127.0.0.1:3499":       false,
		"":                          false,
		"127.0.0.1:3499":            false,
	} {
		if got := gatewayBase(base); got != want {
			t.Errorf("gatewayBase(%q) = %v, want %v", base, got, want)
		}
	}
}
