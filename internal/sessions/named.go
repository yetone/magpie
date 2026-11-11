package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"unique"
	"unsafe"

	"github.com/tidwall/gjson"
)

// named holds a few values by name, in the names' order, and is written as
// the JSON object a map is. A file's parse has a model or two, a day or two
// and a few tools, and Go's map of one is a group of eight: ~500 bytes for
// one model's Tokens, where this holds 56. A long history's 15.9k kept
// parses held ~18MB of such maps.
type named[V any] []entry[V]

type entry[V any] struct {
	name string
	v    V
}

func (n named[V]) find(name string) (int, bool) {
	return slices.BinarySearchFunc(n, name, func(e entry[V], name string) int { return strings.Compare(e.name, name) })
}

// get is name's value, and whether there is one.
func (n named[V]) get(name string) (V, bool) {
	if i, ok := n.find(name); ok {
		return n[i].v, true
	}
	var zero V
	return zero, false
}

// of is name's value, or the zero value when there is none.
func (n named[V]) of(name string) V {
	v, _ := n.get(name)
	return v
}

// at is where name's value is held, a zero one put in when there is none.
// It holds until the next name is put in.
func (n *named[V]) at(name string) *V {
	i, ok := n.find(name)
	if !ok {
		*n = slices.Insert(*n, i, entry[V]{name: name})
	}
	return &(*n)[i].v
}

// del takes name out, leaving nil when nothing is left, as a map's delete
// and the nil the code set after it did.
func (n *named[V]) del(name string) {
	if i, ok := n.find(name); ok {
		*n = slices.Delete(*n, i, i+1)
	}
	if len(*n) == 0 {
		*n = nil
	}
}

// all is each name and its value, in the names' order.
func (n named[V]) all() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for _, e := range n {
			if !yield(e.name, e.v) {
				return
			}
		}
	}
}

// MarshalJSON writes n as json.Marshal writes a map: keys in order.
func (n named[V]) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(e.name)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(e.v)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

var errNotObject = errors.New("sessions: not a JSON object")

// UnmarshalJSON reads an object as json.Unmarshal reads it into a map: a
// name given twice is its last. b is the cache file's own bytes, which are
// not kept: names are held once (unique) for all the parses that use them.
func (n *named[V]) UnmarshalJSON(b []byte) error {
	// read in place: ParseBytes copied every object whole (8.5 MB of a
	// real history's start), and nothing read here is kept but through
	// unique and Unmarshal, which copy
	r := gjson.Parse(unsafe.String(unsafe.SliceData(b), len(b)))
	if r.Type == gjson.Null {
		*n = nil
		return nil
	}
	if !r.IsObject() {
		return errNotObject
	}
	size := 0
	r.ForEach(func(_, _ gjson.Result) bool {
		size++
		return true
	})
	out := make(named[V], 0, size)
	var err error
	r.ForEach(func(k, v gjson.Result) bool {
		var val V
		if !quick(&val, v) {
			if err = json.Unmarshal(unsafe.Slice(unsafe.StringData(v.Raw), len(v.Raw)), &val); err != nil {
				return false
			}
		}
		*out.at(unique.Make(k.String()).Value()) = val
		return true
	})
	if err != nil {
		return err
	}
	*n = out
	return nil
}

// quick reads v into an int or Tokens without reflection when every number
// in it is a plain integer, as a cache magpie wrote has them; anything else
// is left to json.Unmarshal, and its errors.
func quick(val any, v gjson.Result) bool {
	switch p := val.(type) {
	case *int:
		n, ok := integer(v.Raw)
		*p = n
		return ok
	case *Tokens:
		if !v.IsObject() {
			return false
		}
		ok := true
		v.ForEach(func(k, f gjson.Result) bool {
			var at *int
			switch k.Raw {
			case `"input"`:
				at = &p.Input
			case `"output"`:
				at = &p.Output
			case `"cache_read"`:
				at = &p.CacheRead
			case `"cache_write"`:
				at = &p.CacheWrite
			case `"cache_write_1h"`:
				at = &p.CacheWrite1h
			default:
				ok = false
				return false
			}
			*at, ok = integer(f.Raw)
			return ok
		})
		if !ok {
			*p = Tokens{}
		}
		return ok
	}
	return false
}

// integer is s as an int when it is one written plainly: an optional minus
// and up to 18 digits, no leading zero.
func integer(s string) (int, bool) {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	if s == "" || len(s) > 18 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n := 0
	for i := range len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}
