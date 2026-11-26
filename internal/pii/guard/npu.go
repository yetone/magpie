package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/pii/guard/levelzero"
)

// On Windows with an Intel NPU (Core Ultra's AI Boost), the encoder runs
// through Level Zero's ZE_extension_graph (ze_loader.dll in System32):
// a window of 512 tokens takes ~40 ms on the NPU at ~2 W where bert.go
// takes ~410 ms across 4 CPU cores, while discrete GPUs stay asleep. The
// OpenVINO IR v11 graph is built in Go from the downloaded int8 weights
// (dequantized to float16, as coreml.go does), compiled once by the NPU
// driver (~7 s the first time) into a native binary kept in dir/npu, and
// opened from disk in ~45 ms after that. When no NPU is on the computer,
// or compiling or running on it fails, bert.go reads on the CPU.

const (
	npuMin       = 16
	npuIRVersion = "astrlink-guard-ov11-v1"
)

var (
	npuCompile = levelzero.Compile
	npuRead    = npuLogits
	openNPU    = openLevelZero
)

// openLevelZero opens the compiled NPU graph for nt, compiling it into dir/npu
// first when it isn't on disk yet. hasNPU reports whether an NPU device
// was found on this computer, so a machine with none reports no error.
func openLevelZero(dir string, nt *net) (m *levelzero.Model, hasNPU bool, err error) {
	info, ok, err := levelzero.Probe()
	if !ok {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	path := filepath.Join(dir, "npu", npuHash(info, nt)+".blob")
	if _, statErr := os.Stat(path); statErr == nil {
		if m, err := levelzero.Open(path, maxPos, hidden, nt.labels); err == nil {
			return m, true, nil
		}
	}
	if err := compileNPU(path, info, nt); err != nil {
		return nil, true, err
	}
	m, err = levelzero.Open(path, maxPos, hidden, nt.labels)
	if err != nil {
		return nil, true, err
	}
	return m, true, nil
}

func compileNPU(path string, info levelzero.Info, nt *net) error {
	blob, err := npuCompile(npuIR(nt, info.CompilerMajor, info.CompilerMinor))
	if err != nil {
		return err
	}
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, "blob-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	es, _ := os.ReadDir(root)
	for _, e := range es {
		if n := e.Name(); n != filepath.Base(path) {
			os.RemoveAll(filepath.Join(root, n))
		}
	}
	return nil
}

func npuHash(info levelzero.Info, nt *net) string {
	h := sha256.New()
	h.Write([]byte(npuIRVersion))
	var hdr [16]byte
	le := binary.LittleEndian
	le.PutUint32(hdr[0:], info.DeviceID)
	le.PutUint32(hdr[4:], info.DriverVersion)
	le.PutUint16(hdr[8:], info.CompilerMajor)
	le.PutUint16(hdr[10:], info.CompilerMinor)
	le.PutUint32(hdr[12:], uint32(nt.labels))
	h.Write(hdr[:])
	floats := func(fs []float32) {
		var b [4]byte
		for _, f := range fs {
			le.PutUint32(b[:], math.Float32bits(f))
			h.Write(b[:])
		}
	}
	ints := func(xs []int32) {
		var b [4]byte
		for _, x := range xs {
			le.PutUint32(b[:], uint32(x))
			h.Write(b[:])
		}
	}
	bytes8 := func(ws []int8) {
		var buf [4096]byte
		for len(ws) > 0 {
			n := min(len(ws), len(buf))
			for i, w := range ws[:n] {
				buf[i] = byte(w)
			}
			h.Write(buf[:n])
			ws = ws[n:]
		}
	}
	for i := range nt.layers {
		l := &nt.layers[i]
		for _, q := range []*qmat{&l.qkv, &l.attnOut, &l.up, &l.down} {
			bytes8(q.w)
			floats(q.scale)
			ints(q.zp)
			ints(q.sum)
		}
		for _, fs := range [][]float32{l.qkvBias, l.attnBias, l.ln1w, l.ln1b, l.upBias, l.smooth, l.downBias, l.ln2w, l.ln2b} {
			floats(fs)
		}
	}
	bytes8(nt.cls.w)
	ints(nt.cls.zp)
	floats(nt.cls.scale)
	floats(nt.clsBias)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
}

func npuLogits(m *levelzero.Model, nt *net, ids []int) ([]float32, error) {
	n := len(ids)
	if n > maxPos {
		return nil, errors.New("more tokens than a window")
	}
	mask := make([]float32, maxPos)
	for t := n; t < maxPos; t++ {
		mask[t] = -1e4
	}
	out, err := m.Run(nt.embed(ids, maxPos), mask, maxPos, hidden, nt.labels)
	if err != nil {
		return nil, err
	}
	return out[:n*nt.labels], nil
}

// npuProbe is read on the NPU and on the CPU each time the model loads,
// before the NPU reads any request. The graph is built here and compiled
// by whatever NPU driver the computer has, and its labels were checked
// against bert.go's on one Core Ultra 9 185H alone (#7059): a driver or
// compiler that reads it differently is not trusted with masking.
const npuProbe = "Contact 张伟 at zhang.wei@example.com or 13800138000. 地址：北京市海淀区中关村大街1号，" +
	"身份证 110101199003077777, password: Tr0ub4dor&3, card 6222 0212 3456 7890 123."

// npuAgrees says why not when the NPU labels a token of npuProbe other than
// bert.go does. A token the CPU itself barely tells apart (its top two
// labels' probabilities within npuMargin) may go either way: float16 on the
// NPU moves probabilities by a few hundredths.
func (m *Model) npuAgrees() error {
	ids := []int{0}
	for _, t := range m.tok.encode(npuProbe) {
		ids = append(ids, t.id)
	}
	ids = append(ids, 2)
	got, err := npuRead(m.npu, m.net, ids)
	if err != nil {
		return err
	}
	return labelsAgree(got, m.net.logits(ids, 1), m.net.labels)
}

// labelsAgree says why not when a token's top label in got isn't its top
// label in want, by k labels a token, unless want's top two labels are
// within npuMargin of each other.
func labelsAgree(got, want []float32, k int) error {
	if len(got) != len(want) {
		return fmt.Errorf("the NPU gave %d scores where the CPU gave %d", len(got), len(want))
	}
	for t := range len(want) / k {
		g, w := got[t*k:(t+1)*k], want[t*k:(t+1)*k]
		top, second := topTwo(w)
		if argmax(g) != top && softmaxGap(w, top, second) >= npuMargin {
			return fmt.Errorf("the NPU labels a check other than the CPU does (token %d: %s, not %s)", t, labels[argmax(g)], labels[top])
		}
	}
	return nil
}

const npuMargin = 0.2

func argmax(xs []float32) int {
	best := 0
	for i, x := range xs {
		if x > xs[best] {
			best = i
		}
	}
	return best
}

func topTwo(xs []float32) (top, second int) {
	top, second = 0, -1
	for i := 1; i < len(xs); i++ {
		switch {
		case xs[i] > xs[top]:
			top, second = i, top
		case second < 0 || xs[i] > xs[second]:
			second = i
		}
	}
	return top, second
}

// softmaxGap is the difference of the probabilities of labels a and b.
func softmaxGap(xs []float32, a, b int) float64 {
	if b < 0 {
		return 1
	}
	var sum float64
	mx := float64(xs[a])
	for _, x := range xs {
		sum += math.Exp(float64(x) - mx)
	}
	return (math.Exp(float64(xs[a])-mx) - math.Exp(float64(xs[b])-mx)) / sum
}

// npuIR builds the serialized OpenVINO IR v11 buffer (VCLSerializerWithWeightsCopy
// layout: compiler major/minor, uint32(2), uint64(xmlSize), xml, uint64(weightsSize),
// weights) for AstrLink Guard's 12-layer BERT encoder + token classifier.
func npuIR(nt *net, compilerMajor, compilerMinor uint16) []byte {
	const headDim = hidden / heads
	var b ovIRBuilder
	xDims := []int{1, maxPos, hidden}
	maskDims := []int{1, 1, 1, maxPos}
	h4d := []int{1, maxPos, heads, headDim}
	t4d := []int{1, heads, maxPos, headDim}
	s4d := []int{1, heads, maxPos, maxPos}

	x := b.param("x", xDims)
	mask := b.param("mask", maskDims)

	shHeads := b.constI64("sh_heads", []int64{1, maxPos, heads, headDim})
	perm := b.constI64("perm_0213", []int64{0, 2, 1, 3})
	shFlat := b.constI64("sh_flat", []int64{1, maxPos, hidden})
	lnAxes := b.constI64("ln_axes", []int64{-1})
	qScale := b.constF16("q_scale", []float32{float32(1 / math.Sqrt(headDim))}, []int{1, 1, 1, 1})

	deq := func(m *qmat) []float32 {
		out := make([]float32, len(m.w))
		for c := 0; c < m.n; c++ {
			for r := 0; r < m.k; r++ {
				out[c*m.k+r] = float32(int32(m.w[c*m.k+r])-m.zp[c]) * m.scale[c]
			}
		}
		return out
	}
	toHeads := func(name string, t int) int {
		r := b.reshape(name+"_r", t, xDims, shHeads, 4, h4d)
		return b.transpose(name+"_t", r, h4d, perm, t4d)
	}

	for i := range nt.layers {
		l := &nt.layers[i]
		pfx := fmt.Sprintf("l%d", i)
		qkvw := deq(&l.qkv)
		qw := qkvw[:hidden*hidden]
		kw := qkvw[hidden*hidden : 2*hidden*hidden]
		vw := qkvw[2*hidden*hidden:]
		qb := l.qkvBias[:hidden]
		kb := l.qkvBias[hidden : 2*hidden]
		vb := l.qkvBias[2*hidden:]

		qLin, _ := b.linear(pfx+"_q", "", x, xDims, qw, hidden, hidden, qb)
		kLin, _ := b.linear(pfx+"_k", "", x, xDims, kw, hidden, hidden, kb)
		vLin, _ := b.linear(pfx+"_v", "", x, xDims, vw, hidden, hidden, vb)

		qh := toHeads(pfx+"_qh", qLin)
		kh := toHeads(pfx+"_kh", kLin)
		vh := toHeads(pfx+"_vh", vLin)

		qs := b.eltwise("Multiply", pfx+"_qs", "", qh, t4d, qScale, []int{1, 1, 1, 1})
		sc, _ := b.matmul(pfx+"_qk", qs, t4d, kh, t4d, true)
		scm := b.eltwise("Add", pfx+"_mask", "", sc, s4d, mask, maskDims)
		pr := b.softmax(pfx+"_sm", scm, s4d)
		ct, _ := b.matmul(pfx+"_pv", pr, s4d, vh, t4d, false)
		ctt := b.transpose(pfx+"_ctt", ct, t4d, perm, h4d)
		ctf := b.reshape(pfx+"_ctf", ctt, h4d, shFlat, 3, xDims)

		ao, _ := b.linear(pfx+"_ao", "", ctf, xDims, deq(&l.attnOut), hidden, hidden, l.attnBias)
		res1 := b.eltwise("Add", pfx+"_res1", "", ao, xDims, x, xDims)
		x = b.layerNorm(pfx+"_ln1", res1, xDims, lnAxes, l.ln1w, l.ln1b)

		up, upDims := b.linear(pfx+"_up", "", x, xDims, deq(&l.up), hidden, inter, l.upBias)
		ge := b.gelu(pfx+"_gelu", up, upDims)
		sm := b.constF16(pfx+"_smooth", l.smooth, []int{1, 1, inter})
		h := b.eltwise("Multiply", pfx+"_h", "", ge, upDims, sm, []int{1, 1, inter})

		dn, _ := b.linear(pfx+"_dn", "", h, upDims, deq(&l.down), inter, hidden, l.downBias)
		res2 := b.eltwise("Add", pfx+"_res2", "", dn, xDims, x, xDims)
		x = b.layerNorm(pfx+"_ln2", res2, xDims, lnAxes, l.ln2w, l.ln2b)
	}

	logits, lDims := b.linear("logits", "logits", x, xDims, deq(&nt.cls), hidden, nt.labels, nt.clsBias)
	resID := b.id()
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=\"logits_sink\" type=\"Result\" version=\"opset1\"><input><port id=\"0\" precision=\"FP32\">%s</port></input></layer>\n",
		strconv.Itoa(resID), ovDimsXML(lDims))
	b.edge(logits, 0, resID, 0)

	var xml bytes.Buffer
	xml.WriteString("<?xml version=\"1.0\"?>\n<net name=\"astrlink_guard\" version=\"11\">\n\t<layers>\n")
	xml.WriteString(b.layers.String())
	xml.WriteString("\t</layers>\n\t<edges>\n")
	xml.WriteString(b.edges.String())
	xml.WriteString("\t</edges>\n</net>\n")

	xb := xml.Bytes()
	wb := b.weights.Bytes()
	out := make([]byte, 24+len(xb)+len(wb))
	le := binary.LittleEndian
	le.PutUint16(out[0:], compilerMajor)
	le.PutUint16(out[2:], compilerMinor)
	le.PutUint32(out[4:], 2)
	le.PutUint64(out[8:], uint64(len(xb)))
	copy(out[16:], xb)
	wOff := 16 + len(xb)
	le.PutUint64(out[wOff:], uint64(len(wb)))
	copy(out[wOff+8:], wb)
	return out
}

