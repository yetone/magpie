package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// archivedOf reads every object in b back, by the call's provider.
func archivedOf(t *testing.T, b *memBucket) map[string][]Archived {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string][]Archived{}
	for name, data := range b.objs {
		var a Archived
		if err := json.Unmarshal(data, &a); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(data), archiveKey) || strings.Contains(string(data), "magpie-gateway-key") || strings.Contains(string(data), "Bearer k") {
			t.Errorf("a secret in %s:\n%s", name, data)
		}
		out[a.Call.Provider] = append(out[a.Call.Provider], a)
	}
	return out
}

// With the request archive on, a System One request an app sends magpie
// (/v1/systemone) is kept as a chat request is, and its route, its Recent
// call and its usage row name where (wangb on Discord: 请求存档开启, 但是
// systemone 的请求没有记录下来). Off, nothing is kept.
func TestArchiveKeepsSystemOne(t *testing.T) {
	fresh(t)
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"jev-latest","answers":{"intent":{"type":"choice","choice":"bug","confidence":0.9}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer jev.Close()
	if err := provider.Save(provider.Provider{ID: "load-a", Name: "A", Key: "ka-secret", Decide: jev.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	s := New()
	post := func() {
		t.Helper()
		body := `{"model":"load-a/jev-latest","questions":{"intent":{"type":"choice","criteria":{"bug":"a bug report"}}},"state":{"message":"it crashes, token ` + archiveKey + `"}}`
		req := httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer magpie-gateway-key")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"bug"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		archivePending.Wait()
	}

	post()
	if len(b.objs) != 0 || s.Recent()[0].Archive != "" {
		t.Fatalf("archived while off: %v", keys(b.objs))
	}

	settings.Save(settings.Settings{RequestArchive: true})
	post()
	got := archivedOf(t, b)["load-a"]
	if len(got) != 1 || len(b.objs) != 1 {
		t.Fatalf("uploaded %v", keys(b.objs))
	}
	a := got[0]
	c := s.Recent()[0] // the newest first
	r := s.trace.routes[len(s.trace.routes)-1]
	if c.Archive == "" || c.Archive != a.ID || r.Archive != a.ID || c.Route != r.ID {
		t.Fatalf("call %q, route %q (%d/%d), archived %q", c.Archive, r.Archive, c.Route, r.ID, a.ID)
	}
	if a.Request.Path != "/v1/systemone" || !strings.Contains(a.Request.Body, "it crashes") || a.Request.Headers["Authorization"][0] != "[REDACTED]" ||
		a.Response.Status != 200 || !strings.Contains(a.Response.Body, `"bug"`) || a.Call.Model != "load-a/jev-latest" || a.Call.Status != 200 {
		t.Errorf("archived %+v", a)
	}
	n := 0
	for _, row := range usage.Load(time.Time{}) {
		if row.Provider == "load-a" && row.Archive != "" {
			if row.Archive != a.ID {
				t.Errorf("usage row names %q, archived %q", row.Archive, a.ID)
			}
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d usage rows name the archive", n)
	}
}

// With the request archive on, the System One question a routing group's
// Jev classifier asks for a turn, which magpie makes itself, is kept too:
// the request as it went, its key taken out, and the answer; its usage
// row, magpie's own, names it.
func TestArchiveKeepsJevClassifier(t *testing.T) {
	s, _, _, j := jevved(t, "", provider.Rule{Use: "b/big", Intent: "debugging"})
	j.choice, j.sure = "debugging", 0.7
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	settings.Save(settings.Settings{RequestArchive: true})
	if _, r := postOK(t, s, "s1", chat("why does this crash?", nil, 0, "")); r.Rule.Classified.Intent != "debugging" {
		t.Fatalf("classified %+v", r.Rule.Classified)
	}
	archivePending.Wait()
	got := archivedOf(t, b)
	jev := got["ts"]
	if len(jev) != 1 || len(got["b"]) != 1 {
		t.Fatalf("uploaded %v", keys(b.objs))
	}
	a := jev[0]
	if a.Request.Method != "POST" || a.Request.Path != "/v1/systemone" || a.Request.Headers["Authorization"][0] != "[REDACTED]" ||
		!strings.Contains(a.Request.Body, "why does this crash?") || !strings.Contains(a.Response.Body, "debugging") ||
		a.Response.Status != 200 || a.Call.Kind != "routing" || a.Call.Model != "ts/jev-latest" || a.Call.Archive != a.ID {
		t.Errorf("archived %+v", a)
	}
	n := 0
	for _, row := range usage.Load(time.Time{}) {
		if row.Provider == "ts" {
			if row.Archive != a.ID || row.Kind != "routing" {
				t.Errorf("Jev's row %+v, archived %q", row, a.ID)
			}
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d rows of Jev's", n)
	}
}
