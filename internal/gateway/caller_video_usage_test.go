package gateway

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

func TestCallerUsageVideos(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		status int
	}{
		{"a fox", 200},
		{"TOOLONG", 400},
		{"NOID", 502},
	} {
		for _, header := range []string{"Authorization", "x-api-key", "query"} {
			t.Run(tc.prompt+"/"+header, func(t *testing.T) {
				grokSignedIn(t)
				newGrokMedia(t)
				keys, secrets := newCaller(t, "Video client")
				r := httptest.NewRequest("POST", "/v1/videos", strings.NewReader(`{"model":"grok/grok-imagine-video","prompt":"`+tc.prompt+`"}`))
				r.RemoteAddr = "192.168.1.9:5000"
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Magpie-Session", "video-session")
				if header == "query" {
					r.URL.RawQuery = "key=" + secrets[0]
				} else if header == "Authorization" {
					r.Header.Set(header, "Bearer "+secrets[0])
				} else {
					r.Header.Set(header, secrets[0])
				}
				w := httptest.NewRecorder()
				lanGuard(New().Handler()).ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatal(w.Code, w.Body)
				}
				rec := lastUsage(t)
				if rec.CallerKeyID != keys[0].ID || rec.CallerKeyName != "Video client" || rec.Provider != "grok" || rec.Model != "grok-imagine-video" || rec.Status != tc.status || rec.Session != "video-session" {
					t.Fatalf("video caller attribution: %+v", rec)
				}
				rows, _, _ := usage.Ledger(usage.All, usage.Filter{CallerKey: keys[0].ID})
				if len(rows) != 1 {
					t.Fatal("video missing from caller-key ledger", rows)
				}
				log, err := os.ReadFile(usage.Path())
				if err != nil || strings.Contains(string(log), secrets[0]) {
					t.Fatal("video usage leaked the caller credential", err)
				}
			})
		}
	}
}
