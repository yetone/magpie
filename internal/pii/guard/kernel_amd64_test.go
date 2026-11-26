package guard

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The model finds the same spans with the same scores, to the bit, with
// the AVX2 kernels and with SSE2 alone: a CPU with AVX2 masks a request
// as one without it does.
func TestModelSameWithoutAVX2(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2 on this CPU")
	}
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	m.ml, m.npu = nil, nil
	var b strings.Builder
	for i := 0; b.Len() < 6<<10; i++ {
		fmt.Fprintf(&b, "/home/mrwl/projects/api/store_%d.go:%d: db.QueryRow(\"SELECT email FROM users WHERE id = $1\")\n", i, 40+i)
		fmt.Fprintf(&b, "Author: Wei Zhang <wei.zhang@example.com> postgres://app:S3cr3tPassw0rd%d@db.internal:5432/orders\n", i)
		fmt.Fprintf(&b, "联系人：张三，电话 138%08d，住在北京市海淀区中关村大街%d号。\n", i*7919, i)
	}
	text := b.String()
	with := m.Find(text)
	useAVX2 = false
	pickKernels()
	without := m.Find(text)
	useAVX2 = true
	pickKernels()
	if len(with) == 0 {
		t.Fatal("found nothing")
	}
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("AVX2 found %d spans, SSE2 %d:\n%v\n%v", len(with), len(without), with, without)
	}
}
