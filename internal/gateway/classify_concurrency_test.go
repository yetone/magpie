package gateway

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yetone/magpie/internal/access"
)

type classifyResult struct {
	v      verdict
	cached bool
	err    error
}

func resetClassified(t *testing.T) {
	t.Helper()
	reset := func() {
		classified.Lock()
		defer classified.Unlock()
		clear(classified.m)
		clear(classified.failed)
		clear(classified.last)
		clear(classified.group)
		clear(classified.inflight)
	}
	reset()
	t.Cleanup(reset)
}

func TestConcurrentClassificationSharesAnswer(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		results := make(chan classifyResult, 17)
		var calls atomic.Int32
		want := verdict{Intent: "refactoring", Effort: "high", Score: 2, Sure: 0.9}
		ask := func(access.Identity, string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			return want, nil
		}
		run := func() {
			v, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"refactoring"}, before{}, true, "rename this package")
			results <- classifyResult{v, cached, err}
		}
		go run()
		synctest.Wait()
		for range 16 {
			go run()
		}
		synctest.Wait()
		if n := calls.Load(); n != 1 {
			t.Errorf("concurrent calls = %d, want 1", n)
		}
		close(release)
		synctest.Wait()
		shared := 0
		for range 17 {
			got := <-results
			if got.v != want || got.err != nil {
				t.Fatalf("result = %+v, want %+v", got, want)
			}
			if got.cached {
				shared++
			}
		}
		if shared != 16 {
			t.Fatalf("shared answers = %d, want 16", shared)
		}
		v, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"refactoring"}, before{}, true, "rename this package")
		if v != want || !cached || err != nil || calls.Load() != 1 {
			t.Fatalf("completed cache = %+v, %v, %v; calls = %d", v, cached, err, calls.Load())
		}
	})
}

func TestConcurrentClassificationDistinctKeys(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		results := make(chan classifyResult, 7)
		var calls atomic.Int32
		want := verdict{Intent: "refactoring"}
		ask := func(access.Identity, string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			return want, nil
		}
		for _, input := range []struct {
			model   string
			intents []string
			prev    before
			effort  bool
			text    string
		}{
			{t.Name(), []string{"refactoring"}, before{}, false, "rename"},
			{t.Name() + "/other", []string{"refactoring"}, before{}, false, "rename"},
			{t.Name(), []string{"testing"}, before{}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{Intent: "refactoring"}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{Effort: "high"}, false, "rename"},
			{t.Name(), []string{"refactoring"}, before{}, true, "rename"},
			{t.Name(), []string{"refactoring"}, before{}, false, "test"},
		} {
			go func() {
				v, cached, err := classify(ask, access.Identity{}, input.model, input.intents, input.prev, input.effort, input.text)
				results <- classifyResult{v, cached, err}
			}()
		}
		synctest.Wait()
		if n := calls.Load(); n != 7 {
			t.Errorf("distinct calls = %d, want 7", n)
		}
		close(release)
		synctest.Wait()
		for range 7 {
			if got := <-results; got.v != want || got.cached || got.err != nil {
				t.Fatalf("distinct result = %+v", got)
			}
		}
	})
}

func TestConcurrentClassificationSharesFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		rests bool
	}{
		{"upstream", errors.New("classifier down"), true},
		{"answer", answerError{errors.New("not a number")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetClassified(t)
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				results := make(chan classifyResult, 2)
				var calls atomic.Int32
				want := verdict{Intent: "refactoring"}
				ask := func(access.Identity, string, []string, before, bool, string) (verdict, error) {
					n := calls.Add(1)
					<-release
					if n == 1 {
						return want, tc.err
					}
					return want, nil
				}
				run := func() {
					v, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"refactoring"}, before{}, false, "rename")
					results <- classifyResult{v, cached, err}
				}
				go run()
				synctest.Wait()
				go run()
				synctest.Wait()
				if n := calls.Load(); n != 1 {
					t.Errorf("concurrent calls = %d, want 1", n)
				}
				close(release)
				synctest.Wait()
				for range 2 {
					if got := <-results; got.v != (verdict{}) || got.cached || !errors.Is(got.err, tc.err) {
						t.Fatalf("shared failure = %+v, want %v", got, tc.err)
					}
				}
				if tc.rests {
					_, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"refactoring"}, before{}, false, "rename")
					if cached || err == nil || !strings.Contains(err.Error(), "not asked again") || calls.Load() != 1 {
						t.Fatalf("cooldown = %v, %v; calls = %d", cached, err, calls.Load())
					}
					time.Sleep(classifyRest + time.Second)
				}
				v, cached, err := classify(ask, access.Identity{}, t.Name(), []string{"refactoring"}, before{}, false, "rename")
				if v != want || cached || err != nil || calls.Load() != 2 {
					t.Fatalf("retry = %+v, %v, %v; calls = %d", v, cached, err, calls.Load())
				}
			})
		})
	}
}

func TestConcurrentClassificationPanicReleasesWaiters(t *testing.T) {
	resetClassified(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		panicked := make(chan any, 1)
		result := make(chan classifyResult, 1)
		var calls atomic.Int32
		ask := func(access.Identity, string, []string, before, bool, string) (verdict, error) {
			calls.Add(1)
			<-release
			panic("classifier panic")
		}
		go func() {
			defer func() { panicked <- recover() }()
			classify(ask, access.Identity{}, t.Name(), nil, before{}, false, "rename")
		}()
		synctest.Wait()
		go func() {
			v, cached, err := classify(ask, access.Identity{}, t.Name(), nil, before{}, false, "rename")
			result <- classifyResult{v, cached, err}
		}()
		synctest.Wait()
		close(release)
		synctest.Wait()
		if got := <-panicked; got != "classifier panic" {
			t.Fatalf("panic = %v", got)
		}
		if got := <-result; got.cached || got.err == nil || calls.Load() != 1 {
			t.Fatalf("panic waiter = %+v; calls = %d", got, calls.Load())
		}
		want := verdict{Intent: "refactoring"}
		retry := func(access.Identity, string, []string, before, bool, string) (verdict, error) { return want, nil }
		v, cached, err := classify(retry, access.Identity{}, t.Name(), nil, before{}, false, "rename")
		if v != want || cached || err != nil {
			t.Fatalf("retry after panic = %+v, %v, %v", v, cached, err)
		}
	})
}
