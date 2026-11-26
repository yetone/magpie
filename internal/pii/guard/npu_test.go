package guard

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/pii/guard/coreml"
	"github.com/yetone/magpie/internal/pii/guard/levelzero"
)

func guardCopyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	for _, f := range Files {
		s := filepath.Join(src, f)
		d := filepath.Join(dst, f)
		if err := os.Link(s, d); err != nil {
			b, err := os.ReadFile(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dst
}

// On Windows with an Intel NPU, Level Zero labels a long window as bert.go
// does, within 1% of tokens, and finds the exact same spans with scores
// within .25 (float16 NPU MACs vs bert.go's dynamic int8 quantization,
// each up to .13 from the publisher's float32 model). Load compiles the
// native blob into dir/npu the first time and opens the cached blob in
// milliseconds on subsequent loads.
func TestNPUMatchesGo(t *testing.T) {
	src := dir(t)
	if _, ok, _ := levelzero.Probe(); !ok {
		t.Skip("no Intel NPU here")
	}
	d := guardCopyDir(t, src)
	t0 := time.Now()
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.npu == nil {
		t.Fatalf("NPU didn't open: %v", m.NPUErr)
	}
	t.Logf("first load (compiled) in %v on %q", time.Since(t0), m.npu.Device)

	// Second Load opens from the cached native blob without recompiling.
	tWarm := time.Now()
	m2, err := Load(d)
	if err != nil || m2.npu == nil {
		t.Fatalf("cached Load: %v, npuErr=%v", err, m2.NPUErr)
	}
	t.Logf("cached load in %v", time.Since(tWarm))

	via, dev, berr := m.Backend()
	if via != "Level Zero" || dev != m.npu.Device || dev == "" || berr != nil {
		t.Fatalf("Backend = %q %q %v", via, dev, berr)
	}

	text := strings.Repeat("联系人：张三，电话 13800138000，邮箱 zhangsan@example.com。The host 10.20.30.40 logs in as deploy with key sk-proj-AbCdEf0123456789xyz.\n", 12)
	toks := m.tok.encode(text)
	ids := []int{0}
	for _, k := range toks[:min(window, len(toks))] {
		ids = append(ids, k.id)
	}
	ids = append(ids, 2)
	want := m.net.logits(ids, Cores())
	t1 := time.Now()
	got, err := npuLogits(m.npu, m.net, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d tokens: NPU %v", len(ids), time.Since(t1))
	k, same := len(labels), 0
	for i := range ids {
		if argmax(got[i*k:(i+1)*k]) == argmax(want[i*k:(i+1)*k]) {
			same++
		}
	}
	if same*100 < len(ids)*99 {
		t.Fatalf("NPU labels %d of %d tokens as bert.go does", same, len(ids))
	}
	goModel := &Model{tok: m.tok, net: m.net}
	a, b := m.Find(text), goModel.Find(text)
	if len(a) != len(b) {
		t.Fatalf("NPU finds %d spans, bert.go %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Start != b[i].Start || a[i].End != b[i].End || a[i].Label != b[i].Label || math.Abs(a[i].Score-b[i].Score) > .25 {
			t.Errorf("NPU %v, bert.go %v", a[i], b[i])
		}
	}
}

// Concurrent Finds on an NPU model serialize safely over its command queue
// and return identical spans to sequential Finds.
func TestNPUFindsAtOnce(t *testing.T) {
	src := dir(t)
	if _, ok, _ := levelzero.Probe(); !ok {
		t.Skip("no Intel NPU here")
	}
	m, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if m.npu == nil {
		t.Fatalf("NPU didn't open: %v", m.NPUErr)
	}
	texts := make([]string, 6)
	for i := range texts {
		var b strings.Builder
		for k := range 4 + 3*i {
			fmt.Fprintf(&b, "第%d位联系人：张三，电话 138%08d，邮箱 user%d@example.com。\n", k, (i+1)*k*7919, k)
		}
		texts[i] = b.String()
	}
	alone := make([][]Span, len(texts))
	for i, s := range texts {
		alone[i] = m.Find(s)
	}
	at := make([][]Span, len(texts))
	var wg sync.WaitGroup
	for i, s := range texts {
		wg.Go(func() { at[i] = m.Find(s) })
	}
	wg.Wait()
	for i := range texts {
		if len(alone[i]) == 0 || !reflect.DeepEqual(at[i], alone[i]) {
			t.Fatalf("text %d: at once %d spans, alone %d", i, len(at[i]), len(alone[i]))
		}
	}
}

// When NPU compilation fails, Load still succeeds and bert.go reads on the
// CPU, leaving any earlier cached blobs untouched; when a cached blob is
// corrupt, openNPU recompiles over it.
func TestNPUFailureFallsBack(t *testing.T) {
	src := dir(t)
	if _, ok, _ := levelzero.Probe(); !ok {
		t.Skip("no Intel NPU here")
	}
	d := guardCopyDir(t, src)
	npuDir := filepath.Join(d, "npu")
	if err := os.MkdirAll(npuDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(npuDir, "old.blob")
	if err := os.WriteFile(kept, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved := npuCompile
	t.Cleanup(func() { npuCompile = saved })
	npuCompile = func([]byte) ([]byte, error) { return nil, errors.New("compile failed") }

	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.npu != nil || m.NPUErr == nil {
		t.Fatalf("NPU opened: %v", m.NPUErr)
	}
	if via, dev, berr := m.Backend(); via != "" || dev != "CPU" || berr != m.NPUErr {
		t.Fatalf("Backend = %q %q %v", via, dev, berr)
	}
	text := strings.Repeat("联系人：张三，电话 13800138000。", 8)
	if got := m.Find(text); len(got) == 0 {
		t.Fatal("bert.go found nothing")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("earlier blob removed on failed compile: %v", err)
	}

	// Restore compiler, write a corrupt blob at the target path, and check
	// that Load recompiles over it and removes old.blob.
	npuCompile = saved
	info, _, _ := levelzero.Probe()
	corrupt := filepath.Join(npuDir, npuHash(info, m.net)+".blob")
	if err := os.WriteFile(corrupt, []byte("not-an-elf-blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, err := Load(d)
	if err != nil || m2.npu == nil {
		t.Fatalf("recompiling over corrupt blob: %v, npuErr=%v", err, m2.NPUErr)
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Fatalf("old.blob still present after fresh compile: %v", err)
	}
}

// A failed NPU read falls back to bert.go and is reported by Backend until
// a subsequent read succeeds.
func TestFailedNPUReadIsSaid(t *testing.T) {
	src := dir(t)
	if _, ok, _ := levelzero.Probe(); !ok {
		t.Skip("no Intel NPU here")
	}
	m, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if m.npu == nil {
		t.Fatalf("NPU didn't open: %v", m.NPUErr)
	}
	ids := make([]int, 40)
	for i := range ids {
		ids[i] = 5
	}
	ids[0], ids[len(ids)-1] = 0, 2
	m.logits(ids, 1)
	if via, _, err := m.Backend(); via != "Level Zero" || err != nil {
		t.Fatalf("after a good read: %q %v", via, err)
	}
	t.Cleanup(func() { npuRead = npuLogits })
	npuRead = func(*levelzero.Model, *net, []int) ([]float32, error) { return nil, errors.New("device lost") }
	if lg := m.logits(ids, 1); len(lg) != len(ids)*len(labels) {
		t.Fatalf("bert.go read %d logits", len(lg))
	}
	if via, d, err := m.Backend(); via != "" || d != "CPU" || err == nil || err.Error() != "device lost" {
		t.Fatalf("after a failed read: %q %q %v", via, d, err)
	}
	npuRead = npuLogits
	m.logits(ids, 1)
	if via, _, err := m.Backend(); via != "Level Zero" || err != nil {
		t.Fatalf("after NPU read again: %q %v", via, err)
	}
}

func TestBackendNPU(t *testing.T) {
	if !coreml.Available {
		if via, d, err := (&Model{}).Backend(); via != "" || d != "CPU" || err != nil {
			t.Fatalf("no NPU on machine: %q %q %v", via, d, err)
		}
		made := errors.New("NPU compile failed")
		if via, d, err := (&Model{hasNPU: true, NPUErr: made}).Backend(); via != "" || d != "CPU" || err != made {
			t.Fatalf("failed NPU init: %q %q %v", via, d, err)
		}
	}
	m := &Model{npu: &levelzero.Model{Device: "Intel(R) AI Boost"}, hasNPU: true}
	if via, d, err := m.Backend(); via != "Level Zero" || d != "Intel(R) AI Boost" || err != nil {
		t.Fatalf("with NPU: %q %q %v", via, d, err)
	}
	e := "NPU execute failed (0x70000001)"
	m.npuFail.Store(&e)
	if via, d, err := m.Backend(); via != "" || d != "CPU" || err == nil || err.Error() != e {
		t.Fatalf("after failed NPU read: %q %q %v", via, d, err)
	}
	m.npuFail.Store(nil)
	if via, d, err := m.Backend(); via != "Level Zero" || d != "Intel(R) AI Boost" || err != nil {
		t.Fatalf("after good NPU read: %q %q %v", via, d, err)
	}
}

// Opening a cached native NPU blob while the garbage collector scavenges free
// spans — and scavenging again after Open returns — leaves the graph intact:
// Open keeps the blob buffer live across pfnGraphInitialize, after which the
// NPU driver owns its own device copies of the weights.
func TestNPUOpenUnderGC(t *testing.T) {
	src := dir(t)
	if _, ok, _ := levelzero.Probe(); !ok {
		t.Skip("no Intel NPU here")
	}
	base, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if base.npu == nil {
		t.Fatalf("NPU didn't open: %v", base.NPUErr)
	}
	text := "联系人：张三，电话 13800138000，邮箱 zhangsan@example.com。"
	want := base.Find(text)
	if len(want) == 0 {
		t.Fatal("expected PII spans")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
				debug.FreeOSMemory()
			}
		}
	})
	defer func() {
		close(stop)
		wg.Wait()
	}()

	for i := range 6 {
		m, err := Load(src)
		if err != nil {
			t.Fatalf("iter %d Load: %v", i, err)
		}
		if m.npu == nil {
			t.Fatalf("iter %d NPU didn't open: %v", i, m.NPUErr)
		}
		runtime.GC()
		debug.FreeOSMemory()
		churn := make([]byte, 64<<20)
		for j := 0; j < len(churn); j += 4096 {
			churn[j] = byte(i + j)
		}
		runtime.KeepAlive(churn)
		if got := m.Find(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("iter %d Find = %v, want %v", i, got, want)
		}
	}
}

func TestNPUHashCoversWeights(t *testing.T) {
	mk := func() *net {
		q := qmat{w: []int8{1, -2, 3, 4}, scale: []float32{0.5, 0.25}, zp: []int32{0, 1}, sum: []int32{-1, 7}, n: 2, k: 2}
		return &net{
			labels:  3,
			layers:  [12]layer{{qkv: q, attnOut: q, up: q, down: q, ln1w: []float32{1, 1}}},
			cls:     qmat{w: []int8{5, 6}, scale: []float32{0.1}, zp: []int32{0}, n: 1, k: 2},
			clsBias: []float32{0.2},
		}
	}
	info := levelzero.Info{DeviceID: 0x7d1d, DriverVersion: 4297, CompilerMajor: 7, CompilerMinor: 6}
	base := npuHash(info, mk())

	nt1 := mk()
	nt1.layers[0].qkv.w[0] = 2 // same sum if paired, or single weight byte changed
	if got := npuHash(info, nt1); got == base {
		t.Fatalf("changing a layer weight byte kept hash %q", got)
	}

	nt2 := mk()
	nt2.layers[0].up.w[0]++
	nt2.layers[0].up.w[1]-- // sum stays identical, weight bytes differ
	if got := npuHash(info, nt2); got == base {
		t.Fatalf("changing weights with equal column sum kept hash %q", got)
	}

	info2 := info
	info2.DriverVersion++
	if got := npuHash(info2, mk()); got == base {
		t.Fatalf("changing driver version kept hash %q", got)
	}
}

// An NPU whose labels for npuProbe differ from bert.go's is not used: the
// graph is compiled by whatever driver the computer has, and was checked
// against the CPU on one machine alone (#7059). A token the CPU barely
// tells apart may go either way.
func TestNPUThatDisagreesIsNotUsed(t *testing.T) {
	k := len(labels)
	row := func(top, second int, a, b float32) []float32 {
		r := make([]float32, k)
		for i := range r {
			r[i] = -8
		}
		r[top], r[second] = a, b
		return r
	}
	cpu := slices.Concat(row(0, 1, 6, -2), row(3, 4, 5, 0), row(5, 6, 1, 0.9))
	if err := labelsAgree(slices.Clone(cpu), cpu, k); err != nil {
		t.Fatalf("the CPU's own scores: %v", err)
	}
	near := slices.Concat(row(0, 1, 6, -2), row(3, 4, 5, 0), row(6, 5, 1, 0.9))
	if err := labelsAgree(near, cpu, k); err != nil {
		t.Fatalf("a token the CPU can't tell apart went the other way: %v", err)
	}
	off := slices.Concat(row(0, 1, 6, -2), row(4, 3, 5, 0), row(5, 6, 1, 0.9))
	if err := labelsAgree(off, cpu, k); err == nil || !strings.Contains(err.Error(), "token 1") {
		t.Fatalf("a token the CPU is sure of, labelled otherwise: %v", err)
	}
	if err := labelsAgree(cpu[:k], cpu, k); err == nil {
		t.Fatal("fewer scores passed")
	}
}

// Load drops an NPU that fails the check, and says why (with the model's
// files, MAGPIE_GUARD_DIR).
func TestNPUCheckReadsTheProbe(t *testing.T) {
	d := dir(t)
	t.Cleanup(func() { npuRead, openNPU = npuLogits, openLevelZero })
	openNPU = func(string, *net) (*levelzero.Model, bool, error) { return &levelzero.Model{}, true, nil }
	var read []int
	npuRead = func(_ *levelzero.Model, nt *net, ids []int) ([]float32, error) {
		read = ids
		return nt.logits(ids, 1), nil
	}
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.ml != nil {
		t.Skip("CoreML serves this computer, so the NPU is never opened")
	}
	if m.npu == nil || m.NPUErr != nil {
		t.Fatalf("an NPU that gives the CPU's own scores was dropped: %v", m.NPUErr)
	}
	if len(read) < npuMin {
		t.Fatalf("the probe is %d tokens, under npuMin %d: the NPU would never read it", len(read), npuMin)
	}
	// every token's top label moved one along: the check fails, Load
	// reads on the CPU and says why
	npuRead = func(_ *levelzero.Model, nt *net, ids []int) ([]float32, error) {
		got := nt.logits(ids, 1)
		k := nt.labels
		for i := 0; i+k <= len(got); i += k {
			r := got[i : i+k]
			r[(argmax(r)+1)%k] += 100
		}
		return got, nil
	}
	if m, err = Load(d); err != nil {
		t.Fatal(err)
	}
	if m.npu != nil || m.NPUErr == nil || !strings.Contains(m.NPUErr.Error(), "labels a check other") {
		t.Fatalf("an NPU that labels otherwise was kept: npu %v, %v", m.npu != nil, m.NPUErr)
	}
	npuRead = func(*levelzero.Model, *net, []int) ([]float32, error) { return nil, errors.New("device lost") }
	if m, err = Load(d); err != nil {
		t.Fatal(err)
	}
	if m.npu != nil || m.NPUErr == nil || m.NPUErr.Error() != "device lost" {
		t.Fatalf("a failed read: npu %v, %v", m.npu != nil, m.NPUErr)
	}
}
