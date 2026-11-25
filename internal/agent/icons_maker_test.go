package agent

import (
	"os"
	"strings"
	"testing"
)

// Every maker a model's logo is known by is named in the provider
// editor's groups (modelorder.js, WJaunita45604 on X): one added to
// byFamily without a name there would put its models under Other.
func TestEveryModelMakerIsNamedInTheEditor(t *testing.T) {
	b, err := os.ReadFile("../gui/assets/modelorder.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "const MAKERS = {")
	end := strings.Index(src[start:], "};")
	if start < 0 || end < 0 {
		t.Fatal("modelorder.js has no MAKERS")
	}
	makers := src[start : start+end]
	for _, f := range byFamily {
		if !strings.Contains(makers, `"`+f.icon+`":`) && !strings.Contains(makers, " "+f.icon+":") {
			t.Errorf("%s's maker (%s) has no name in modelorder.js's MAKERS", f.prefix, f.icon)
		}
	}
}
