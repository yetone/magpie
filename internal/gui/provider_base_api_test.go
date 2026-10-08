package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The API a custom provider's Base URL was given as in its editor is kept
// (01huadalang on Discord: 我选 response 保存了然后再打开一会就变成 openai
// 兼容了). A provider saved as OpenAI compatible, then given its URL as
// OpenAI Responses, has both URLs: the editor showed the first, chat, and
// so OpenAI compatible. The pick is saved, sent back to the editor, kept by
// a save that doesn't say, and dropped once its URL is gone.
func TestProviderBaseAPIKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	shown := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
		var got struct{ Providers []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, p := range got.Providers {
			if p["id"] == "relay" {
				s, _ := p["baseAPI"].(string)
				return s
			}
		}
		t.Fatalf("relay not listed: %s", w.Body)
		return ""
	}
	const u = "http://127.0.0.1:1/v1"
	// added as OpenAI compatible, as the editor sends it
	post("save", `{"id":"relay","name":"Relay","key":"sk-a","chat":"`+u+`","icon":"generic","baseAPI":"chat","new":true}`)
	if got := shown(); got != "chat" {
		t.Fatalf("added as OpenAI compatible, shown as %q", got)
	}
	// reopened, OpenAI Responses picked and its URL typed: chat's stays (#105)
	post("save", `{"id":"relay","from":"relay","name":"Relay","chat":"`+u+`","responses":"`+u+`","icon":"generic","baseAPI":"responses"}`)
	if got := shown(); got != "responses" {
		t.Fatalf("saved as OpenAI Responses, shown as %q", got)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != u || p.Responses != u {
		t.Fatalf("stored %+v", p)
	}
	// a save that doesn't say which (an older window, another page) keeps it
	post("save", `{"id":"relay","from":"relay","name":"Relay","chat":"`+u+`","responses":"`+u+`","icon":"generic"}`)
	if got := shown(); got != "responses" {
		t.Fatalf("a save without the pick lost it: %q", got)
	}
	// the Responses URL cleared elsewhere (the CLI): no pick is left to show
	p, _ = provider.Find("relay")
	p.Responses = ""
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got != "" {
		t.Fatalf("a pick with no URL kept: %q", got)
	}
}
