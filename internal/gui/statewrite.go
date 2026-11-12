package gui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/yetone/magpie/internal/agent"
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
// s goes as shareOptions has it, each option list once.
func writeStateJSON(rw http.ResponseWriter, s stateJSON) ([32]byte, bool) {
	s = shareOptions(s)
	agents, lists, opts := s.Agents, s.OptionLists, s.Options
	f := keptFile(s)
	s.Agents, f.State.Agents = nil, nil
	s.OptionLists, f.State.OptionLists = nil, nil
	s.Options, f.State.Options = nil, nil
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
		!bytes.HasPrefix(tail, []byte(agentsNull)) || !bytes.HasPrefix(keptTail, []byte(keptHead)) ||
		!bytes.HasSuffix(tail, []byte("}")) || !bytes.HasSuffix(keptTail, []byte("}}")) {
		s.Agents, s.OptionLists, s.Options = agents, lists, opts
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
	both := io.MultiWriter(out, h)
	_, _ = out.WriteString(`{"agents":[`)
	h.Write([]byte(keptHead[:len(keptHead)-len("null")] + "["))
	// each agent goes straight to rw and the sum, as Encode writes it
	enc := json.NewEncoder(lineless{both})
	for i := range agents {
		if i > 0 {
			_, _ = both.Write([]byte{','})
		}
		// a pointer, so a method on *T is used as it is in the slice
		if enc.Encode(&agents[i]) != nil {
			return [32]byte{}, false
		}
	}
	_, _ = both.Write([]byte{']'})
	// the rest, up to the state's closing brace: the option lists and
	// options, the state's last fields, go after it, each option alone as
	// each agent is
	_, _ = out.Write(tail[len(agentsNull) : len(tail)-1])
	h.Write(keptTail[len(keptHead) : len(keptTail)-2])
	if len(lists) > 0 {
		// the lists' indexes as Encode writes them, through one small
		// buffer; then each option alone
		_, _ = both.Write([]byte(`,"optionLists":[`))
		nb := make([]byte, 0, 512)
		for i, l := range lists {
			if i > 0 {
				nb = append(nb, ',')
			}
			nb = append(nb, '[')
			for j, k := range l {
				if j > 0 {
					nb = append(nb, ',')
				}
				nb = strconv.AppendInt(nb, int64(k), 10)
				if len(nb) > 480 {
					_, _ = both.Write(nb)
					nb = nb[:0]
				}
			}
			nb = append(nb, ']')
		}
		_, _ = both.Write(nb)
		_, _ = both.Write([]byte(`],"options":[`))
		for i := range opts {
			if i > 0 {
				_, _ = both.Write([]byte{','})
			}
			if enc.Encode(opts[i]) != nil {
				return [32]byte{}, false
			}
		}
		_, _ = both.Write([]byte{']'})
	}
	_, _ = out.WriteString("}\n")
	h.Write([]byte("}}\n"))
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

// shareOptions is s with each fields' option list sent once, in
// OptionLists, and named by the field's List: a list two fields offer
// alike is one entry, a FastFor naming the field's own agent sent as
// fastSelf. A list is of indexes into Options, where an option that
// several lists offer is once. s's own agents are left as they were. The
// page puts each field's list back as it reads the state (api in app.js).
//
// A state with real accounts has ~1,700 options in its lists, ~260 of
// them unlike the rest. They are told apart by sorting them by Value, not
// in a map, and each list is a run of one slice of indexes: what this
// allocates is a few bytes an option, not a map entry and a list each.
func shareOptions(s stateJSON) stateJSON {
	if s.OptionLists != nil {
		return s
	}
	n := 0
	for i := range s.Agents {
		for _, f := range s.Agents[i].Fields {
			if len(f.Options) >= 2 {
				n += len(f.Options)
			}
		}
	}
	if n == 0 {
		return s
	}
	// every listed option, in field order, as it is sent
	all := make([]*agent.Option, 0, n)
	agents := slices.Clone(s.Agents)
	for i := range agents {
		fields := slices.Clone(agents[i].Fields)
		for j := range fields {
			if opts := fields[j].Options; len(opts) >= 2 {
				opts = fastForSelf(opts, agents[i].ID)
				for k := range opts {
					all = append(all, &opts[k])
				}
			}
		}
		agents[i].Fields = fields
	}
	// each option's index in the table: alike options sort together
	order := make([]int32, n)
	for i := range order {
		order[i] = int32(i)
	}
	slices.SortStableFunc(order, func(a, b int32) int { return strings.Compare(all[a].Value, all[b].Value) })
	ids := make([]int32, n)
	var table []*agent.Option
	for g := 0; g < n; {
		e := g + 1
		for e < n && all[order[e]].Value == all[order[g]].Value {
			e++
		}
		first := int32(len(table))
		for _, k := range order[g:e] {
			ids[k] = -1
			for t := first; t < int32(len(table)); t++ {
				if sameOption(table[t], all[k]) {
					ids[k] = t
					break
				}
			}
			if ids[k] < 0 {
				ids[k] = int32(len(table))
				table = append(table, all[k])
			}
		}
		g = e
	}
	var lists [][]int32
	at := 0
	for i := range agents {
		for j := range agents[i].Fields {
			f := &agents[i].Fields[j]
			if len(f.Options) < 2 {
				continue
			}
			l := ids[at : at+len(f.Options) : at+len(f.Options)]
			at += len(f.Options)
			k := slices.IndexFunc(lists, func(m []int32) bool { return slices.Equal(m, l) })
			if k < 0 {
				lists, k = append(lists, l), len(lists)
			}
			f.Options, f.List = nil, k+1
		}
	}
	s.Agents, s.OptionLists, s.Options = agents, lists, table
	return s
}

// fastSelf is the FastFor an option list is sent with for the agent
// whose list it is: each agent's catalog differed only there, so none
// were alike. The page puts the agent's id back (api in app.js).
const fastSelf = "*"

// fastForSelf is opts with each FastFor that names id as fastSelf; opts
// itself when none does, a copy otherwise.
func fastForSelf(opts []agent.Option, id string) []agent.Option {
	copied := false
	for k := range opts {
		if opts[k].FastFor != id || id == "" {
			continue
		}
		if !copied {
			opts, copied = slices.Clone(opts), true
		}
		opts[k].FastFor = fastSelf
	}
	return opts
}

// sameOption reports whether a and b are the same option.
func sameOption(a, b *agent.Option) bool {
	if a.Label != b.Label || a.Ref != b.Ref || a.FastFor != b.FastFor || a.Note != b.Note {
		return false
	}
	return reflect.DeepEqual(a, b)
}
