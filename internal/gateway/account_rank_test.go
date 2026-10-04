package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// The trace tells each key's place in its provider's own list, as dragged,
// whatever order routing weighed them in, so the live routing view can
// seat them in it (#217).
func TestWeighedRankIsDraggedOrder(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed} {
		p := provider.Provider{ID: "rank" + mode, Name: "Rank", Chat: "https://example.invalid/v1", Key: "first", Keys: []provider.KeyAccount{{Key: "second"}, {Key: "third"}}, Routing: mode}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetAccountOrder(p.ID, []string{provider.KeyID("third"), provider.KeyID("first"), provider.KeyID("second")}); err != nil {
			t.Fatal(err)
		}
		got, err := provider.Find(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, pl := (&Server{}).plan(*got, "model", provider.Chat)
		want := map[string]int{provider.KeyID("third"): 0, provider.KeyID("first"): 1, provider.KeyID("second"): 2}
		if len(pl.order) != 3 {
			t.Fatalf("mode=%q order=%+v", mode, pl.order)
		}
		for _, w := range pl.order {
			id := w.ID[len(p.ID)+1:]
			if w.Rank != want[id] {
				t.Errorf("mode=%q %s rank=%d, want %d", mode, w.Who, w.Rank, want[id])
			}
		}
	}
}
