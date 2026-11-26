package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

var tokenRe = regexp.MustCompile(`"token":"([^"]*)"`)

// pageToken is the token the runtime in the page navigated to holds.
func pageToken(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	w := pageGet(h, path)
	m := tokenRe.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil || m[1] == "" {
		t.Fatalf("%s: %d, no token in %.300s", path, w.Code, w.Body)
	}
	return m[1]
}

// storeCall is a page's call into its store, as runtime.js makes it: a
// simple POST from its opaque origin.
func storeCall(h http.Handler, ref string, call map[string]any, hdr ...string) (int, map[string]any) {
	b, _ := json.Marshal(call)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/db", strings.NewReader(string(b)))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// A page that declares db reaches its store from its own origin with the
// token magpie put in it, and what it writes the agent reads.
func TestArtifactPageStore(t *testing.T) {
	h := artifacts(t)
	said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{
		"html": "<!doctype html><html><head><title>Board</title></head><body><p>x", "capabilities": map[string]any{"db": map[string]any{}, "user": map[string]any{}},
	})
	if isErr || !strings.Contains(said, "capabilities: db, user") || !strings.Contains(said, "artifact_data") {
		t.Fatalf("publish: %s", said)
	}
	id := publishedID(t, said)
	page := ArtifactPagePath + id + "/"
	w := pageGet(h, page)
	body := w.Body.String()
	if !strings.Contains(body, `"db":{}`) || !strings.Contains(body, `"user":{}`) || strings.Index(body, "window, \"claude\"") > strings.Index(body, "<title>") || !strings.Contains(w.Header().Get("Vary"), "Sec-Fetch-Mode") {
		t.Fatalf("the page navigated to: %v %.400s", w.Header(), body)
	}
	tok := pageToken(t, h, page)

	if code, out := storeCall(h, id, map[string]any{"t": tok, "op": "set", "path": "tasks/t1", "data": map[string]any{"title": "ship"}}); code != 200 {
		t.Fatalf("set: %d %v", code, out)
	}
	code, out := storeCall(h, id, map[string]any{"t": tok, "op": "get", "path": "tasks/t1"})
	doc, _ := out["doc"].(map[string]any)
	if data, _ := doc["data"].(map[string]any); code != 200 || data["title"] != "ship" || doc["version"] != 1.0 {
		t.Fatalf("get: %d %v", code, out)
	}
	if said, _ := artifactsCall(t, h, artifactDataTool, map[string]any{"id": id, "action": "get", "path": "tasks/t1"}); !strings.Contains(said, `"ship"`) {
		t.Fatalf("the agent reads: %s", said)
	}
	if code, out := storeCall(h, id, map[string]any{"t": tok, "op": "query", "collection": "tasks", "where": []any{map[string]any{"field": "title", "op": "==", "value": "ship"}}}); code != 200 || len(out["docs"].([]any)) != 1 {
		t.Fatalf("query: %d %v", code, out)
	}
	if code, out := storeCall(h, id, map[string]any{"t": tok, "op": "acquire", "path": "locks/a", "holder": "tab1", "ttlMs": 5000}); code != 200 || out["lease"].(map[string]any)["acquired"] != true {
		t.Fatalf("acquire: %d %v", code, out)
	}
	// a ttl so large that it wraps round in nanoseconds is the longest lease
	code, out = storeCall(h, id, map[string]any{"t": tok, "op": "acquire", "path": "locks/b", "holder": "tab1", "ttlMs": int64(9223372036854775)})
	exp, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(out["lease"].(map[string]any)["expiresAt"]))
	if code != 200 || time.Until(exp) < 9*time.Minute {
		t.Fatalf("a huge ttl: %d %v", code, out)
	}
	if code, out := storeCall(h, id, map[string]any{"t": tok, "op": "set", "path": "tasks", "data": map[string]any{}}); code != 400 || errCode(out) != "invalid_argument" {
		t.Fatalf("a set at a collection: %d %v", code, out)
	}
	if code, out := storeCall(h, id, map[string]any{"t": tok, "op": "drop"}); errCode(out) != "capability_removed" {
		t.Fatalf("an unknown call: %d %v", code, out)
	}
}

