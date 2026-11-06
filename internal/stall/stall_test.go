package stall

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func get(ctx context.Context, url string, after time.Duration) (int, error) {
	ctx, w := Start(ctx, after)
	defer w.Stop()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, w.Err(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(w.Reader(res.Body))
	return len(b), w.Err(err)
}

// A body that keeps coming, slowly, for many times the stall's while is
// read whole: only silence ends it (#5572).
func TestSlowButMovingIsReadWhole(t *testing.T) {
	const chunks = 12
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < chunks; i++ {
			if _, err := w.Write([]byte("0123456789")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()
	n, err := get(context.Background(), srv.URL, 250*time.Millisecond)
	if err != nil || n != chunks*10 {
		t.Fatalf("got %d bytes, err %v; want %d", n, err, chunks*10)
	}
}

// A server that says nothing, before its answer or part way through it,
// ends with ErrStalled.
func TestSilenceIsAStall(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"no answer": func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
		"part way": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("0123456789"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, err := get(ctx, srv.URL, 100*time.Millisecond)
			if !errors.Is(err, ErrStalled) || ctx.Err() != nil {
				t.Fatalf("err = %v, want a stall", err)
			}
		})
	}
}