type ovIRBuilder struct {
	layers  strings.Builder
	edges   strings.Builder
	weights bytes.Buffer
	nextID  int
}

func (b *ovIRBuilder) id() int {
	id := b.nextID
	b.nextID++
	return id
}

func ovDimsXML(dims []int) string {
	var s strings.Builder
	for _, d := range dims {
		fmt.Fprintf(&s, "<dim>%d</dim>", d)
	}
	return s.String()
}

func ovShapeAttr(dims []int) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = strconv.Itoa(d)
	}
	return strings.Join(parts, ",")
}

func (b *ovIRBuilder) edge(fromL, fromP, toL, toP int) {
	fmt.Fprintf(&b.edges, "\t\t<edge from-layer=%q from-port=%q to-layer=%q to-port=%q/>\n",
		strconv.Itoa(fromL), strconv.Itoa(fromP), strconv.Itoa(toL), strconv.Itoa(toP))
}

func (b *ovIRBuilder) param(name string, dims []int) int {
	id := b.id()
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Parameter\" version=\"opset1\"><data shape=%q element_type=\"f32\"/><output><port id=\"0\" precision=\"FP32\" names=%q>%s</port></output></layer>\n",
		strconv.Itoa(id), name, ovShapeAttr(dims), name, ovDimsXML(dims))
	return id
}

