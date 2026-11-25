package netproxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
)

// recorder is a TCP relay in front of addr that notes the length of every
// TLS record the client sends through it, by type, on each connection.
func recorder(t *testing.T, addr string) (string, func() [][2]int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen [][2]int // type, length, in the order sent
	var conns []net.Conn
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", addr)
			if err != nil {
				c.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, c, s)
			mu.Unlock()
			wg.Add(2)
			go func() { defer wg.Done(); defer c.Close(); io.Copy(c, s) }()
			go func() {
				defer wg.Done()
				defer s.Close()
				head := make([]byte, 5)
				for {
					if _, err := io.ReadFull(c, head); err != nil {
						return
					}
					n := int(binary.BigEndian.Uint16(head[3:]))
					rec := make([]byte, n)
					if _, err := io.ReadFull(c, rec); err != nil {
						return
					}
					mu.Lock()
					seen = append(seen, [2]int{int(head[0]), n})
					mu.Unlock()
					if _, err := s.Write(append(head, rec...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() [][2]int {
		mu.Lock()
		defer mu.Unlock()
		out := append([][2]int(nil), seen...)
		seen = nil
		return out
	}
}

// #7567 (k0ngk0ng): behind a fake-IP proxy whose path mangled Go's
// requests and passed curl's and Python's, a 70KB body went out in some
// forty records of about a TCP segment each, where OpenSSL writes 16KB
// ones. After a record error on a host, its requests go in whole records;
// a host with none keeps Go's own sizes.
func TestRecordErrorSendsWholeRecords(t *testing.T) {
	clearProxyEnv(t)
	srv, d := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Got", r.Proto)
		if n != 70<<10 {
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	addr, records := recorder(t, srv.Listener.Addr().String())
	base := "https://" + addr
	u, _ := url.Parse(base)
	t.Cleanup(func() {
		h1For.Lock()
		delete(h1For.at, hostKey(u))
		h1For.Unlock()
	})
	body := bytes.Repeat([]byte("x"), 70<<10)
	post := func() (proto string, small, full int) {
		t.Helper()
		d.CloseIdleConnections()
		records()
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
		res, err := d.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("the body didn't arrive whole: %d", res.StatusCode)
		}
		for _, r := range records() {
			if r[0] != 23 { // application data
				continue
			}
			if r[1] >= 16<<10 {
				full++
			} else {
				small++
			}
		}
		return res.Header.Get("X-Got"), small, full
	}

	if _, small, _ := post(); small < 10 {
		t.Fatalf("Go's own record sizes sent only %d small records: the test can't tell the shapes apart", small)
	}
	markH2Broken(&http.Request{URL: u})
	proto, small, full := post()
	if proto != "HTTP/1.1" {
		t.Errorf("after a record error the request went over %s", proto)
	}
	// the handshake's Finished, the request's tail, and room for one more
	if small > 3 || full < 4 {
		t.Errorf("after a record error 70KB went in %d small records and %d whole ones, not whole records", small, full)
	}

	other, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if h2BrokenFor(&http.Request{URL: other}) {
		t.Fatal("another host:port was marked")
	}
}