// The token is never in a page a script fetches, nor good for another
// artifact; a page without db, another computer and a rebound name reach
// no store; and the exemption lets no other POST through.
func TestArtifactPageStoreRefused(t *testing.T) {
	h := artifacts(t)
	db := artifact.Capabilities{"db": json.RawMessage(`{}`)}
	a, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>a"), Capabilities: db})
	b, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>b"), Capabilities: db})
	plain, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>plain")})
	pa := ArtifactPagePath + a.ID + "/"
	if w := pageGet(h, pa, "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code != 200 || strings.Contains(w.Body.String(), `"token"`) || strings.Contains(w.Body.String(), "claude") {
		t.Fatalf("a fetch of the page got the runtime: %d %.300s", w.Code, w.Body)
	}
	tok := pageToken(t, h, pa)
	set := func(t string) map[string]any {
		return map[string]any{"t": t, "op": "set", "path": "x/1", "data": map[string]any{"n": 1}}
	}
	for _, c := range []struct {
		name   string
		ref    string
		call   map[string]any
		hdr    []string
		status int
		code   string
	}{
		{"no token", a.ID, set(""), nil, 403, "not_granted"},
		{"another artifact's token", b.ID, set(tok), nil, 403, "not_granted"},
		{"a page without db", plain.ID, set(pageToken(t, h, ArtifactPagePath+plain.ID+"/")), nil, 403, "not_granted"},
		{"a rebound name", a.ID, set(tok), []string{"Host", "rebound.example:3425"}, 403, ""},
		{"a missing artifact", "aaaaaaaaaaaaaaaa", set(tok), nil, 0, "not_granted"},
	} {
		r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+c.ref+"/_api/db", strings.NewReader(func() string { b, _ := json.Marshal(c.call); return string(b) }()))
		r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
		r.Header.Set("Origin", "null")
		for i := 0; i+1 < len(c.hdr); i += 2 {
			if c.hdr[i] == "Host" {
				r.Host = c.hdr[i+1]
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if c.status != 0 && w.Code != c.status || w.Code < 400 || errCode(out) != c.code {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest(http.MethodPost, pa+"_api/db", strings.NewReader(`{"t":"`+tok+`","op":"get","path":"x/1"}`))
	r.RemoteAddr, r.Host = "192.0.2.7:4000", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() != 403 {
		t.Error("another computer reached a store")
	}
	// the exemption is the db call alone: corsGuard turns away every
	// other POST from Origin null, before any handler sees it
	for _, p := range []string{pa, pa + "x.json", pa + "_api/events", pa + "_api/other", pa + "_api/db/x", "/v1/chat/completions", "/v1/messages"} {
		r := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{"t":"`+tok+`"}`))
		r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
		r.Header.Set("Origin", "null")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("a POST from Origin null to %s: %d %v", p, w.Code, w.Header())
		}
	}
	// a page can't publish a file where its store calls go
	if _, err := artifact.PublishPage(artifact.Publish{Page: []byte("<p>"), Files: map[string][]byte{"_api/db": []byte("x")}}); err == nil {
		t.Error("a file at _api/db was published")
	}
}

// A write, the agent's too, is told on the page's events stream.
func TestArtifactPageEvents(t *testing.T) {
	h := artifacts(t)
	a, _ := artifact.PublishPage(artifact.Publish{Page: []byte("<p>a"), Capabilities: artifact.Capabilities{"db": json.RawMessage(`{}`)}})
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+ArtifactPagePath+a.ID+"/_api/events?t="+tok, nil)
	req.Header.Set("Origin", "null")
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("events: %d %v", res.StatusCode, res.Header)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctxDone:
				return
			}
		}
	}()
	// the stream is open once its first comment is in
	for l := ""; l != ": magpie"; {
		select {
		case l = <-lines:
		case <-time.After(10 * time.Second):
			t.Fatal("the stream didn't open")
		}
	}
	if said, isErr := artifactsCall(t, h, artifactDataTool, map[string]any{"id": a.ID, "action": "set", "path": "notes/n1", "data": map[string]any{"text": "hi"}}); isErr {
		t.Fatal(said)
	}
	for {
		select {
		case l := <-lines:
			if l == `data: {"paths":["notes/n1"]}` {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the agent's write wasn't told")
		}
	}
}

// artifact_data: the agent reads and writes the store, by action.
func TestArtifactDataTool(t *testing.T) {
	h := artifacts(t)
	said, _ := artifactsCall(t, h, artifactPublishTool, map[string]any{"html": "<p>x"})
	id := publishedID(t, said)
	call := func(args map[string]any) (string, bool) {
		args["id"] = id
		return artifactsCall(t, h, artifactDataTool, args)
	}
	if said, isErr := call(map[string]any{"action": "set", "path": "tasks/t1", "data": map[string]any{"title": "one", "done": false}}); isErr || !strings.Contains(said, "doesn't declare db") {
		t.Fatalf("a write to a page without db says so: %s", said)
	}
	if said, _ := call(map[string]any{"action": "list"}); !strings.Contains(said, `"tasks": 1`) {
		t.Fatalf("list: %s", said)
	}
	if said, isErr := call(map[string]any{"action": "str_replace", "path": "tasks/t1", "old_str": `"done": false`, "new_str": `"done": true`}); isErr {
		t.Fatalf("str_replace: %s", said)
	}
	if said, isErr := call(map[string]any{"action": "str_replace", "path": "tasks/t1", "old_str": `"done": true`, "new_str": `"done": tru`}); !isErr || !strings.Contains(said, "isn't a JSON object") {
		t.Fatalf("a str_replace that breaks the JSON: %s", said)
	}
	if said, _ := call(map[string]any{"action": "query", "path": "tasks", "where": []any{map[string]any{"field": "done", "op": "==", "value": true}}}); !strings.Contains(said, `"t1"`) {
		t.Fatalf("query after str_replace: %s", said)
	}
	if said, isErr := call(map[string]any{"action": "batch", "ops": []any{map[string]any{"op": "set", "path": "tasks/t2", "data": map[string]any{}}, map[string]any{"op": "delete", "path": "tasks/t1"}}}); isErr || !strings.Contains(said, "2 writes") {
		t.Fatalf("batch: %s", said)
	}
	if said, _ := call(map[string]any{"action": "get", "path": "tasks/t1"}); !strings.Contains(said, "no document") {
		t.Fatalf("get after delete: %s", said)
	}
	if said, isErr := call(map[string]any{"action": "update", "path": "tasks/t2", "data": []any{1}}); !isErr {
		t.Fatalf("an update with an array: %s", said)
	}
	if said, isErr := call(map[string]any{"action": "fly"}); !isErr || !strings.Contains(said, "str_replace") {
		t.Fatalf("an unknown action: %s", said)
	}
	// publishing it with db, and read_artifact saying what is kept
	if said, _ := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "<p>y", "version": 1, "capabilities": map[string]any{"db": map[string]any{}}}); strings.Contains(said, "data is empty") {
		t.Fatalf("a publish over kept data says it is empty: %s", said)
	}
	if said, _ := artifactsCall(t, h, artifactReadTool, map[string]any{"id": id}); !strings.Contains(said, "capabilities: db") || !strings.Contains(said, "data: 1 documents") {
		t.Fatalf("read: %s", said)
	}
	if said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": id, "html": "<p>z", "version": 2, "capabilities": map[string]any{"permissions": map[string]any{}}}); !isErr {
		t.Fatalf("permissions declared: %s", said)
	}
}