func (b *ovIRBuilder) constI64(name string, vals []int64) int {
	id := b.id()
	off := b.weights.Len()
	for _, v := range vals {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		b.weights.Write(buf[:])
	}
	sz := len(vals) * 8
	dims := []int{len(vals)}
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Const\" version=\"opset1\"><data element_type=\"i64\" shape=%q offset=%q size=%q/><output><port id=\"0\" precision=\"I64\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, ovShapeAttr(dims), strconv.Itoa(off), strconv.Itoa(sz), ovDimsXML(dims))
	return id
}

func (b *ovIRBuilder) constF16(name string, vals []float32, dims []int) int {
	cid := b.id()
	off := b.weights.Len()
	raw := make([]byte, len(vals)*2)
	for i, f := range vals {
		binary.LittleEndian.PutUint16(raw[2*i:], half(f))
	}
	b.weights.Write(raw)
	dxml := ovDimsXML(dims)
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Const\" version=\"opset1\"><data element_type=\"f16\" shape=%q offset=%q size=%q/><output><port id=\"0\" precision=\"FP16\">%s</port></output></layer>\n",
		strconv.Itoa(cid), name+"_f16", ovShapeAttr(dims), strconv.Itoa(off), strconv.Itoa(len(raw)), dxml)
	cvid := b.id()
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Convert\" version=\"opset1\"><data destination_type=\"f32\"/><input><port id=\"0\" precision=\"FP16\">%s</port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(cvid), name, dxml, dxml)
	b.edge(cid, 0, cvid, 0)
	return cvid
}

