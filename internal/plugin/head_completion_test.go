package plugin

import (
	"context"
	"encoding/base64"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A short reply can finish between the head reader's empty-queue check
// and its check for a completed call. The queued head still belongs to
// that reply, and its body and terminal error must reach the consumer.
func TestHostHeadArrivesWithCompletion(t *testing.T) {
	for _, why := range []string{"", "the vendor ended early"} {
		t.Run(why, func(t *testing.T) {
			f := newFakeHost(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for range 128 {
				id, c := f.begin(t, true)
				type result struct {
					head   message
					answer *message
					err    error
				}
				read := make(chan result, 1)
				go func() {
					h, a, err := c.headRead(ctx)
					read <- result{h, a, err}
				}()
				runtime.Gosched()
				c.push(message{Event: "head", Status: 200})
				c.push(message{Event: "chunk", Data: base64.StdEncoding.EncodeToString([]byte("reply"))})
				answer := message{}
				if why != "" {
					answer = errAnswer(why)
				}
				c.transition(callAnswered, nil, &answer)
				r := <-read
				if r.err != nil || r.head.Status != 200 {
					c.release()
					t.Fatalf("completed reply's head = %+v, %v", r.head, r.err)
				}
				b := readBody(f, id, c, ctx)
				b.answer = r.answer
				got, err := io.ReadAll(b)
				b.Close()
				if string(got) != "reply" || why == "" && err != nil || why != "" && (err == nil || !strings.Contains(err.Error(), why)) {
					t.Fatalf("body = %q, %v; want reply, error %q", got, err, why)
				}
			}
		})
	}
}
