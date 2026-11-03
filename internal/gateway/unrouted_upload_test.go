package gateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #4775: the client's upload can outlast the routing hold while wire
// bytes are still arriving. Exercise the listener's guards and TLS, with
// only a local fake provider. A large first write buys ample rate room;
// waiting on the watch's measured body time makes the old cutoff fail.
func TestUnroutedProgressingUploadOutlastsMost(t *testing.T) {
	for _, encoding := range []string{"identity", "chunked", "gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			unroutedLimits(t, 20*time.Millisecond, time.Second)
			s, up := slowEyed(t, 0)
			srv := httptest.NewTLSServer(s.Front())
			defer srv.Close()
			pr, pw := io.Pipe()
			defer pr.Close()
			defer pw.Close()
			// Incompressible text keeps the compressed wire credit well
			// above the three seconds spent reading, even under -race.
			rng := rand.New(rand.NewPCG(1, 2))
			noise := make([]byte, 192<<10)
			for i := range noise {
				noise[i] = byte(rng.Uint32())
			}
			body := []byte(`{"model":"probe/text","messages":[{"role":"user","content":"` + base64.StdEncoding.EncodeToString(noise) + `"}]}`)
			wire := body
			if encoding != "chunked" {
				wire = encodedBody(t, body, encoding)
			}
			req, err := http.NewRequest("POST", srv.URL+"/v1/chat/completions", pr)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if encoding != "chunked" {
				req.ContentLength = int64(len(wire))
				req.Header.Set("Content-Encoding", encoding)
			}
			got := make(chan unroutedReply, 1)
			finished := make(chan struct{})
			defer func() {
				pw.Close()
				pr.Close()
				<-finished
			}()
			go func() {
				defer close(finished)
				res, err := srv.Client().Do(req)
				if err != nil {
					got <- unroutedReply{err: err}
					return
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				got <- unroutedReply{res.StatusCode, string(b), err}
			}()
			if _, err := io.CopyN(pw, bytes.NewReader(wire), int64(len(wire)-1)); err != nil {
				t.Fatal(err)
			}
			if r := heldPast(t, watchedHold(t, s), 3*unroutedMost, got); r != nil {
				t.Fatalf("progressing upload cut off before routing: %d %q %v", r.code, r.body, r.err)
			}
			if n := watchedHold(t, s).bodyRead.Load(); n != int64(len(wire)-1) {
				t.Fatalf("upload credit counts %d bytes, want %d wire bytes", n, len(wire)-1)
			}
			if len(up.sent("text")) != 0 {
				t.Fatal("provider asked before the upload finished")
			}
			if _, err := pw.Write(wire[len(wire)-1:]); err != nil {
				t.Fatal(err)
			}
			pw.Close()
			r := <-got
			if r.err != nil || r.code != http.StatusOK {
				t.Fatalf("completed upload: %d %q %v", r.code, r.body, r.err)
			}
			if len(up.sent("text")) != 1 {
				t.Fatal("completed upload was not sent exactly once")
			}
		})
	}
}

// A stalled upload still ends on its socket's idle deadline even after
// receiving enough bytes to earn additional time at the routing hold.
func TestUnroutedUploadCreditKeepsIdleDeadline(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, time.Second)
	s, up := slowEyed(t, 0)
	s.requestLimits.readTimeout = 200 * time.Millisecond
	srv := httptest.NewServer(s.Front())
	defer srv.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	body := unroutedHi + strings.Repeat(" ", 128<<10)
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body)+1, body)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(b), "request body read timed out") {
		t.Fatalf("idle upload: %d %s %v", res.StatusCode, b, err)
	}
	if len(up.sent("text")) != 0 {
		t.Fatal("stalled upload reached the provider")
	}
}

// Upload time cannot be banked for a later internal hang, nor can the
// declared length buy time for bytes the client has not sent. Use fixed
// times to pin the rate boundary without a scheduler-dependent race.
func TestUnroutedUploadTimeIsBounded(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		read    int64
		spent   time.Duration
		reading bool
		want    time.Duration
	}{
		{"no bytes", 0, 8 * time.Second, true, 10 * time.Second},
		{"below rate", unroutedUploadRate, 8 * time.Second, true, 9 * time.Second},
		{"at rate", 8 * unroutedUploadRate, 8 * time.Second, true, 2 * time.Second},
		{"above rate", 16 * unroutedUploadRate, 8 * time.Second, true, 2 * time.Second},
		{"between reads", 8 * unroutedUploadRate, 8 * time.Second, false, 2 * time.Second},
		{"fast body then internal hold", 16 * unroutedUploadRate, time.Second, false, 9 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &waitKind{kind: "body", spent: tc.spent}
			if tc.reading {
				k.n, k.spent, k.since = 1, 0, now.Add(-tc.spent)
			}
			u := &unroutedHold{f: &inflight{start: now.Add(-10 * time.Second)}, kinds: []*waitKind{k}}
			u.bodyRead.Store(tc.read)
			if got := u.unmaskedLocked(now); got != tc.want {
				t.Fatalf("internal hold = %s, want %s", got, tc.want)
			}
			// Unknown-work protection is independent of earned bytes.
			if got := u.unknownLocked(now); got != 10*time.Second {
				t.Fatalf("upload bytes excused unknown work: %s", got)
			}
		})
	}
}

// Compressed bodies can carry arbitrarily many wire bytes with little
// decoded output (empty gzip members or zstd skippable frames). They must
// still exhaust the hold, even while making progress and never going idle.
func TestUnroutedUploadAllowanceHasCeiling(t *testing.T) {
	now := time.Now()
	for _, read := range []int64{32 << 20, 1<<63 - 1} {
		u := &unroutedHold{
			f:     &inflight{start: now.Add(-11 * time.Minute)},
			kinds: []*waitKind{{kind: "body", n: 1, since: now.Add(-10 * time.Minute)}},
		}
		u.bodyRead.Store(read)
		// At most eight minutes of upload may be excused. Together
		// with the two-minute base budget this bounds a non-masking hold.
		if held := u.unmaskedLocked(now); held != 3*time.Minute {
			t.Errorf("%d wire bytes: charged hold = %s, want 3m", read, held)
		}
	}
}
