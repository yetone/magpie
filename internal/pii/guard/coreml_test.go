package guard

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/pii/guard/coreml"
)

// half rounds as numpy's astype(float16) does, which made the sha256 in
// coreml.json: to the nearest, a tie to the even one.
func TestHalfRounds(t *testing.T) {
	for _, c := range []struct {
		f float32
		h uint16
	}{
		{1, 0x3c00}, {-2, 0xc000}, {65504, 0x7bff}, {65520, 0x7c00},
		{float32(math.Ldexp(1, -24)), 0x0001}, {float32(math.Ldexp(1, -25)), 0},
		{float32(math.Ldexp(3, -25)), 0x0002}, {float32(math.Ldexp(1, -14)), 0x0400},
		{1 + float32(math.Ldexp(1, -11)), 0x3c00}, {1 + float32(math.Ldexp(3, -11)), 0x3c02},
		{float32(math.Copysign(0, -1)), 0x8000}, {float32(math.NaN()), 0x7e00},
		{float32(math.Inf(-1)), 0xfc00},
	} {
		if got := half(c.f); got != c.h {
			t.Errorf("half(%g) = %#04x, want %#04x", c.f, got, c.h)
		}
	}
}

// The weights written from the model are byte for byte the ones
// coremltools wrote from it (coremlWeights checks the sha256).
func TestCoreMLWeights(t *testing.T) {
	nt, err := loadNet(dir(t) + "/model_int8.onnx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coremlWeights(nt); err != nil {
		t.Fatal(err)
	}
}

