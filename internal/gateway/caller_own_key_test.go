package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// A key given its own value — one clients already send, from another
// gateway (love1sbug on X) — is that key from another computer and from
// this one, as a sk-magpie- key is: counted as it, and never sent on.
func TestOwnKeyValueIsTheKey(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("caller credential reached provider:", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	const own = "cpa-family-key-0042"
	if _, err := access.Update("add-key", access.Change{Name: "Family", Secret: own}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler()) // as the real server has it
	call := func(from, key string) int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = from
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := call("192.0.2.7:4000", own); code != 200 {
		t.Fatal("from another computer", code)
	}
	if code := call("127.0.0.1:4000", own); code != 200 {
		t.Fatal("from this computer", code)
	}
	if code := call("192.0.2.7:4000", own+"x"); code != http.StatusUnauthorized {
		t.Fatal("a wrong key", code)
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 2 {
		t.Fatalf("records: %+v", recs)
	}
	for _, rec := range recs {
		if rec.CallerKeyName != "Family" {
			t.Fatalf("not counted as the key: %+v", rec)
		}
	}
	keys, err := access.List()
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if code := call("192.0.2.7:4000", own); code != http.StatusUnauthorized {
		t.Fatal("a disabled key from another computer", code)
	}
}
