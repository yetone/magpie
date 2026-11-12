package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"slices"
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
		if err := json.NewEncoder(&want).Encode(shareOptions(s)); err != nil {
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

// Each field's option list goes once: the fields offering one list alike
// name it, and what the page puts back (api in app.js) is the state as
// it was. With real accounts 13 agents' model pickers offered the same
// 51KB catalog, 650KB of a 690KB state read.
func TestStateSendsEachOptionListOnce(t *testing.T) {
	catalog := []agent.Option{{Value: "a/1", Label: "One", Icons: []string{"x"}}, {Value: "a/2", Note: "fast"}}
	same := slices.Clone(catalog) // equal, not the same backing array
	other := slices.Clone(catalog)
	other[1].FastFor = "codex"
	icons := slices.Clone(catalog)
	icons[0].Icons = []string{"y"}
	s := stateJSON{Agents: []agentJSON{
		{ID: "a", Fields: []fieldJSON{{Key: "model", Options: catalog}, {Key: "effort", Options: []agent.Option{{Value: "low"}}}}},
		{ID: "b", Fields: []fieldJSON{{Key: "model", Options: same}, {Key: "small", Options: catalog}}},
		{ID: "c", Fields: []fieldJSON{{Key: "model", Options: other}}},
		{ID: "d", Fields: []fieldJSON{{Key: "model", Options: icons}}},
		{ID: "e", Fields: []fieldJSON{{Key: "model", Options: []agent.Option{}}}},
	}}
	var before bytes.Buffer
	if err := json.NewEncoder(&before).Encode(s); err != nil {
		t.Fatal(err)
	}
	w := &sinkWriter{keep: true}
	writeStateJSON(w, s)
	var sent struct {
		Agents []struct {
			ID     string `json:"id"`
			Fields []struct {
				Key     string         `json:"key"`
				Options []agent.Option `json:"options"`
				List    int            `json:"list"`
			} `json:"fields"`
		} `json:"agents"`
		OptionLists [][]agent.Option `json:"optionLists"`
	}
	if err := json.Unmarshal(w.body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.OptionLists) != 3 {
		t.Fatalf("%d option lists sent, want 3 (the catalog, the fast one's, the other icons'): %s", len(sent.OptionLists), w.body.Bytes())
	}
	lists := map[string]int{}
	for _, a := range sent.Agents {
		for _, f := range a.Fields {
			if f.List != 0 {
				if f.Options != nil {
					t.Fatalf("%s's %s sent its options and a list", a.ID, f.Key)
				}
				lists[a.ID+"/"+f.Key] = f.List
			}
		}
	}
	if lists["a/model"] == 0 || lists["a/model"] != lists["b/model"] || lists["a/model"] != lists["b/small"] {
		t.Fatalf("the fields offering one list name %v", lists)
	}
	if lists["c/model"] == lists["a/model"] || lists["d/model"] == lists["a/model"] {
		t.Fatalf("a list unlike the catalog was sent as it: %v", lists)
	}
	if lists["a/effort"] != 0 || lists["e/model"] != 0 {
		t.Fatalf("a list of one or none was moved: %v", lists)
	}
	// put back as the page does, it is the state as it was
	var back map[string]any
	if err := json.Unmarshal(w.body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	ol := back["optionLists"].([]any)
	for _, a := range back["agents"].([]any) {
		for _, f := range a.(map[string]any)["fields"].([]any) {
			f := f.(map[string]any)
			if l, ok := f["list"].(float64); ok {
				f["options"] = ol[int(l)-1]
				delete(f, "list")
			}
		}
	}
	delete(back, "optionLists")
	var want map[string]any
	if err := json.Unmarshal(before.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(back)
	b2, _ := json.Marshal(want)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("put back, the state is\n%s\nwant\n%s", b1, b2)
	}
	// and the state given is as it was
	var after bytes.Buffer
	_ = json.NewEncoder(&after).Encode(s)
	if !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("writing the state changed the caller's agents")
	}
}