func (b *ovIRBuilder) matmul(name string, a int, adims []int, w int, wdims []int, transB bool) (int, []int) {
	id := b.id()
	tb := "false"
	outLast := wdims[len(wdims)-1]
	if transB {
		tb = "true"
		outLast = wdims[len(wdims)-2]
	}
	odims := append(slices.Clone(adims[:len(adims)-1]), outLast)
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"MatMul\" version=\"opset1\"><data transpose_a=\"false\" transpose_b=%q/><input><port id=\"0\" precision=\"FP32\">%s</port><port id=\"1\" precision=\"FP32\">%s</port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, tb, ovDimsXML(adims), ovDimsXML(wdims), ovDimsXML(odims))
	b.edge(a, 0, id, 0)
	b.edge(w, 0, id, 1)
	return id, odims
}

func (b *ovIRBuilder) eltwise(op, name, outTensorName string, a int, adims []int, c int, cdims []int) int {
	id := b.id()
	namesAttr := ""
	if outTensorName != "" {
		namesAttr = fmt.Sprintf(" names=%q", outTensorName)
	}
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=%q version=\"opset1\"><data auto_broadcast=\"numpy\"/><input><port id=\"0\" precision=\"FP32\">%s</port><port id=\"1\" precision=\"FP32\">%s</port></input><output><port id=\"0\" precision=\"FP32\"%s>%s</port></output></layer>\n",
		strconv.Itoa(id), name, op, ovDimsXML(adims), ovDimsXML(cdims), namesAttr, ovDimsXML(adims))
	b.edge(a, 0, id, 0)
	b.edge(c, 0, id, 1)
	return id
}

