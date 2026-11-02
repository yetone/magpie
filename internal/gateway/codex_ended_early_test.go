package gateway

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// lockedLog is the standard logger's output for a test, safe to read
// while the gateway's goroutines write it.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func catchLog(t *testing.T) *lockedLog {
	t.Helper()
	l := &lockedLog{}
	old := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(old) })
	return l
}

// waitLogged waits for the log to have a line holding every one of want.
func waitLogged(t *testing.T, l *lockedLog, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, line := range strings.Split(l.String(), "\n") {
			all := true
			for _, w := range want {
				all = all && strings.Contains(line, w)
			}
			if all {
				return line
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line holds %q; the log:\n%s", want, l.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func codexUpstreamAt(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	codexSignedIn(t)
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
}

// wjh66 on Discord (0.1.2696, Windows): a Codex turn on gpt-6.1-sol showed
// "reconnecting 5/5", then "error decoding response body" — reqwest's
// words for a body whose connection broke while it was read — and app.log
// had nothing of the turn. The ChatGPT backend's connection broken off
// inside a chunk, as a reset does, reaches Codex as a whole HTTP reply
// whose stream says it failed (no broken body of magpie's for Codex to
// fail to decode), and app.log says which side ended it and how.
func TestCodexReplyBrokenOffLogged(t *testing.T) {
	logs := catchLog(t)
	codexUpstreamAt(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		events := sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`data: {"type":"response.output_text.delta","delta":"half a reply"}`)
		// no Content-Type, as the ChatGPT backend streams; a chunk
		// promised longer than what comes, and the connection closed
		rw.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
		rw.WriteString(strconv.FormatInt(int64(len(events)+64), 16) + "\r\n" + events)
		rw.Flush()
	})
	server := httptest.NewServer(New().Handler())
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+CodexPath+"/responses",
		strings.NewReader(`{"model":"gpt-6.1-sol","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.160.0 (Windows 10.0.26100; x86_64)")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("magpie's reply broke off under the client (Codex: error decoding response body): %v\n%s", err, body)
	}
	if !strings.Contains(string(body), "half a reply") || !strings.Contains(string(body), "response.failed") {
		t.Fatalf("the reply's delta and its failure weren't both passed on:\n%s", body)
	}
	line := waitLogged(t, logs, "gateway: codex:", "gpt-6.1-sol", "reading OpenAI's stream failed", "before its last event")
	if !strings.Contains(line, "told it as response.failed") {
		t.Fatalf("the line doesn't say what the client was told: %s", line)
	}
	waitLogged(t, logs, "gpt-6.1-sol", "failed: 502", "before its last event")
}

// The other side: the client going away mid-reply (Codex's own timeout, a
// proxy between them, a window closed) is logged as the client's end, so
// a report of a broken reply tells the leg it broke on.
func TestCodexClientGoneLogged(t *testing.T) {
	logs := catchLog(t)
	codexUpstreamAt(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.1-sol"}}`,
			`data: {"type":"response.output_text.delta","delta":"half a reply"}`))
		w.(http.Flusher).Flush()
		// the reply goes on until magpie lets go of it: a write to the
		// client that left is how magpie learns it did
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":" more"}`))
			w.(http.Flusher).Flush()
		}
	})
	server := httptest.NewServer(New().Handler())
	defer server.Close()
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("POST", server.URL+CodexPath+"/responses",
		strings.NewReader(`{"model":"gpt-6.1-sol","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(res.Body)
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the delta never came: %v", err)
		}
		if strings.Contains(l, "half a reply") {
			break
		}
	}
	res.Body.Close() // the connection with it: the client is gone
	waitLogged(t, logs, "gateway: codex:", "gpt-6.1-sol", "before its last event", "went away")
}

// The same failure over and over is logged once a minute, with how many
// times it came in between.
func TestFailedCallLineOncePerMinute(t *testing.T) {
	failedLog.Lock()
	failedLog.last = nil
	failedLog.Unlock()
	c := Call{Agent: "codex", Model: "gpt-6.1-sol", Provider: "openai", Status: 502, Millis: 1200, Error: "OpenAI closed the stream"}
	now := time.Date(2026, 11, 2, 15, 27, 0, 0, time.UTC)
	line, ok := failedCallLine(c, now)
	if !ok || !strings.Contains(line, "codex's call to gpt-6.1-sol on openai failed: 502 after 1.2s: OpenAI closed the stream") {
		t.Fatalf("first failure: %v %q", ok, line)
	}
	for i := range 3 {
		if _, ok := failedCallLine(c, now.Add(time.Duration(i+1)*time.Second)); ok {
			t.Fatal("the same failure was logged again within the minute")
		}
	}
	other := c
	other.Status = 499
	if _, ok := failedCallLine(other, now.Add(time.Second)); !ok {
		t.Fatal("another failure was held with the first")
	}
	line, ok = failedCallLine(c, now.Add(time.Minute+time.Second))
	if !ok || !strings.Contains(line, "and 3 times more like it") {
		t.Fatalf("after the minute: %v %q", ok, line)
	}
	if _, ok := failedCallLine(Call{Status: 200, Model: "x"}, now); ok {
		t.Fatal("a call that didn't fail was logged")
	}
}
