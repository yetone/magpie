package pii

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real pf-serve and model, where PII_REAL_SERVE names a pf-serve and
// PII_REAL_DIR a folder holding the model under modelName(): both are
// 1.6 GB and more to download, so CI has neither and the test is skipped
// there.
func realServe(t *testing.T) string {
	t.Helper()
	serve, d := os.Getenv("PII_REAL_SERVE"), os.Getenv("PII_REAL_DIR")
	if serve == "" || d == "" {
		t.Skip("PII_REAL_SERVE and PII_REAL_DIR name no real pf-serve and model")
	}
	if fi, err := os.Stat(filepath.Join(d, modelName())); err != nil || fi.Size() != modelSize {
		t.Fatalf("no model of %d bytes at %s: %v", modelSize, filepath.Join(d, modelName()), err)
	}
	old := dir
	dir = func() string { return d }
	t.Cleanup(func() { dir = old })
	return serve
}

// On the CPU pf-serve maps the model's file and computes on its pages,
// rather than reading the 1.6 GB into memory of its own (#853 in
// magpie-community/plugins: 1.82 GB private on Windows). The pages are
// the file's: the system reads them in as they are used and lets them
// go under pressure without writing them anywhere. What the process
// holds of its own is the graph's buffers, a few hundred MB at most.
func TestRealServeOnTheCPUMapsTheModel(t *testing.T) {
	h, err := startOne(realServe(t), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.kill()
	base := "Hi, I'm Jane Doe (jane.doe@example.com, +1 415 555 0100), living at 221B Baker Street, London. "
	got, err := h.classify([]string{strings.Repeat(base, 200)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0]) == 0 || base[got[0][0].Start:got[0][0].End] != "Jane Doe" {
		t.Fatalf("found %v, want Jane Doe first", got[0][:min(len(got[0]), 4)])
	}
	own, ok := privateBytes(h.cmd.Process.Pid)
	if !ok {
		t.Skip("this system's private memory isn't read here")
	}
	t.Logf("pf-serve on the CPU holds %d MB of its own", own>>20)
	if own > modelSize/2 {
		t.Fatalf("pf-serve holds %d MB of its own, the model is %d MB: it was read into memory, not mapped", own>>20, modelSize>>20)
	}
}
