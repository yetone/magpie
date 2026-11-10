package lazyre

import (
	"sync"
	"testing"
)

func TestCompiledOnFirstUse(t *testing.T) {
	r := New(`a(b+)`)
	if r.re != nil {
		t.Fatal("compiled at New")
	}
	if got := r.FindStringSubmatch("xabbb"); len(got) != 2 || got[1] != "bbb" {
		t.Fatalf("FindStringSubmatch = %q", got)
	}
	if r.re == nil {
		t.Fatal("not compiled after use")
	}
}

func TestConcurrentFirstUse(t *testing.T) {
	r := New(`^[0-9]+$`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !r.MatchString("123") {
				t.Error("no match")
			}
		}()
	}
	wg.Wait()
}

func TestBadPatternPanicsInMustAll(t *testing.T) {
	defer func() {
		allMu.Lock()
		all = all[:len(all)-1]
		allMu.Unlock()
		if recover() == nil {
			t.Fatal("MustAll did not panic on a bad pattern")
		}
	}()
	New(`(`)
	MustAll()
}
