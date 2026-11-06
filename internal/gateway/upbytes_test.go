package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request asked twice — its first member overloaded, the next answering
// — goes to its ledger row with what its tries sent up to the vendor:
// UpBytes the bytes that went on the wire, tries summed, PlainBytes the
// last try's body before compression. Neither was in a row before (#4456),
// so a session's upstream traffic and what a hand-on re-sent could only be
// guessed at from outside.
func TestLedgerKeepsUpBytes(t *testing.T) {
	fresh(t)
	var sizes []int
	overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"overloaded"}}`)
	}))
	t.Cleanup(overloaded.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: overloaded.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	t.Cleanup(ok.Close)
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m"}, Chat: ok.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	if len(sizes) != 2 {
		t.Fatalf("the vendor saw %d asks (sizes %v)", len(sizes), sizes)
	}
	route := lastRoute(s).ID
	var rows []usage.Record
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows = rows[:0]
		for _, u := range usage.Load(time.Time{}) {
			if u.RouteID == route && !u.HandedOn {
				rows = append(rows, u)
			}
		}
		if len(rows) >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("the request's row: %d rows", len(rows))
	}
	row := rows[0]
	if row.Tries != 2 || row.Status != 200 {
		t.Fatalf("the request's row: tries %d, status %d", row.Tries, row.Status)
	}
	if row.UpBytes != sizes[0]+sizes[1] {
		t.Fatalf("UpBytes %d, the tries' bodies %v", row.UpBytes, sizes)
	}
	if row.PlainBytes != sizes[1] {
		t.Fatalf("PlainBytes %d, the last try's body %d", row.PlainBytes, sizes[1])
	}
}

// A turn of Codex's own on its ChatGPT sign-in goes zstd-compressed, and
// is the case #4456 asks about most: its row says the compressed bytes
// that went up and the plain body they hold. That route reaches the
// ChatGPT backend from the mux, not through serve, so it sums its tries
// in a context of its own; before, its row had neither.
func TestCodexOwnTurnKeepsUpBytes(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	zstdTurns(t)
	var wire, plain, down int
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		wire = len(b)
		if r.Header.Get("Content-Encoding") != "zstd" {
			t.Errorf("sent as %q, not zstd", r.Header.Get("Content-Encoding"))
		}
		dec, _ := zstd.NewReader(nil)
		defer dec.Close()
		p, err := dec.DecodeAll(b, nil)
		if err != nil {
			t.Errorf("decoding the turn: %v", err)
		}
		plain = len(p)
		w.Header().Set("Content-Type", "text/event-stream")
		n, _ := io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
		down = n
	})
	codexChatGPTAuth(t, "me@example.com")
	rec := httptest.NewRecorder()
	turn := `{"model":"gpt-5.5","stream":true,"input":"` + strings.Repeat("the same words again ", 400) + `"}`
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(turn))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	u := usage.Load(time.Time{})
	if len(u) != 1 {
		t.Fatalf("usage %+v", u)
	}
	if wire == 0 || wire >= plain {
		t.Fatalf("the turn went up as %d bytes of %d", wire, plain)
	}
	if u[0].UpBytes != wire || u[0].PlainBytes != plain {
		t.Fatalf("row says up %d plain %d; the backend got %d of %d", u[0].UpBytes, u[0].PlainBytes, wire, plain)
	}
	if u[0].DownBytes != down {
		t.Fatalf("row says down %d; the backend answered %d bytes", u[0].DownBytes, down)
	}
}

// A request asked again inside one try — Aloha's relay refusing both key
// headers, asked again with x-api-key alone (RetryOneAuth) — went up
// twice, and its row counts both: the try's second request is on the
// wire as much as its first.
func TestLedgerCountsATrysSecondAsk(t *testing.T) {
	fresh(t)
	var sizes []int
	up := &adaptiveVendor{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(raw))
		if r.Header.Get("x-api-key") != "" && r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"type":"authentication_error","message":"鉴权 header， x-api-key 和 Authorization 不可以同时存在"}}`)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		up.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL, Models: []string{"claude-sonnet-4-5"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", `{"model":"relay/claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(sizes) != 2 {
		t.Fatalf("the relay saw %d asks", len(sizes))
	}
	var rows []usage.Record
	deadline := time.Now().Add(10 * time.Second)
	for len(rows) == 0 && time.Now().Before(deadline) {
		for _, u := range usage.Load(time.Time{}) {
			if u.Provider == "relay" && !u.HandedOn {
				rows = append(rows, u)
			}
		}
		if len(rows) == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].UpBytes != sizes[0]+sizes[1] || rows[0].PlainBytes != sizes[1] {
		t.Fatalf("row says up %d plain %d; the relay got %v", rows[0].UpBytes, rows[0].PlainBytes, sizes)
	}
}

// rowOf is the row a request ended with (not a try that handed it on),
// once it is in the ledger.
func rowOf(t *testing.T, of func(usage.Record) bool) usage.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rows []usage.Record
		for _, u := range usage.Load(time.Time{}) {
			if of(u) && !u.HandedOn {
				rows = append(rows, u)
			}
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if len(rows) > 1 || time.Now().After(deadline) {
			t.Fatalf("the request's rows: %+v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A request's row says what came back down as well as what went up:
// DownBytes the bytes of the vendors' replies magpie read, its tries
// summed, the overloaded member's refusal too, whole or streamed
// (faithleysath, matrix0487 on Discord). Before, a row had none.
func TestLedgerKeepsDownBytes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			served := 0
			serve := func(w http.ResponseWriter, s string) {
				n, _ := io.WriteString(w, s)
				mu.Lock()
				served += n
				mu.Unlock()
			}
			overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				serve(w, `{"error":{"message":"overloaded"}}`)
			}))
			t.Cleanup(overloaded.Close)
			ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					serve(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`, `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, `data: [DONE]`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				serve(w, chatOK)
			}))
			t.Cleanup(ok.Close)
			for _, p := range []provider.Provider{
				{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: overloaded.URL + "/v1"},
				{ID: "b", Name: "B", Key: "k", Models: []string{"m"}, Chat: ok.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := postAs(t, s, "", fmt.Sprintf(`{"model":"group/g","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream))
			if code != 200 || !strings.Contains(body, "hello") {
				t.Fatalf("%d %s", code, body)
			}
			route := lastRoute(s).ID
			row := rowOf(t, func(u usage.Record) bool { return u.RouteID == route })
			mu.Lock()
			want := served
			mu.Unlock()
			if row.Tries != 2 || row.DownBytes != want {
				t.Fatalf("row: tries %d, down %d; the vendors answered %d bytes", row.Tries, row.DownBytes, want)
			}
		})
	}
}
