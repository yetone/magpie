package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"
	"unsafe"

	"github.com/yetone/magpie/internal/provider"
)

// keepTurn runs one Responses request through previousResponse, answered
// with output, and returns the turn kept under id.
func keepTurn(t *testing.T, body, id, output string) *prevTurn {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	w, r, _, done := previousResponse(httptest.NewRecorder(), r, provider.Responses, []byte(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	io.WriteString(w, `{"id":"`+id+`","object":"response","status":"completed","output":[`+output+`]}`)
	done()
	k, ok := prevResponses.get(prevKey{prevScope(r), id})
	if !ok {
		t.Fatalf("%s not kept", id)
	}
	return k
}

// TestResentHistorySharesKeptBytes: a client that sends its whole
// conversation each turn, naming no earlier response, has the history a
// kept turn holds shared, not copied again, and counted once for as long
// as any turn holding it is kept.
func TestResentHistorySharesKeptBytes(t *testing.T) {
	old := prevResponses
	prevResponses = newPrevStore(prevCap, prevTTL)
	t.Cleanup(func() { prevResponses = old })
	u1 := `{"type":"message","role":"user","content":"` + strings.Repeat("long question ", 2000) + `"}`
	a1 := `{"type":"message","role":"assistant","content":"answer one"}`
	u2 := `{"type":"message","role":"user","content":"and then?"}`
	first := keepTurn(t, `{"model":"m","input":[`+u1+`]}`, "resp_1", a1)
	second := keepTurn(t, `{"model":"m","input":[`+u1+`,`+a1+`,`+u2+`]}`, "resp_2", `{"type":"message","role":"assistant","content":"two"}`)
	if got, _ := json.Marshal(second.items); !strings.HasPrefix(string(got), `[`+u1+`,`+a1+`,`+u2+`,`) {
		t.Fatalf("kept %.200s", got)
	}
	for i := range 2 {
		if unsafe.SliceData(second.items[i]) != unsafe.SliceData(first.items[i]) {
			t.Fatalf("item %d copied again", i)
		}
	}
	if want := len(u2) + len(`{"type":"message","role":"assistant","content":"two"}`); second.own != want {
		t.Fatalf("own %d, want the %d bytes the turn added", second.own, want)
	}
	both := first.own + second.own
	if prevResponses.size != both {
		t.Fatalf("size %d, want %d", prevResponses.size, both)
	}
	prevResponses.mu.Lock()
	prevResponses.drop(prevResponses.m[first.key])
	prevResponses.mu.Unlock()
	// the second still holds u1 and a1: they stay counted
	if prevResponses.size != both {
		t.Fatalf("size %d once the first went, want %d: what the second holds is uncounted", prevResponses.size, both)
	}
	prevResponses.mu.Lock()
	prevResponses.drop(prevResponses.m[second.key])
	prevResponses.mu.Unlock()
	if prevResponses.size != 0 {
		t.Fatalf("size %d with nothing kept", prevResponses.size)
	}
}

// TestPrevStoreCountsHeldBytes: over random turns sharing, following and
// pushing each other out, the store's size is always the bytes of the
// chunks its turns hold, each once, and every turn its own input and
// output.
func TestPrevStoreCountsHeldBytes(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	st := newPrevStore(4000, prevTTL)
	var convs [][]json.RawMessage
	var ids, scopes []string
	shared := 0
	for i := range 3000 {
		var fresh []json.RawMessage
		var prior *prevTurn
		// mostly one of the newest conversations goes on, as clients do
		c := len(convs)
		if n := len(convs); n > 0 && r.IntN(8) != 0 {
			c = n - 1 - r.IntN(min(n, 3))
		}
		if c == len(convs) {
			scopes = append(scopes, fmt.Sprint("s", r.IntN(2)))
		}
		scope := scopes[c]
		switch {
		case c < len(convs) && r.IntN(4) == 0 && ids[c] != "":
			// names the earlier turn
			if k, ok := st.get(prevKey{scope, ids[c]}); ok {
				prior = k
			}
			fresh = []json.RawMessage{json.RawMessage(fmt.Sprintf(`"n%d"`, i))}
		case c < len(convs):
			// sends it whole again, and something new
			fresh = append(convs[c][:len(convs[c]):len(convs[c])], json.RawMessage(fmt.Sprintf(`"m%d%s"`, i, strings.Repeat("x", r.IntN(200)))))
		default:
			fresh = []json.RawMessage{json.RawMessage(fmt.Sprintf(`"f%d"`, i))}
			convs, ids = append(convs, nil), append(ids, "")
		}
		k := st.kept(scope, prior, fresh)
		if k.own < len(fresh[len(fresh)-1]) {
			t.Fatalf("own %d under the new item", k.own)
		}
		total := 0
		for _, it := range fresh {
			total += len(it)
		}
		if prior == nil && k.own < total {
			shared++
		}
		out := []json.RawMessage{json.RawMessage(fmt.Sprintf(`"o%d"`, i))}
		id := fmt.Sprint("r", i)
		turn := &prevTurn{key: prevKey{scope, id}, items: k.items, own: k.own, chunks: k.chunks, ends: k.ends}
		turn.add(out)
		want := fresh
		if prior != nil {
			want = append(prior.items[:len(prior.items):len(prior.items)], fresh...)
		}
		want = append(want[:len(want):len(want)], out...)
		if g, w := fmt.Sprint(turn.items), fmt.Sprint(want); g != w {
			t.Fatalf("turn %d holds %.200s, want %.200s", i, g, w)
		}
		if len(turn.ends) != len(turn.chunks) || turn.ends[len(turn.ends)-1] != len(turn.items) {
			t.Fatalf("turn %d: ends %v over %d items", i, turn.ends, len(turn.items))
		}
		st.put(turn)
		convs[c], ids[c] = want, id
		// the size is the distinct chunks held
		held := map[*prevChunk]bool{}
		sum := 0
		for e := st.order.Front(); e != nil; e = e.Next() {
			for _, ch := range e.Value.(*prevTurn).chunks {
				if !held[ch] {
					held[ch] = true
					sum += ch.size
				}
				if ch.refs <= 0 {
					t.Fatalf("a held chunk has refs %d", ch.refs)
				}
			}
		}
		if st.size != sum || st.size > st.cap {
			t.Fatalf("after turn %d: size %d, chunks held %d, cap %d", i, st.size, sum, st.cap)
		}
	}
	if shared < 500 {
		t.Fatalf("only %d turns shared: the test reaches too little", shared)
	}
}
