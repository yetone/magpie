package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// #4775 review: earned upload time is only ever time spent waiting on the
// client. A body sent at once earns far more than it took to read (2 MiB
// earns 64s at unroutedUploadRate), and that credit buys a later internal
// hold nothing: one held after its body by a lock routing needs is
// answered 504 at unroutedMost, as before the upload credit.
func TestUnroutedFastUploadBanksNothing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	unroutedLimits(t, 20*time.Millisecond, 500*time.Millisecond)
	unknownLimit(t, time.Hour)
	s, up := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	sticks.Lock()
	var once sync.Once
	unlock := func() { once.Do(sticks.Unlock) }
	t.Cleanup(unlock)

	body := unroutedHi + strings.Repeat(" ", 2<<20)
	got := posted(srv, strings.NewReader(body))
	u := watchedHold(t, s)
	var r unroutedReply
	select {
	case r = <-got:
	case <-time.After(20 * time.Second):
		// a banked credit would hold it for the 64s the body earned
		unlock()
		t.Fatal("an internal hold after a fast upload was not answered at unroutedMost")
	}
	unlock()
	if r.err != nil || r.code != http.StatusGatewayTimeout || !strings.Contains(r.body, "without routing it to any provider") {
		t.Fatalf("internal hold after a fast upload: %d %q %v", r.code, r.body, r.err)
	}
	if n := u.bodyRead.Load(); n != int64(len(body)) {
		t.Fatalf("the hold counted %d body bytes, want %d", n, len(body))
	}
	if len(up.sent("text")) != 0 {
		t.Fatal("a request held before routing reached the provider")
	}
}

// emptyPiece is a piece of an encoded body that keeps the wire busy and
// decodes to nothing: a gzip member whose only content is a 60 KB extra
// field, or a zstd skippable frame of 60 KB.
func emptyPiece(t *testing.T, encoding string) []byte {
	t.Helper()
	filler := bytes.Repeat([]byte{'x'}, 60<<10)
	switch encoding {
	case "gzip":
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Header.Extra = filler
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	case "zstd":
		b := binary.LittleEndian.AppendUint32(nil, 0x184D2A50)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(filler)))
		return append(b, filler...)
	}
	t.Fatalf("no empty piece for %q", encoding)
	return nil
}

// #4775 review: a body that keeps arriving but decodes to next to nothing
// (empty gzip members, zstd skippable frames) is never idle on the socket
// and never reaches the decoded size cap, and it earns upload time far
// faster than it is read. Its earned time stops at unroutedUploadGrace, so
// it is answered 408 there rather than held for as long as it is sent.
func TestUnroutedEmptyEncodedStreamIsBounded(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			unroutedLimits(t, 20*time.Millisecond, 500*time.Millisecond)
			unknownLimit(t, time.Hour)
			grace := unroutedUploadGrace
			unroutedUploadGrace = time.Second
			t.Cleanup(func() { unroutedUploadGrace = grace })
			s, up := slowEyed(t, 0)
			srv := httptest.NewServer(s.Front())
			t.Cleanup(srv.Close)

			piece := emptyPiece(t, encoding)
			conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Encoding: %s\r\nTransfer-Encoding: chunked\r\n\r\n", encoding)
			// 60 KB every 50ms: about 1.2 MB/s, forty times the rate
			stop, sent := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(sent)
				tick := time.NewTicker(50 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
					}
					if _, err := fmt.Fprintf(conn, "%x\r\n%s\r\n", len(piece), piece); err != nil {
						return
					}
				}
			}()
			t.Cleanup(func() {
				close(stop)
				conn.Close()
				<-sent
			})
			u := watchedHold(t, s)

			res, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("a stream that decodes to nothing was not answered: %v", err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(b), "its body was still arriving") {
				t.Fatalf("a stream that decodes to nothing: %d %s", res.StatusCode, b)
			}
			// it earned more than the whole hold: the cap ended it, not
			// the rate or the decoder
			if earned := time.Duration(u.bodyRead.Load()) * time.Second / unroutedUploadRate; earned < 2*(unroutedMost+unroutedUploadGrace) {
				t.Fatalf("the stream earned only %s; the test needs it to outearn the cap", earned)
			}
			if len(up.sent("text")) != 0 {
				t.Fatal("a body still arriving reached the provider")
			}
		})
	}
}

// #4775 review: an upload that earns at least as fast as it is read holds
// its charged time still, here a sliver short of unroutedMost. most's
// timer looks again no sooner than unroutedRecheck, rather than every
// sliver for as long as the body keeps coming.
func TestUnroutedMostRecheckIsNoSpin(t *testing.T) {
	unroutedLimits(t, time.Hour, 800*time.Millisecond)
	now := time.Now()
	k := &waitKind{kind: "body", n: 1, since: now}
	u := &unroutedHold{f: &inflight{start: now.Add(-(unroutedMost - 100*time.Microsecond))}, kinds: []*waitKind{k}}
	u.bodyRead.Store(1 << 40)
	var looks, running atomic.Int64
	var stopped atomic.Bool
	u.wmu.Lock()
	u.most = time.AfterFunc(0, func() {
		running.Add(1)
		defer running.Add(-1)
		if stopped.Load() {
			return
		}
		looks.Add(1)
		u.checkMost()
	})
	u.wmu.Unlock()
	time.Sleep(time.Second)
	stopped.Store(true)
	for running.Load() > 0 {
		time.Sleep(time.Millisecond)
	}
	u.most.Stop()
	// a second at a look per 100ms is about 10; at a look per sliver it
	// is thousands
	if n := looks.Load(); n > 30 {
		t.Fatalf("most's timer looked %d times in a second at a hold standing still", n)
	}
	if n := looks.Load(); n == 0 {
		t.Fatal("most's timer never looked")
	}
}
