package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestUsageNamesTheKeyThatAnswered(t *testing.T) {
	for _, proto := range provider.Protocols {
		t.Run(string(proto), func(t *testing.T) {
			fresh(t)
			var tried []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if key == "" {
					key = r.Header.Get("x-api-key")
				}
				tried = append(tried, key)
				w.Header().Set("Content-Type", "application/json")
				if key == "personal-secret" {
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"message":"rate limited"}}`)
					return
				}
				switch proto {
				case provider.Chat:
					io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":10}}}`)
				case provider.Responses:
					io.WriteString(w, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":30,"output_tokens":5,"input_tokens_details":{"cached_tokens":10}}}`)
				case provider.Anthropic:
					io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":5,"cache_read_input_tokens":10}}`)
				}
			}))
			defer up.Close()
			p := provider.Provider{ID: "plan", Name: "Plan", Key: "personal-secret", KeyName: "Personal", Models: []string{"m1"},
				Keys: []provider.KeyAccount{{Name: "Off", Key: "off-secret", Off: true}, {Name: "Team", Key: "team-secret"}}}
			path, body := "/v1/chat/completions", chatReq
			switch proto {
			case provider.Chat:
				p.Chat = up.URL + "/v1"
			case provider.Responses:
				p.Responses = up.URL + "/v1"
				path, body = "/v1/responses", `{"model":"plan/m1","input":"hi"}`
			case provider.Anthropic:
				p.Anthropic = up.URL
				path, body = "/v1/messages", `{"model":"plan/m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			if code, reply := sendTo(s, path, body); code != 200 || !strings.Contains(reply, "ok") {
				t.Fatalf("reply: %d %s", code, reply)
			}
			r := lastUsage(t)
			if r.ProviderKeyID != provider.KeyID("team-secret") || r.ProviderKeyName != "Team" || r.Input != 20 || r.Output != 5 || r.CacheRead != 10 ||
				strings.Join(tried, ",") != "personal-secret,team-secret" {
				t.Fatalf("usage: %+v; tried %v", r, tried)
			}
			if err := provider.RenameKey("plan", r.ProviderKeyID, "Renamed team"); err != nil {
				t.Fatal(err)
			}
			if err := provider.UseKey("plan", r.ProviderKeyID); err != nil {
				t.Fatal(err)
			}
			if code, _ := sendTo(s, path, body); code != 200 {
				t.Fatal(code)
			}
			recs := usage.Load(time.Time{})
			if len(recs) != 2 || recs[0].ProviderKeyID != recs[1].ProviderKeyID || recs[1].ProviderKeyName != "Renamed team" {
				t.Fatalf("after promotion and rename: %+v", recs)
			}
			log, err := os.ReadFile(usage.Path())
			if err != nil || strings.Contains(string(log), "team-secret") || strings.Contains(string(log), "personal-secret") {
				t.Fatalf("usage log leaked a key or was unreadable: %v", err)
			}
		})
	}
}

func TestImageUsageNamesTheKey(t *testing.T) {
	s, _ := easeled(t)
	p, _ := provider.Find("art")
	p.KeyName = "Images"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if code, body := sendTo(s, "/v1/images/generations", `{"model":"art/gpt-image-1","prompt":"a bird"}`); code != 200 {
		t.Fatalf("reply: %d %s", code, body)
	}
	if r := lastUsage(t); r.ProviderKeyID != provider.KeyID("key") || r.ProviderKeyName != "Images" || r.Input != 7 || r.Output != 100 {
		t.Fatalf("image usage: %+v", r)
	}
}
