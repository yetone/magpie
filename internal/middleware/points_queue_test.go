package middleware

import (
	"reflect"
	"testing"
)

// The onUsage queue is made when the gateway starts, for every user, with
// or without a module that has onUsage: its slots stay small (a 1024-row
// queue of whole Records held ~1 MB from the start).
func TestOnUsageQueueIsSmall(t *testing.T) {
	o := Points().(*observer)
	stop := o.Serve()
	defer stop()
	o.mu.Lock()
	q := o.queue
	o.mu.Unlock()
	if n := int(reflect.TypeOf(q).Elem().Size()) * cap(q); n > 16<<10 {
		t.Fatalf("the queue's slots take %d bytes, want at most 16 KB", n)
	}
}
