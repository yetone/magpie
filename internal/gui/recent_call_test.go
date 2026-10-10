package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestRecentCallBodiesOnDemand(t *testing.T) {
	sandboxHome(t)
	long := strings.Repeat("body", 70<<10)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"`+long+`"}}]}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "recent", Name: "Recent", Chat: up.URL, Key: "fixture"}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	was := served.Load()
	served.Store(s)
	t.Cleanup(func() { served.Store(was) })
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	query := func(c gateway.Call) string {
		return "/api/gateway/call?" + url.Values{"id": {strconv.FormatUint(c.ID, 10)}}.Encode()
	}
	ask := func(content string) gateway.Call {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"recent/m","messages":[{"role":"user","content":"`+content+`"}]}`))
		r.Header.Set("User-Agent", "codex")
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("gateway: %d %.200s", w.Code, w.Body)
		}
		return s.Recent()[0]
	}
	first := ask(long)
	w := get("/api/providers")
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(state.Gateway.Calls) != 1 {
		t.Fatalf("summary: %d, %d calls", w.Code, len(state.Gateway.Calls))
	}
	if c := state.Gateway.Calls[0]; c.RequestBody != "" || c.ResponseBody != "" || !c.Time.Equal(first.Time) || c.Model != first.Model || c.Status != first.Status {
		t.Fatal("providers must keep call metadata without sending bodies")
	}
	// Saves return the same summary, not another copy of every body.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers/arrange", strings.NewReader(`{"order":["recent"]}`)))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"requestBody"`) || strings.Contains(w.Body.String(), `"responseBody"`) {
		t.Fatal("a provider write returned call bodies")
	}
	w = get(query(first))
	var detail gateway.Call
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || detail.RequestBody != first.RequestBody || detail.ResponseBody != first.ResponseBody || !detail.RequestTruncated || !detail.ResponseTruncated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the detail must keep both captured previews and truncation flags")
	}
	if s.Recent()[0].RequestBody == "" || s.Recent()[0].ResponseBody == "" {
		t.Fatal("building a summary changed the gateway's captured bodies")
	}
	guard := webGuard("session", "fixture", 0, mux)
	w = httptest.NewRecorder()
	guard.ServeHTTP(w, httptest.NewRequest("GET", query(first), nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("body detail bypassed the web session")
	}
	// the next call, the same agent's to the same model, is told apart
	// from the first by its id, not by when it came (#1521)
	second := ask("second " + long)
	if second.ID == first.ID {
		t.Fatalf("two calls share id %d", first.ID)
	}
	if err := json.Unmarshal(get(query(second)).Body.Bytes(), &detail); err != nil || !strings.Contains(detail.RequestBody, "second") {
		t.Fatal("the second call's detail isn't its own")
	}
	if err := json.Unmarshal(get(query(first)).Body.Bytes(), &detail); err != nil || strings.Contains(detail.RequestBody, "second") {
		t.Fatal("the first call's detail isn't its own")
	}
	for _, path := range []string{"/api/gateway/call", "/api/gateway/call?id=x", "/api/gateway/call?id=999999", "/api/gateway/call?time=" + url.QueryEscape(first.Time.Format(time.RFC3339Nano))} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Fatalf("unknown call: %d", w.Code)
		}
	}
	for range 40 {
		ask(long)
	}
	w = get("/api/providers")
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	summary, _ := json.Marshal(state.Gateway.Calls)
	captured, _ := json.Marshal(s.Recent())
	if len(state.Gateway.Calls) != 40 || len(summary)*100 >= len(captured) {
		t.Fatalf("recent summaries: %d bytes, captured calls: %d bytes", len(summary), len(captured))
	}
	t.Logf("40 calls: %d bytes of metadata instead of %d bytes with bodies", len(summary), len(captured))
	if w := get(query(first)); w.Code != http.StatusNotFound {
		t.Fatalf("an evicted call returned %d", w.Code)
	}
	served.Store(nil)
	if w := get(query(first)); w.Code != http.StatusNotFound {
		t.Fatalf("a gateway served elsewhere returned %d", w.Code)
	}
}
