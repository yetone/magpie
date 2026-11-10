package memembed

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/pii/guard"
)

// vectors.bin, in the model's folder: "MEV1", the vectors' length as a
// little-endian uint32, then one record a text read: its sha256 and its
// vector, little-endian float32s. A file that can't be read whole is read
// as none: the memories are read again, which is all it costs.

var magic = []byte("MEV1")

type vecs struct {
	mu   sync.Mutex
	path string
	m    map[[32]byte][]float32
}

var store struct {
	mu sync.Mutex
	v  *vecs
}

// vectors is what was read, from vectors.bin on the first call.
func vectors() *vecs {
	store.mu.Lock()
	defer store.mu.Unlock()
	path := filepath.Join(dir(), "vectors.bin")
	if store.v == nil || store.v.path != path {
		store.v = &vecs{path: path, m: readVecs(path)}
	}
	return store.v
}

func readVecs(path string) map[[32]byte][]float32 {
	m := map[[32]byte][]float32{}
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 8 || !bytes.Equal(b[:4], magic) || binary.LittleEndian.Uint32(b[4:8]) != guard.EmbedDim {
		return m
	}
	rec := 32 + 4*guard.EmbedDim
	b = b[8:]
	if len(b)%rec != 0 {
		return map[[32]byte][]float32{}
	}
	for ; len(b) > 0; b = b[rec:] {
		var k [32]byte
		copy(k[:], b[:32])
		v := make([]float32, guard.EmbedDim)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[32+4*i:]))
		}
		m[k] = v
	}
	return m
}

func (vs *vecs) get(k [32]byte) ([]float32, bool) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	v, ok := vs.m[k]
	return v, ok
}

func (vs *vecs) put(k [32]byte, v []float32) {
	vs.mu.Lock()
	vs.m[k] = v
	vs.mu.Unlock()
}

// prune drops the vectors of texts not in keep, and says whether it did.
func (vs *vecs) prune(keep map[[32]byte]bool) bool {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	gone := false
	for k := range vs.m {
		if !keep[k] {
			delete(vs.m, k)
			gone = true
		}
	}
	return gone
}

// save writes them through a temp file renamed over the last.
func (vs *vecs) save() error {
	vs.mu.Lock()
	var b bytes.Buffer
	b.Write(magic)
	b.Write(binary.LittleEndian.AppendUint32(nil, guard.EmbedDim))
	for k, v := range vs.m {
		b.Write(k[:])
		for _, f := range v {
			b.Write(binary.LittleEndian.AppendUint32(nil, math.Float32bits(f)))
		}
	}
	vs.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(vs.path), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(vs.path, b.Bytes())
}
