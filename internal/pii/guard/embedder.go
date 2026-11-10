package guard

import (
	"fmt"
	"math"
	"path/filepath"
)

// paraphrase-multilingual-MiniLM-L12-v2 (sentence-transformers, Apache
// 2.0; Xenova's int8 ONNX of it) is the second network bert.go runs: the
// same 12-layer BERT, 384 wide, with XLM-RoBERTa's 250,002 pieces and no
// classifier. Its last hidden state, averaged over the tokens and scaled
// to length 1, is a text's place in a space where texts that say the same
// are near, in any of 50 languages and across them: memory's search by
// meaning. Its weights are int8 with one scale and zero
// point per matrix (AstrLink Guard's have one a column), and its matmuls
// unfused; loadEmbedNet lays them out as bert.go reads them.

const (
	embVocab = 250002 // the tokenizer's pieces
	embRows  = 250037 // the word embeddings' rows (padded past the pieces)
	// EmbedDim is the length of an embedding
	EmbedDim = hidden
)

// EmbedFiles are the files LoadEmbedder reads, in its folder.
var EmbedFiles = []string{"model_int8.onnx", "tokenizer.json"}

// Embedder is paraphrase-multilingual-MiniLM-L12-v2, loaded.
type Embedder struct {
	tok *tokenizer
	net *net
}

// LoadEmbedder reads the model in dir.
func LoadEmbedder(dir string) (*Embedder, error) {
	tok, err := loadTokenizer(filepath.Join(dir, "tokenizer.json"), embVocab)
	if err != nil {
		return nil, fmt.Errorf("tokenizer: %w", err)
	}
	nt, err := loadEmbedNet(filepath.Join(dir, "model_int8.onnx"))
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	return &Embedder{tok: tok, net: nt}, nil
}

// Embed is text's embedding, of EmbedDim numbers and length 1: the same
// for a search and what it searches. A text past maxPos tokens is read to
// there.
func (e *Embedder) Embed(text string) []float32 {
	toks := e.tok.encode(text)
	ids := make([]int, 0, min(len(toks), maxPos-2)+2)
	ids = append(ids, 0)
	for _, t := range toks[:min(len(toks), maxPos-2)] {
		ids = append(ids, t.id)
	}
	ids = append(ids, 2)
	n := len(ids)
	procs := take(Cores())
	defer give(procs)
	x := e.net.hidden(e.net.embed(ids, 0), n, procs)
	out := make([]float32, hidden)
	for t := 0; t < n; t++ {
		for i, v := range x[t*hidden : t*hidden+hidden] {
			out[i] += v
		}
	}
	var sq float64
	for i := range out {
		out[i] /= float32(n)
		sq += float64(out[i]) * float64(out[i])
	}
	if sq > 0 {
		inv := float32(1 / math.Sqrt(sq))
		for i := range out {
			out[i] *= inv
		}
	}
	return out
}

