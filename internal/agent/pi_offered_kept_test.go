package agent

import (
	"os"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Pi's Values asks piOffered on every Drift of the window's poll: it is
// built once while provider.StampNow stays the same, and again after a
// change, which reaches the answer.
func TestPiOfferedKeptPerStamp(t *testing.T) {
	syncHome(t)
	thinks := func(reasoning string) {
		t.Helper()
		if err := os.WriteFile(catalog.CachePath(), []byte(`{
		  "moonshotai": {"models": {"kimi-k3-max": {"id":"kimi-k3-max","name":"Kimi-K3-Max","reasoning":`+reasoning+`,"limit":{"context":262144}}}}
		}`), 0o644); err != nil {
			t.Fatal(err)
		}
		catalog.Reset()
	}
	thinks("true")
	if err := provider.Save(provider.Provider{ID: "trae-cn", Name: "Trae CN", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Catalog: "moonshotai", Models: []string{"kimi-k3-max"}}); err != nil {
		t.Fatal(err)
	}
	const model = "magpie/trae-cn/kimi-k3-max"
	built := func() int64 {
		builtOffered.Lock()
		defer builtOffered.Unlock()
		return builtOffered.m["pi\x00trae-cn/kimi-k3-max"].at.UnixNano()
	}
	want := []string{"off", "minimal", "low", "medium", "high"}
	if got := piOffered("pi", model); !reflect.DeepEqual(got, want) {
		t.Fatalf("Pi offers %v, want %v", got, want)
	}
	first := built()
	for range 5 {
		piOffered("pi", model)
	}
	if built() != first {
		t.Fatal("built again while nothing changed")
	}
	if got := piOffered("pi", "magpie/trae-cn/none"); got != nil {
		t.Fatalf("a model magpie doesn't have: %v", got)
	}
	thinks("false")
	if got := piOffered("pi", model); !reflect.DeepEqual(got, []string{"off"}) {
		t.Fatalf("after the catalog said it doesn't think, Pi offers %v, want off alone", got)
	}
}
