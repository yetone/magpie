package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group whose members are all served on Responses is asked there by Pi,
// omp, droid and dsh (#1594, ObsidianArch02). The gateway relays such a
// request to the member it picks as it is: a tool call's arguments reach
// the agent delta by delta, as the vendor sent them, not held until the
// call ends as a Chat answer translated from Responses has them.
func TestGroupOnResponsesRelaysArgumentDeltas(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"codemode","arguments":""}}`,
			`event: response.function_call_arguments.delta`+"\n"+`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"code\":"}`,
			`event: response.function_call_arguments.delta`+"\n"+`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"print(1)\"}"}`,
			`event: response.function_call_arguments.done`+"\n"+`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"code\":\"print(1)\"}"}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"codemode","arguments":"{\"code\":\"print(1)\"}"}}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`,
		))
	}))
	defer up.Close()
	for _, id := range []string{"r1", "r2"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "GPTs", Members: []string{"r1/gpt-5.5", "r2/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"model":"group/gpts","input":"hi","stream":true,"tools":[{"type":"function","name":"codemode","parameters":{"type":"object"}}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	out := rec.Body.String()
	for _, d := range []string{`"delta":"{\"code\":"`, `"delta":"\"print(1)\"}"`} {
		if !strings.Contains(out, d) {
			t.Fatalf("the argument delta %s didn't reach the agent:\n%s", d, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/responses") {
		t.Fatalf("the member was asked on %v", paths)
	}
}

// A group of members that all serve Responses, one best there, is asked
// on Responses (provider.GroupAPI; zyh6036 on X), and the gateway relays
// it as it is to each: the relay best on Chat that serves Responses too is
// asked on /responses, not translated to Chat.
func TestGroupOfResponsesServersRelaysOnResponses(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"r1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}},
		{ID: "both", Name: "Both", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "subs", Name: "Subs", Members: []string{"both/gpt-5.5", "resp/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	// the member best on Chat, first in the group, is asked on /responses
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"group/subs","input":"hi"}`)))
	mu.Lock()
	defer mu.Unlock()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") || !slices.Equal(paths, []string{"/v1/responses"}) {
		t.Fatalf("the group was asked on %v: %d %s", paths, rec.Code, rec.Body)
	}
}