// loadEmbedNet reads the model's int8 ONNX into a net: each
// per-matrix scale and zero point given to every column, and each layer's
// query, key and value matrices side by side as one, as AstrLink Guard's
// fused attention has them.
func loadEmbedNet(path string) (*net, error) {
	ts, err := readONNX(path)
	if err != nil {
		return nil, err
	}
	var missing error
	get := func(name string, dtype int, dims ...int64) *tensor {
		t := ts[name]
		if missing != nil {
			return t
		}
		if t == nil {
			missing = fmt.Errorf("the model has no %s", name)
			return nil
		}
		if t.dtype != dtype || len(t.dims) != len(dims) {
			missing = fmt.Errorf("%s is %d %v", name, t.dtype, t.dims)
			return nil
		}
		for i, d := range dims {
			if t.dims[i] != d {
				missing = fmt.Errorf("%s is %v", name, t.dims)
				return nil
			}
		}
		return t
	}
	fl := func(name string, n int64) []float32 {
		t := get(name, tFloat, n)
		if t == nil {
			return nil
		}
		return t.floats()
	}
	scalar := func(name string) float32 {
		t := get(name, tFloat)
		if t == nil {
			return 0
		}
		return t.floats()[0]
	}
	// an embedding table: uint8, one scale and zero point
	embQ := func(name string, rows int64) ([]uint8, []float32, []int32) {
		q := get(name+".weight_quantized", tUint8, rows, hidden)
		s := scalar(name + ".weight_scale")
		z := get(name+".weight_zero_point", tUint8)
		if missing != nil {
			return nil, nil, nil
		}
		sc, zp := make([]float32, hidden), make([]int32, hidden)
		for c := range sc {
			sc[c], zp[c] = s, int32(z.raw[0])
		}
		// raw is a slice of the file read whole; the table is most of it
		return q.raw, sc, zp
	}
	emb := func(name string, rows int64) []float32 {
		q, sc, zp := embQ(name, rows)
		if q == nil {
			return nil
		}
		out := make([]float32, len(q))
		for i, b := range q {
			c := i % hidden
			out[i] = float32(int32(b)-zp[c]) * sc[c]
		}
		return out
	}
	// mats are matrices [k, n_i] as one [k, Σn_i], by column
	mats := func(k int64, names ...string) qmat {
		var parts []*tensor
		var scales []float32
		var zps []int32
		n := 0
		for _, name := range names {
			q := ts[name+"_quantized"]
			if q == nil || len(q.dims) != 2 {
				get(name+"_quantized", tInt8, k, -1)
				return qmat{}
			}
			q = get(name+"_quantized", tInt8, k, q.dims[1])
			s := scalar(name + "_scale")
			z := get(name+"_zero_point", tInt8)
			if missing != nil {
				return qmat{}
			}
			parts = append(parts, q)
			for range q.dims[1] {
				scales = append(scales, s)
				zps = append(zps, int32(int8(z.raw[0])))
			}
			n += int(q.dims[1])
		}
		m := qmat{k: int(k), n: n, w: make([]int8, int(k)*n), scale: scales, zp: zps, sum: make([]int32, n)}
		c := 0
		for _, q := range parts {
			pn := int(q.dims[1])
			for pc := 0; pc < pn; pc, c = pc+1, c+1 {
				for r := 0; r < m.k; r++ {
					v := int8(q.raw[r*pn+pc])
					m.w[c*m.k+r] = v
					m.sum[c] += int32(v)
				}
			}
		}
		return m
	}
	nt := &net{}
	nt.word, nt.wordScale, nt.wordZP = embQ("embeddings.word_embeddings", embRows)
	nt.pos = emb("embeddings.position_embeddings", maxPos)
	nt.typ = emb("embeddings.token_type_embeddings", 2)
	if t := get("embeddings.position_ids", tInt64, 1, maxPos); t != nil {
		nt.posIDs = t.int64s()
	}
	nt.embLNw = fl("embeddings.LayerNorm.weight", hidden)
	nt.embLNb = fl("embeddings.LayerNorm.bias", hidden)
	ones := make([]float32, inter)
	for i := range ones {
		ones[i] = 1
	}
	for i := range nt.layers {
		l := &nt.layers[i]
		p := fmt.Sprintf("encoder.layer.%d.", i)
		// the exporter's names for the layer's six matmuls: query, key,
		// value, the attention's output, up and down
		b := 1525 + 13*i
		name := func(off int) string { return fmt.Sprintf("onnx::MatMul_%d", b+off) }
		l.qkv = mats(hidden, name(0), name(1), name(4))
		l.qkvBias = append(append(fl(p+"attention.self.query.bias", hidden), fl(p+"attention.self.key.bias", hidden)...), fl(p+"attention.self.value.bias", hidden)...)
		l.attnOut = mats(hidden, name(10))
		l.attnBias = fl(p+"attention.output.dense.bias", hidden)
		l.ln1w = fl(p+"attention.output.LayerNorm.weight", hidden)
		l.ln1b = fl(p+"attention.output.LayerNorm.bias", hidden)
		l.up = mats(hidden, name(11))
		l.upBias = fl(p+"intermediate.dense.bias", inter)
		l.smooth = ones
		l.down = mats(inter, name(12))
		l.downBias = fl(p+"output.dense.bias", hidden)
		l.ln2w = fl(p+"output.LayerNorm.weight", hidden)
		l.ln2b = fl(p+"output.LayerNorm.bias", hidden)
		if missing == nil && (l.qkv.n != 3*hidden || l.attnOut.n != hidden || l.up.n != inter || l.down.n != hidden) {
			missing = fmt.Errorf("layer %d's matmuls are %d %d %d %d wide", i, l.qkv.n, l.attnOut.n, l.up.n, l.down.n)
		}
	}
	if missing != nil {
		return nil, missing
	}
	return nt, nil
}
