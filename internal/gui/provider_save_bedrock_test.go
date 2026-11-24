package gui

// Amazon Bedrock takes an AWS access key in place of its API key (#7687).
// The editor adds one with no key at all; the listing says the vendor is
// "aws" and whether a Secret and a session token are saved, never either.
// A blank token keeps the saved one while the key's ID is the same, a new
// ID drops it, and clearSessionToken or Remove drop it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestProviderSaveBedrockAccessKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	type akJSON struct {
		Vendor    string `json:"vendor"`
		ID        string `json:"id"`
		SecretSet bool   `json:"secretSet"`
		TokenSet  bool   `json:"tokenSet"`
	}
	list := func() (string, map[string]*akJSON, map[string]string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct {
			Providers []struct {
				ID        string  `json:"id"`
				AccessKey *akJSON `json:"accessKey"`
			} `json:"providers"`
			Presets []struct {
				ID     string `json:"id"`
				Vendor string `json:"accessKeyVendor"`
			} `json:"presets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		ps, pre := map[string]*akJSON{}, map[string]string{}
		for _, x := range got.Providers {
			ps[x.ID] = x.AccessKey
		}
		for _, x := range got.Presets {
			pre[x.ID] = x.Vendor
		}
		return w.Body.String(), ps, pre
	}
	if _, _, pre := list(); pre["bedrock"] != "aws" || pre["bedrock-mantle"] != "aws" || pre["deepseek"] != "" {
		t.Fatalf("presets: %v", pre)
	}

	b, err := provider.FromPreset("bedrock")
	if err != nil {
		t.Fatal(err)
	}
	// added with the access key and no API key
	post(`{"new":true,"preset":"bedrock","id":"bedrock","name":"Amazon Bedrock","key":"","chat":"` + b.Chat + `","responses":"` + b.Responses +
		`","anthropic":"` + b.Anthropic + `","accessKeyID":" ASIA1 ","secretAccessKey":" SK-1 ","sessionToken":" TOK-1 "}`)
	p, err := provider.Find("bedrock")
	if err != nil || p.Key != "" || p.AccessKeyID != "ASIA1" || p.SecretAccessKey != "SK-1" || p.SessionToken != "TOK-1" || !p.SignsWithAccessKey() || !p.Ready() {
		t.Fatalf("added: %+v %v", p, err)
	}
	body, ps, _ := list()
	if strings.Contains(body, "SK-1") || strings.Contains(body, "TOK-1") {
		t.Fatal("the listing told the Secret or the token")
	}
	if a := ps["bedrock"]; a == nil || *a != (akJSON{"aws", "ASIA1", true, true}) {
		t.Fatalf("listed: %+v", a)
	}

	// the editor's save, its Secret and token fields left blank
	base := `"id":"bedrock","from":"bedrock","name":"Amazon Bedrock","preset":"bedrock","key":"","chat":"` + b.Chat + `","responses":"` + b.Responses +
		`","anthropic":"` + b.Anthropic + `","models":[],"headers":{}`
	post(`{` + base + `,"accessKeyID":"ASIA1"}`)
	if p, _ = provider.Find("bedrock"); p.SecretAccessKey != "SK-1" || p.SessionToken != "TOK-1" {
		t.Fatalf("blank: %+v", p)
	}
	// the token alone removed
	post(`{` + base + `,"accessKeyID":"ASIA1","clearSessionToken":true}`)
	if p, _ = provider.Find("bedrock"); p.SecretAccessKey != "SK-1" || p.SessionToken != "" {
		t.Fatalf("token removed: %+v", p)
	}
	post(`{` + base + `,"accessKeyID":"ASIA1","sessionToken":"TOK-2"}`)
	// another key: the old token isn't its own
	post(`{` + base + `,"accessKeyID":"AKIA2","secretAccessKey":"SK-2"}`)
	if p, _ = provider.Find("bedrock"); p.AccessKeyID != "AKIA2" || p.SecretAccessKey != "SK-2" || p.SessionToken != "" {
		t.Fatalf("another key: %+v", p)
	}
	post(`{` + base + `,"accessKeyID":"AKIA2","sessionToken":"TOK-3"}`)
	// Remove drops all three
	post(`{` + base + `,"accessKeyID":"","clearAccessKey":true,"key":"ABSK-key"}`)
	if p, _ = provider.Find("bedrock"); p.AccessKeyID != "" || p.SecretAccessKey != "" || p.SessionToken != "" {
		t.Fatalf("removed: %+v", p)
	}
}
