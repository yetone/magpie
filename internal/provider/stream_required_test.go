package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

// crsStreamOnly is a claude-relay-service Responses endpoint as
// Cyrus_4433 on X met it: "2 个模型中有 2 个没有响应；gpt-6-sol: 400 ·
// Stream must be set to true", for a request that didn't ask for a stream.
func crsStreamOnly(t *testing.T, plain bool) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		if !gjson.GetBytes(b, "stream").Bool() {
			rw.WriteHeader(http.StatusBadRequest)
			if plain {
				_, _ = io.WriteString(rw, "Stream must be set to true")
			} else {
				_, _ = io.WriteString(rw, `{"error":{"message":"Stream must be set to true","type":"invalid_request_error"}}`)
			}
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(rw, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

func sandboxHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// A model's test on a backend that only streams is asked once more
// streamed, and is green: the editor's Test models, one model at a time.
func TestProbeStreamsWhereTheBackendOnlyStreams(t *testing.T) {
	for _, plain := range []bool{false, true} {
		sandboxHome(t)
		srv, asked := crsStreamOnly(t, plain)
		p := Provider{ID: "relay", Name: "My Relay", Responses: srv.URL + "/v1", Key: "k", Models: []string{"gpt-6-sol", "gpt-6.1-sol"}}
		rs := p.TestModels(context.Background(), []string{"gpt-6-sol", "gpt-6.1-sol"})
		if len(rs) == 0 {
			t.Fatalf("plain=%v: no results", plain)
		}
		for _, r := range rs {
			if !r.OK {
				t.Errorf("plain=%v: %+v", plain, r)
			}
		}
		on, off := 0, 0
		for _, b := range asked() {
			if gjson.Get(b, "stream").Bool() {
				on++
			} else {
				off++
			}
		}
		if on != 2 || off != 2 {
			t.Errorf("plain=%v: %d streamed, %d not: %v", plain, on, off, asked())
		}
	}
}

// Protocol detection probes the same way, so a stream-only Responses
// endpoint is found.
func TestDetectFindsAStreamOnlyBackend(t *testing.T) {
	sandboxHome(t)
	srv, _ := crsStreamOnly(t, false)
	p := Provider{ID: "relay", Name: "My Relay", Responses: srv.URL + "/v1", Key: "k"}
	ds, err := p.Detect(context.Background(), srv.URL+"/v1", "gpt-6-sol")
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, d := range ds {
		if d.Protocol == Responses && d.OK {
			ok = true
		}
	}
	if !ok {
		t.Errorf("%+v", ds)
	}
}

func TestStreamRequired(t *testing.T) {
	for msg, want := range map[string]bool{
		"Stream must be set to true":                          true,
		`{"error":{"message":"stream must be true"}}`:         true,
		"400 · Stream must be set to true":                    true,
		"This endpoint only supports streaming":               true,
		"streaming is required":                               true,
		"max_tokens must be at most 8":                        false,
		"stream_options is only allowed when stream is true.": false,
		"model not found":                                     false,
	} {
		if got := StreamRequired([]byte(msg)); got != want {
			t.Errorf("%q: %v", msg, got)
		}
	}
}