// On a Mac, CoreML labels a long window as bert.go does, but for a token
// in a hundred, and finds the same spans, their scores within .2 (each is
// up to .13 from the publisher's float32 model, on either side). Load
// compiles the program into MAGPIE_GUARD_DIR/coreml the first time.
func TestCoreMLMatchesGo(t *testing.T) {
	d := dir(t)
	if !coreml.Available {
		t.Skip("no CoreML here")
	}
	t0 := time.Now()
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.ml == nil {
		t.Fatalf("CoreML didn't open: %v", m.MLErr)
	}
	t.Logf("loaded in %v", time.Since(t0))
	text := strings.Repeat("联系人：张三，电话 13800138000，邮箱 zhangsan@example.com。The host 10.20.30.40 logs in as deploy with key sk-proj-AbCdEf0123456789xyz.\n", 12)
	toks := m.tok.encode(text)
	ids := []int{0}
	for _, k := range toks[:min(window, len(toks))] {
		ids = append(ids, k.id)
	}
	ids = append(ids, 2)
	want := m.net.logits(ids, Cores())
	t1 := time.Now()
	got, err := coremlLogits(m.ml, m.net, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d tokens: CoreML %v", len(ids), time.Since(t1))
	k, same := len(labels), 0
	for i := range ids {
		if argmax(got[i*k:(i+1)*k]) == argmax(want[i*k:(i+1)*k]) {
			same++
		}
	}
	if same*100 < len(ids)*99 {
		t.Fatalf("CoreML labels %d of %d tokens as bert.go does", same, len(ids))
	}
	goModel := &Model{tok: m.tok, net: m.net}
	a, b := m.Find(text), goModel.Find(text)
	if len(a) != len(b) {
		t.Fatalf("CoreML finds %d spans, bert.go %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Start != b[i].Start || a[i].End != b[i].End || a[i].Label != b[i].Label || math.Abs(a[i].Score-b[i].Score) > .2 {
			t.Errorf("CoreML %v, bert.go %v", a[i], b[i])
		}
	}
}

// When the program can't be made (here, weights that aren't the ones it
// was made from), the model loads and bert.go reads, and the program
// compiled before is left.
func TestCoreMLFailureFallsBack(t *testing.T) {
	d := dir(t)
	if !coreml.Available {
		t.Skip("no CoreML here")
	}
	before, _ := os.ReadDir(filepath.Join(d, "coreml"))
	saved := coremlLayout
	t.Cleanup(func() { coremlLayout = saved })
	coremlLayout = bytes.Replace(saved, []byte(`"sha256": "`), []byte(`"sha256": "0`), 1)
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.ml != nil || m.MLErr == nil {
		t.Fatalf("CoreML opened: %v", m.MLErr)
	}
	text := strings.Repeat("联系人：张三，电话 13800138000。", 8)
	if got := m.Find(text); len(got) == 0 {
		t.Fatal("bert.go found nothing")
	}
	after, _ := os.ReadDir(filepath.Join(d, "coreml"))
	if len(after) != len(before) {
		t.Fatalf("the coreml folder went from %d to %d", len(before), len(after))
	}
}

// Backend names what reads the model: CoreML on the device its plan puts
// most of the program on while CoreML reads, and the CPU with why once
// CoreML's last read failed or it couldn't be made (Settings said "on
// the CPU" while CoreML read every window: yyhhyyyyyy on Discord).
func TestBackend(t *testing.T) {
	made := errors.New("no CoreML program")
	via, d, err := (&Model{MLErr: made}).Backend()
	if via != "" || d != "CPU" || (coreml.Available && err != made) || (!coreml.Available && err != nil) {
		t.Fatalf("without CoreML: %q %q %v", via, d, err)
	}
	m := &Model{ml: &coreml.Model{Device: "Neural Engine"}}
	if via, d, err := m.Backend(); via != "CoreML" || d != "Neural Engine" || err != nil {
		t.Fatalf("with CoreML: %q %q %v", via, d, err)
	}
	e := "prediction failed"
	m.mlFail.Store(&e)
	if via, d, err := m.Backend(); via != "" || d != "CPU" || err == nil || err.Error() != e {
		t.Fatalf("after a failed read: %q %q %v", via, d, err)
	}
	m.mlFail.Store(nil)
	if via, _, _ := m.Backend(); via != "CoreML" {
		t.Fatalf("after a good read: %q", via)
	}
}

// A failed CoreML read is what Backend says until one succeeds: logits
// records it as bert.go reads the window instead.
func TestFailedCoreMLReadIsSaid(t *testing.T) {
	d := dir(t)
	if !coreml.Available {
		t.Skip("no CoreML here")
	}
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.ml == nil {
		t.Fatalf("CoreML didn't open: %v", m.MLErr)
	}
	ids := make([]int, 40)
	for i := range ids {
		ids[i] = 5
	}
	ids[0], ids[len(ids)-1] = 0, 2
	m.logits(ids, 1)
	if via, _, err := m.Backend(); via != "CoreML" || err != nil {
		t.Fatalf("after a good read: %q %v", via, err)
	}
	t.Cleanup(func() { coremlRead = coremlLogits })
	coremlRead = func(*coreml.Model, *net, []int) ([]float32, error) { return nil, errors.New("prediction failed") }
	if lg := m.logits(ids, 1); len(lg) != len(ids)*len(labels) {
		t.Fatalf("bert.go read %d logits", len(lg))
	}
	if via, d, err := m.Backend(); via != "" || d != "CPU" || err == nil || err.Error() != "prediction failed" {
		t.Fatalf("after a failed read: %q %q %v", via, d, err)
	}
	coremlRead = coremlLogits
	m.logits(ids, 1)
	if via, _, err := m.Backend(); via != "CoreML" || err != nil {
		t.Fatalf("after CoreML read again: %q %v", via, err)
	}
}

// CoreML's compute plan names where it runs the program: on a Mac with
// macOS 14.4 or later, a device for every operation it places, shares
// adding to one, and the loaded model's Device the largest of them.
func TestCoreMLPlanNamesADevice(t *testing.T) {
	d := dir(t)
	if !coreml.Available {
		t.Skip("no CoreML here")
	}
	m, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if m.ml == nil {
		t.Fatalf("CoreML didn't open: %v", m.MLErr)
	}
	es, _ := filepath.Glob(filepath.Join(d, "coreml", "*.mlmodelc"))
	if len(es) != 1 {
		t.Fatalf("compiled programs: %v", es)
	}
	sh, err := coreml.Plan(es[0])
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, s := range sh {
		sum += s
	}
	t.Logf("plan: Neural Engine %.3f, GPU %.3f, CPU %.3f; Device %q", sh[0], sh[1], sh[2], m.ml.Device)
	if math.Abs(sum-1) > 1e-6 || m.ml.Device != coreml.Most(sh) || m.ml.Device == "" || m.ml.DeviceErr != nil {
		t.Fatalf("shares %v (sum %v), Device %q, %v", sh, sum, m.ml.Device, m.ml.DeviceErr)
	}
	if via, dev, err := m.Backend(); via != "CoreML" || dev != m.ml.Device || err != nil {
		t.Fatalf("Backend: %q %q %v", via, dev, err)
	}
}
