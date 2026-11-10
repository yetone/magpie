package gui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
)

// stateHeld is what writeStateJSON writes through, used again from poll
// to poll: two buffers for the state without its agents (which
// json.Marshal copied out whole each time) and the 32KB writer. One set
// is kept, not a sync.Pool's: polls are seconds apart and a GC between
// them would empty a pool. A poll that finds it taken makes its own.
var stateHeld atomic.Pointer[stateBufs]

type stateBufs struct {
	tail, kept bytes.Buffer
	out        *bufio.Writer
}

// encodeTo is json.Marshal(v) into b, without its copy of the result:
// Encode escapes as Marshal does, and its newline is cut.
func encodeTo(b *bytes.Buffer, v any) ([]byte, error) {
	b.Reset()
	if err := json.NewEncoder(b).Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

// stateBufMax is the largest buffer kept for the next poll; one grown
// past it by an odd state goes.
const stateBufMax = 4 << 20

// agentsNull is how a stateJSON with no agents begins: Agents is its
// first field.
const agentsNull = `{"agents":null`

// writeStateJSON writes s to rw as writeJSON does, and returns keepState's
// sum of it (encodedSum(keptFile(s))), encoding each agent once, for both.
// Encoding the state whole grew a buffer to its size, twice a poll: once
// to send it and once for the sum, 68 MB of what twenty polls of the
// Agents page allocated with real accounts. Each agent is encoded alone
// now, straight to both, and only the rest of the state, without its
// agents, is encoded twice (with the settings and without). false when
// the sum wasn't found; s was written all the same, when it could be.
func writeStateJSON(rw http.ResponseWriter, s stateJSON) ([32]byte, bool) {
	agents := s.Agents
	f := keptFile(s)
	s.Agents, f.State.Agents = nil, nil
	bufs := stateHeld.Swap(nil)
	if bufs == nil {
		bufs = new(stateBufs)
	}
	defer func() {
		if bufs.tail.Cap() <= stateBufMax && bufs.kept.Cap() <= stateBufMax {
			if bufs.out != nil {
				bufs.out.Reset(nil)
			}
			stateHeld.CompareAndSwap(nil, bufs)
		}
	}()
	tail, err := encodeTo(&bufs.tail, s)
	keptTail, kerr := encodeTo(&bufs.kept, f)
	v, _ := json.Marshal(Version)
	keptHead := `{"version":` + string(v) + `,"state":` + agentsNull
	if agents == nil || err != nil || kerr != nil ||
		!bytes.HasPrefix(tail, []byte(agentsNull)) || !bytes.HasPrefix(keptTail, []byte(keptHead)) {
		s.Agents = agents
		writeJSON(rw, s)
		sum, err := encodedSum(keptFile(s))
		return sum, err == nil
	}
	rw.Header().Set("Content-Type", "application/json")
	if bufs.out == nil {
		bufs.out = bufio.NewWriterSize(rw, 32<<10)
	} else {
		bufs.out.Reset(rw)
	}
	out := bufs.out
	defer out.Flush()
	h := sha256.New()
	_, _ = out.WriteString(`{"agents":[`)
	h.Write([]byte(keptHead[:len(keptHead)-len("null")] + "["))
	// each agent goes straight to rw and the sum, as Encode writes it
	enc := json.NewEncoder(lineless{io.MultiWriter(out, h)})
	for i := range agents {
		if i > 0 {
			_ = out.WriteByte(',')
			h.Write([]byte{','})
		}
		// a pointer, so a method on *T is used as it is in the slice
		if enc.Encode(&agents[i]) != nil {
			return [32]byte{}, false
		}
	}
	_ = out.WriteByte(']')
	_, _ = out.Write(tail[len(agentsNull):])
	_ = out.WriteByte('\n')
	h.Write([]byte{']'})
	h.Write(keptTail[len(keptHead):])
	h.Write([]byte{'\n'})
	return [32]byte(h.Sum(nil)), true
}

// lineless writes what a json.Encoder writes without the newline Encode
// ends each value with: Encode writes a value and its newline at once.
type lineless struct{ w io.Writer }

func (l lineless) Write(b []byte) (int, error) {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		_, err := l.w.Write(b[:n-1])
		return n, err
	}
	return l.w.Write(b)
}
