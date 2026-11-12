package provider

import (
	"os"
	"strings"
	"testing"
)

// StepFun's mark is the five squares in a circle (stepfun.com's favicon,
// its Hugging Face avatar, lobe-icons' StepFun). The old stepped-S with a
// blue gradient must not come back under either file name a provider can
// still carry (zzstar101 on X).
func TestStepFunLogoIsTheCurrentMark(t *testing.T) {
	for _, name := range []string{"stepfun.svg", "stepfun-color.svg"} {
		b, err := os.ReadFile("../gui/assets/icons/" + name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "M12 0C5.372 0 0 5.372 0 12") || strings.Contains(s, "linearGradient") {
			t.Errorf("%s is not StepFun's current mark", name)
		}
	}
	for _, p := range Presets() {
		if strings.HasPrefix(p.ID, "stepfun") && p.Icon != "stepfun" {
			t.Errorf("preset %s icon = %q", p.ID, p.Icon)
		}
	}
}
