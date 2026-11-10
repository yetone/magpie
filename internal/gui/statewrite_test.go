package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// sinkWriter is a ResponseWriter that keeps what is written, or only
// counts it.
type sinkWriter struct {
	h    http.Header
	keep bool
	body bytes.Buffer
	n    int
}

func (w *sinkWriter) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}
func (w *sinkWriter) WriteHeader(int) {}
func (w *sinkWriter) Write(b []byte) (int, error) {
	w.n += len(b)
	if w.keep {
		w.body.Write(b)
	}
	return len(b), nil
}

// bigState is a state the size real accounts give the Agents page: many
// agents, each with long model pickers.
func bigState() stateJSON {
	var s stateJSON
	for a := range 40 {
		var opts []agent.Option
		for m := range 300 {
			opts = append(opts, agent.Option{
				Value: fmt.Sprintf("provider-%d/model-%d", a, m),
				Label: fmt.Sprintf("Model %d <fast> & cheap", m),
				Note:  "200K context · $3/$15",
				Group: "Provider",
			})
		}
		s.Agents = append(s.Agents, agentJSON{
			ID: fmt.Sprintf("agent-%d", a), Name: "Agent", Path: "/usr/local/bin/agent",
			Fields: []fieldJSON{{Key: "model", Label: "Model", Value: "provider/model-1", Options: opts}},
		})
	}
	s.Profiles = []profileJSON{{Name: "work", Summary: "Work"}}
	s.Catalog = "ok"
	s.Notice = "restart Codex"
	s.Served = 12000
	return s
}

// The state is written as writeJSON wrote it, and the sum it gives is the
// one keepState finds for it, with no agents too.
func TestStateWrittenAsEncoded(t *testing.T) {
	// Big after small and small after big: the held buffers are used again.
	for _, s := range []stateJSON{bigState(), {Agents: []agentJSON{}}, {}, bigState(), {}} {
		w := &sinkWriter{keep: true}
		sum, ok := writeStateJSON(w, s)
		var want bytes.Buffer
		if err := json.NewEncoder(&want).Encode(s); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w.body.Bytes(), want.Bytes()) {
			t.Fatalf("the state was written as\n%.300s\nwant\n%.300s", w.body.Bytes(), want.Bytes())
		}
		kept, err := encodedSum(keptFile(s))
		if err != nil || !ok || sum != kept {
			t.Fatalf("the sum found while writing (%v) is not keepState's", ok)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type %q", ct)
		}
	}
}

// Each poll of the Agents page encoded the state whole twice, to send it
// and for keepState's sum, growing a buffer to its size each time: 68 MB
// of what twenty polls allocated. Each agent is encoded once now, alone.
func TestStateWriteGrowsNoBufferItsSize(t *testing.T) {
	if raceOn {
		t.Skip("-race: sync.Pool drops what it holds, so allocations are not what a build allocates")
	}
	s := bigState()
	w := &sinkWriter{}
	writeStateJSON(w, s) // json's encoders, warmed
	size := w.n
	const runs = 5
	var all uint64
	for range runs {
		// polls are seconds apart, and a GC between them empties the
		// buffers json pools (twice: a pool keeps a GC's victims once)
		runtime.GC()
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		writeStateJSON(&sinkWriter{}, s)
		runtime.ReadMemStats(&m1)
		all += m1.TotalAlloc - m0.TotalAlloc
	}
	per := int(all) / runs
	if per > size/2 {
		t.Fatalf("writing a %d-byte state allocated %d bytes, want under half its size", size, per)
	}
}
