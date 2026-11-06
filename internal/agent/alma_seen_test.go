package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A look at Alma's providers decodes an answer once: the Agents page's
// model field, its check and its options each decoded all of Alma's
// providers again from the answer kept 5s. A new answer (Alma asked again
// after a change) is decoded anew.
func TestAlmaProvidersSeenDecodeOnce(t *testing.T) {
	var mu sync.Mutex
	gets, model := 0, "gpt-5.4"
	asked := func() int { mu.Lock(); defer mu.Unlock(); return gets }
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			return
		}
		mu.Lock()
		gets++
		m := model
		mu.Unlock()
		var b strings.Builder
		b.WriteString(`[{"id":"mg","name":"magpie","type":"openai","baseURL":"http://x/v1","enabled":true,"models":["` + m + `"],"availableModels":[`)
		for i := range 400 {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"m-%d","name":"Model %d","capabilityOverrides":{"vision":true,"functionCalling":true}}`, i, i)
		}
		b.WriteString(`]}]`)
		rw.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldFor := almaAPI, almaReadFor
	almaAPI, almaReadFor = srv.URL, time.Minute
	t.Cleanup(func() { almaAPI, almaReadFor = oldAPI, oldFor })

	ps, err := almaProvidersSeen()
	if err != nil || len(ps) != 1 || ps[0].Name != "magpie" || len(ps[0].Known) != 400 || ps[0].Models[0] != "gpt-5.4" {
		t.Fatalf("first look: %v %+v", err, ps)
	}
	if n := testing.AllocsPerRun(20, func() {
		if ps, err := almaProvidersSeen(); err != nil || len(ps) != 1 {
			t.Fatal(err, ps)
		}
	}); n > 10 {
		t.Errorf("a look at the answer kept allocates %v times", n)
	}
	if g := asked(); g != 1 {
		t.Fatalf("looks asked Alma %d times", g)
	}
	// the same answer asked for again, past the time one is kept, is not
	// decoded again either
	almaReadFor = 0
	if n := testing.AllocsPerRun(5, func() {
		if ps, err := almaProvidersSeen(); err != nil || len(ps) != 1 {
			t.Fatal(err, ps)
		}
	}); n > 400 {
		t.Errorf("a look at the same answer asked again allocates %v times", n)
	}
	if g := asked(); g < 6 {
		t.Fatalf("Alma asked %d times with nothing kept", g)
	}
	almaReadFor = time.Minute
	mu.Lock()
	gets = 1
	mu.Unlock()
	mu.Lock()
	model = "gpt-5.5"
	mu.Unlock()
	if err := almaDo("PUT", "/api/providers/mg", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if ps, err := almaProvidersSeen(); err != nil || ps[0].Models[0] != "gpt-5.5" || asked() != 2 {
		t.Fatalf("after a change: %v, %v, %d asks", err, ps, asked())
	}
	// one that can't be read is an error, not the last good one
	almaAPI = "http://127.0.0.1:1"
	if _, err := almaProvidersSeen(); err == nil {
		t.Error("Alma gone: no error")
	}
}
