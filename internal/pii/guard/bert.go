package guard

import (
	"fmt"
	"math"
	"slices"
	"sync"
)

// The network: a 12-layer BERT (Multilingual-MiniLM, 384 wide, 12
// heads) with a token classifier, as the publisher quantized it to int8
// for ONNX Runtime. Its weights are int8 (per column, with a scale and a
// zero point), and each matmul's input is quantized as it comes, to uint8
// over its own range (DynamicQuantizeLinear). This does what ONNX Runtime
// does with the same weights: the integer sums exactly, the rest in
// float32, so what it labels is what the publisher's int8 model labels.

const (
	hidden = 384
	heads  = 12
	inter  = 1536
	layers = 12
	maxPos = 512
)

// qmat is a quantized weight [k, n], kept by column (w[c*k : c*k+k]) so
// that each output is one contiguous dot product.
type qmat struct {
	k, n  int
	w     []int8
	scale []float32
	zp    []int32
	sum   []int32 // each column's sum of w
}

type layer struct {
	qkv        qmat
	qkvBias    []float32
	attnOut    qmat
	attnBias   []float32
	ln1w, ln1b []float32
	up         qmat
	upBias     []float32
	smooth     []float32
	down       qmat
	downBias   []float32
	ln2w, ln2b []float32
}

type net struct {
	// the word embeddings as the model has them, [vocabSize, hidden]
	// uint8 with a scale and zero point per column (15 MB, where
	// dequantized they would be 61), and the position and type ones
	// dequantized
	word           []uint8
	wordScale      []float32
	wordZP         []int32
	pos, typ       []float32
	posIDs         []int64
	embLNw, embLNb []float32
	layers         [layers]layer
	cls            qmat
	clsBias        []float32
	labels         int
}

