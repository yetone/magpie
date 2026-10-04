package gui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

func TestPurposeAssetMatchesUsage(t *testing.T) {
	b, err := assets.ReadFile("assets/purposes.js")
	if err != nil {
		t.Fatal(err)
	}
	_, data, ok := strings.Cut(string(b), "const REQUEST_KINDS = ")
	var kinds map[string]usage.PurposeKind
	if !ok || json.Unmarshal([]byte(strings.TrimSuffix(data, ";\n")), &kinds) != nil || !reflect.DeepEqual(kinds, usage.PurposeKinds()) {
		t.Fatal("the browser's purpose catalog is stale: run go generate ./internal/usage")
	}
}
