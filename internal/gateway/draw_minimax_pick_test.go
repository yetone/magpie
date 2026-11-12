package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// netube99 on X: a MiniMax key kept for its image models (Select none, or
// a few chat picks) refused minimax-cn/MiniMax-H3-Image in Settings → Images
// with "no model … to generate images".
func TestMiniMaxImageModelPickable(t *testing.T) {
	for _, preset := range []string{"minimax-cn", "minimax"} {
		for _, state := range []string{"all", "none", "picks"} {
			t.Run(preset+"/"+state, func(t *testing.T) {
				s, _, p := minimaxDrawing(t, preset)
				switch state {
				case "none":
					p.NoModels = true
				case "picks":
					p.Models = []string{"MiniMax-M2.7"}
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				if state == "none" {
					code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"`+preset+`/MiniMax-H3-Image","prompt":"a cat"}`)
					if code != 200 || len(a.Data) != 1 {
						t.Fatalf("drew %d %s", code, raw)
					}
				}
				for _, m := range []string{"image-01", "image-01-live", "MiniMax-H3-Image", "MiniMax-H3-Image-Highspeed"} {
					ref := preset + "/" + m
					if got := DrawerMissingOf(ref); got != "" {
						t.Errorf("%s is missing", got)
					}
					if _, id, ok := resolveDrawing(ref); !ok || id != m {
						t.Errorf("%s doesn't resolve for drawing: %q %v", ref, id, ok)
					}
				}
			})
		}
	}
}