func loadNet(path string) (*net, error) {
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
			if d >= 0 && t.dims[i] != d {
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
	embQ := func(name string, rows int64) ([]uint8, []float32, []int32) {
		q := get(name+".weight_quantized", tUint8, rows, hidden)
		s := get(name+".weight_scale", tFloat, hidden)
		z := get(name+".weight_zero_point", tUint8, hidden)
		if missing != nil {
			return nil, nil, nil
		}
		zp := make([]int32, hidden)
		for c := range zp {
			zp[c] = int32(z.raw[c])
		}
		// a copy: raw is a slice of the whole data file, which is let go
		return slices.Clone(q.raw), s.floats(), zp
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
	mat := func(prefix string, k, n int64) qmat {
		q := get(prefix+"_quantized", tInt8, k, n)
		if missing != nil {
			return qmat{}
		}
		n = q.dims[1]
		s := get(prefix+"_scale", tFloat, n)
		z := get(prefix+"_zero_point", tInt8, n)
		if missing != nil {
			return qmat{}
		}
		m := qmat{k: int(k), n: int(n), w: make([]int8, k*n), scale: s.floats(), zp: make([]int32, n), sum: make([]int32, n)}
		for c := 0; c < m.n; c++ {
			m.zp[c] = int32(int8(z.raw[c]))
			for r := 0; r < m.k; r++ {
				v := int8(q.raw[r*m.n+c])
				m.w[c*m.k+r] = v
				m.sum[c] += int32(v)
			}
		}
		return m
	}
	nt := &net{}
	nt.word, nt.wordScale, nt.wordZP = embQ("inner.bert.embeddings.word_embeddings", vocabSize)
	nt.pos = emb("inner.bert.embeddings.position_embeddings", maxPos)
	nt.typ = emb("inner.bert.embeddings.token_type_embeddings", 2)
	if t := get("onnx::Slice_206", tInt64, 1, maxPos); t != nil {
		nt.posIDs = t.int64s()
	}
	nt.embLNw = fl("inner.bert.embeddings.LayerNorm.weight", hidden)
	nt.embLNb = fl("inner.bert.embeddings.LayerNorm.bias", hidden)
	for i := range nt.layers {
		l := &nt.layers[i]
		p := fmt.Sprintf("inner.bert.encoder.layer.%d.", i)
		m := 1299 + 16*i // the exporter's names for the layer's three matmuls
		l.qkv = mat(fmt.Sprintf("Attention_%d_qkv_weight", i), hidden, 3*hidden)
		l.qkvBias = fl(fmt.Sprintf("Attention_%d_qkv_bias", i), 3*hidden)
		l.attnOut = mat(fmt.Sprintf("onnx::MatMul_%d", m), hidden, hidden)
		l.attnBias = fl(p+"attention.output.dense.bias", hidden)
		l.ln1w = fl(p+"attention.output.LayerNorm.weight", hidden)
		l.ln1b = fl(p+"attention.output.LayerNorm.bias", hidden)
		l.up = mat(fmt.Sprintf("onnx::MatMul_%d", m+1), hidden, inter)
		l.upBias = fl(p+"intermediate.dense.bias", inter)
		l.smooth = fl(fmt.Sprintf("r15_smooth_inv_s_%d", i), inter)
		l.down = mat(fmt.Sprintf("onnx::MatMul_%d", m+2), inter, hidden)
		l.downBias = fl(p+"output.dense.bias", hidden)
		l.ln2w = fl(p+"output.LayerNorm.weight", hidden)
		l.ln2b = fl(p+"output.LayerNorm.bias", hidden)
	}
	nt.cls = mat("onnx::MatMul_1478", hidden, -1)
	if missing == nil {
		nt.labels = nt.cls.n
		nt.clsBias = fl("inner.classifier.bias", int64(nt.labels))
	}
	if missing != nil {
		return nil, missing
	}
	return nt, nil
}

// quant is DynamicQuantizeLinear: x as uint8 over its range (0 always in
// it), with its scale and zero point, as ONNX Runtime computes them.
func quant(x []float32) (q []uint8, scale float32, zp int32) {
	lo, hi := rangeOf(x)
	scale = float32(1)
	if hi != lo {
		scale = (hi - lo) / 255
	}
	z := min(float32(255), max(float32(0), 0-lo/scale))
	zp = int32(math.RoundToEven(float64(z)))
	q = make([]uint8, len(x))
	for i, v := range x {
		r := int32(math.RoundToEven(float64(v/scale))) + zp
		q[i] = uint8(min(255, max(0, r)))
	}
	return q, scale, zp
}

// matmul is x [rows, m.k] quantized, times m: float32(Σ (x-xz)(w-wz)) *
// (xs*ws), as MatMulInteger, Cast and Mul do it, in out [rows, m.n], over
// t goroutines. The
// kernels take int8 on both sides, so x's uint8 q goes as q-128, and
// Σ q·w = Σ (q-128)·w + 128·Σ w.
func (m *qmat) matmul(x []float32, rows, t int) []float32 {
	q, xs, xz := quant(x)
	s := make([]int8, len(q))
	xsum := make([]int32, rows)
	for r := 0; r < rows; r++ {
		var sum int32
		for i, v := range q[r*m.k : r*m.k+m.k] {
			sum += int32(v)
			s[r*m.k+i] = int8(int32(v) - 128)
		}
		xsum[r] = sum
	}
	out := make([]float32, rows*m.n)
	groups := (m.n + 3) / 4
	parallel(t, groups, func(g0, g1 int) {
		var acc [4]int32
		for g := g0; g < g1; g++ {
			c0 := 4 * g
			nc := min(4, m.n-c0)
			for r := 0; r < rows; r++ {
				a := s[r*m.k : r*m.k+m.k]
				if nc == 4 {
					dot4(a, m.w[c0*m.k:(c0+4)*m.k], &acc)
				} else {
					for j := 0; j < nc; j++ {
						acc[j] = dot(a, m.w[(c0+j)*m.k:(c0+j+1)*m.k])
					}
				}
				for j := 0; j < nc; j++ {
					c := c0 + j
					wz := m.zp[c]
					v := acc[j] + 128*m.sum[c] - wz*xsum[r] - xz*m.sum[c] + int32(m.k)*xz*wz
					out[r*m.n+c] = float32(v) * (xs * m.scale[c])
				}
			}
		}
	})
	return out
}

// parallel calls f over [0, n) in t parts at once; a panic in one is
// panicked again here once all have ended.
func parallel(t, n int, f func(a, b int)) {
	t = min(t, n)
	if t <= 1 {
		f(0, n)
		return
	}
	var wg sync.WaitGroup
	var c carried
	step := (n + t - 1) / t
	for a := 0; a < n; a += step {
		wg.Add(1)
		go func(a, b int) {
			defer wg.Done()
			defer c.catch()
			f(a, b)
		}(a, min(n, a+step))
	}
	wg.Wait()
	c.rethrow()
}

// layerNorm sets each row of x to its layer norm (epsilon 1e-12).
func layerNorm(x []float32, rows int, w, b []float32) {
	for r := 0; r < rows; r++ {
		row := x[r*hidden : r*hidden+hidden]
		var mean float32
		for _, v := range row {
			mean += v
		}
		mean /= hidden
		var vr float32
		for _, v := range row {
			d := v - mean
			vr += d * d
		}
		vr /= hidden
		inv := float32(1 / math.Sqrt(float64(vr)+1e-12))
		for i, v := range row {
			row[i] = (v-mean)*inv*w[i] + b[i]
		}
	}
}

// logits are the classifier's scores for each of ids, a sequence of up to
// maxPos tokens with its <s> and </s>: [len(ids), labels], read over procs
// goroutines.
func (nt *net) logits(ids []int, procs int) []float32 {
	return nt.encode(nt.embed(ids, 0), len(ids), procs)
}

// embed is the layer-normed embeddings of ids, [len(ids), hidden], in a
// slice of at least rows rows (the rest zero).
func (nt *net) embed(ids []int, rows int) []float32 {
	n := len(ids)
	x := make([]float32, max(n, rows)*hidden)
	for t, id := range ids {
		row := x[t*hidden : t*hidden+hidden]
		w := nt.word[id*hidden : id*hidden+hidden]
		p := nt.pos[int(nt.posIDs[t])*hidden:]
		for i := range row {
			row[i] = (float32(int32(w[i])-nt.wordZP[i])*nt.wordScale[i] + nt.typ[i]) + p[i]
		}
	}
	layerNorm(x, n, nt.embLNw, nt.embLNb)
	return x
}

// encode is the classifier's scores for embeddings x of n tokens, over
// procs goroutines.
func (nt *net) encode(x []float32, n, procs int) []float32 {
	x = nt.hidden(x, n, procs)
	out := nt.cls.matmul(x, n, procs)
	for t := 0; t < n; t++ {
		row := out[t*nt.labels : t*nt.labels+nt.labels]
		for i := range row {
			row[i] = nt.clsBias[i] + row[i]
		}
	}
	return out
}

// hidden is the encoder's last hidden state for embeddings x of n tokens,
// [n, hidden], over procs goroutines.
func (nt *net) hidden(x []float32, n, procs int) []float32 {
	for i := range nt.layers {
		l := &nt.layers[i]
		qkv := l.qkv.matmul(x, n, procs)
		for t := 0; t < n; t++ {
			row := qkv[t*3*hidden : t*3*hidden+3*hidden]
			for i, b := range l.qkvBias {
				row[i] += b
			}
		}
		ctx := attention(qkv, n, procs)
		a := l.attnOut.matmul(ctx, n, procs)
		skipNorm(a, x, n, l.attnBias, l.ln1w, l.ln1b)
		x = a
		h := l.up.matmul(x, n, procs)
		for t := 0; t < n; t++ {
			row := h[t*inter : t*inter+inter]
			geluRow(row, l.upBias, l.smooth)
		}
		o := l.down.matmul(h, n, procs)
		skipNorm(o, x, n, l.downBias, l.ln2w, l.ln2b)
		x = o
	}
	return x
}

// skipNorm is SkipLayerNormalization: out = norm(out + skip + bias).
func skipNorm(out, skip []float32, rows int, bias, w, b []float32) {
	for r := 0; r < rows; r++ {
		o := out[r*hidden : r*hidden+hidden]
		s := skip[r*hidden : r*hidden+hidden]
		for i := range o {
			o[i] = o[i] + s[i] + bias[i]
		}
	}
	layerNorm(out, rows, w, b)
}

// attention is the self-attention of qkv [n, 3*hidden] (Q, K, V side by
// side, each head's 32 columns together), every token seeing every other.
// It runs over procs goroutines.
func attention(qkv []float32, n, procs int) []float32 {
	const d = hidden / heads
	scale := float32(1 / math.Sqrt(d))
	ctx := make([]float32, n*hidden)
	// Each head's q and v laid out [n][d], and its k transposed, [d][np]
	// with np = n rounded up to 16 (the rest zero), so the kernels read
	// contiguous rows.
	np := (n + 15) &^ 15
	q := make([]float32, heads*n*d)
	kt := make([]float32, heads*d*np)
	v := make([]float32, heads*n*d)
	for t := 0; t < n; t++ {
		row := qkv[t*3*hidden : t*3*hidden+3*hidden]
		for h := 0; h < heads; h++ {
			o := (h*n + t) * d
			copy(q[o:o+d], row[h*d:h*d+d])
			copy(v[o:o+d], row[2*hidden+h*d:2*hidden+h*d+d])
			for i, x := range row[hidden+h*d : hidden+h*d+d] {
				kt[(h*d+i)*np+t] = x
			}
		}
	}
	parallel(procs, heads*n, func(a, b int) {
		p := make([]float32, np)
		var qs, c [d]float32
		for j := a; j < b; j++ {
			h, t := j/n, j%n
			for i, x := range q[(h*n+t)*d : (h*n+t)*d+d] {
				qs[i] = x * scale
			}
			scores(&qs, kt[h*d*np:(h+1)*d*np], p)
			ps := p[:n]
			mx := maxOf(ps)
			expSub(ps, mx)
			var sum float32
			for _, e := range ps {
				sum += e
			}
			inv := 1 / sum
			for u := range ps {
				ps[u] *= inv
			}
			c = [d]float32{}
			mix(&c, ps, v[h*n*d:(h+1)*n*d])
			copy(ctx[t*hidden+h*d:t*hidden+h*d+d], c[:])
		}
	})
	return ctx
}
