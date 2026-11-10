package gui

import (
	"mime"
	"sync"
	"testing"
)

// Handler sets the page's own types even where the system's table says
// otherwise (Windows' registry, which another program may have changed).
func TestAssetTypesHoldAgainstTheRegistry(t *testing.T) {
	for ext := range assetTypes {
		mime.AddExtensionType(ext, "application/x-other")
	}
	was := addAssetTypes
	addAssetTypes = sync.OnceFunc(registerAssetTypes)
	t.Cleanup(func() { addAssetTypes = was; registerAssetTypes() })
	Handler(nil, nil)
	for ext, want := range assetTypes {
		if got := mime.TypeByExtension(ext); got != want {
			t.Errorf("%s served as %q, not %q", ext, got, want)
		}
	}
}