func (b *ovIRBuilder) linear(name, outTensorName string, x int, xdims []int, w []float32, k, n int, bias []float32) (int, []int) {
	wid := b.constF16(name+"_w", w, []int{n, k})
	mm, mmdims := b.matmul(name+"_mm", x, xdims, wid, []int{n, k}, true)
	bid := b.constF16(name+"_b", bias, []int{1, 1, n})
	add := b.eltwise("Add", name, outTensorName, mm, mmdims, bid, []int{1, 1, n})
	return add, mmdims
}

func (b *ovIRBuilder) reshape(name string, x int, xdims []int, shapeConst int, sdims int, odims []int) int {
	id := b.id()
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Reshape\" version=\"opset1\"><data special_zero=\"false\"/><input><port id=\"0\" precision=\"FP32\">%s</port><port id=\"1\" precision=\"I64\"><dim>%d</dim></port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, ovDimsXML(xdims), sdims, ovDimsXML(odims))
	b.edge(x, 0, id, 0)
	b.edge(shapeConst, 0, id, 1)
	return id
}

func (b *ovIRBuilder) transpose(name string, x int, xdims []int, permConst int, odims []int) int {
	id := b.id()
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Transpose\" version=\"opset1\"><input><port id=\"0\" precision=\"FP32\">%s</port><port id=\"1\" precision=\"I64\"><dim>%d</dim></port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, ovDimsXML(xdims), len(odims), ovDimsXML(odims))
	b.edge(x, 0, id, 0)
	b.edge(permConst, 0, id, 1)
	return id
}

func (b *ovIRBuilder) softmax(name string, x int, xdims []int) int {
	id := b.id()
	dxml := ovDimsXML(xdims)
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Softmax\" version=\"opset8\"><data axis=\"-1\"/><input><port id=\"0\" precision=\"FP32\">%s</port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, dxml, dxml)
	b.edge(x, 0, id, 0)
	return id
}

func (b *ovIRBuilder) gelu(name string, x int, xdims []int) int {
	id := b.id()
	dxml := ovDimsXML(xdims)
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"Gelu\" version=\"opset7\"><data approximation_mode=\"erf\"/><input><port id=\"0\" precision=\"FP32\">%s</port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(id), name, dxml, dxml)
	b.edge(x, 0, id, 0)
	return id
}

func (b *ovIRBuilder) layerNorm(name string, x int, xdims []int, axesConst int, gamma, beta []float32) int {
	mvn := b.id()
	dxml := ovDimsXML(xdims)
	fmt.Fprintf(&b.layers, "\t\t<layer id=%q name=%q type=\"MVN\" version=\"opset6\"><data normalize_variance=\"true\" eps=\"1e-05\" eps_mode=\"inside_sqrt\"/><input><port id=\"0\" precision=\"FP32\">%s</port><port id=\"1\" precision=\"I64\"><dim>1</dim></port></input><output><port id=\"0\" precision=\"FP32\">%s</port></output></layer>\n",
		strconv.Itoa(mvn), name+"_mvn", dxml, dxml)
	b.edge(x, 0, mvn, 0)
	b.edge(axesConst, 0, mvn, 1)
	gid := b.constF16(name+"_g", gamma, []int{1, 1, hidden})
	mul := b.eltwise("Multiply", name+"_mul", "", mvn, xdims, gid, []int{1, 1, hidden})
	bid := b.constF16(name+"_b", beta, []int{1, 1, hidden})
	return b.eltwise("Add", name, "", mul, xdims, bid, []int{1, 1, hidden})
}
