package catalog

import (
	"os"
	"testing"
	"time"
)

// Once nothing has read the loaded catalog for loadIdle it is let go, with
// what was built and indexed of it, and the next read parses the same
// file again. Read just now, it stays.
func TestIdleCatalogLetGoAndReadAgain(t *testing.T) {
	writeCatalog(t, `{"openai":{"models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","name":"GPT-6.1 Sol","cost":{"input":1,"output":2}}}}}`)
	loadKept.off = false
	t.Cleanup(func() { loadKept.off = true })
	listedBefore := len(Provider("openai"))
	if _, ok := PriceOf("openai", "gpt-6.1-sol"); !ok {
		t.Fatal("the catalog wasn't read")
	}
	if dropIdleLoad() {
		t.Fatal("the catalog was let go though read just now")
	}
	drops := loadDrops.Load()
	loadKept.last.Store(time.Now().Add(-2 * loadIdle).UnixNano())
	loadIdleCheck()
	loadMu.Lock()
	held, kept := loaded || mdev != nil, built != nil
	loadMu.Unlock()
	if held || kept || loadDrops.Load() != drops+1 {
		t.Fatalf("idle catalog kept: loaded %v, built %v, %d drops", held, kept, loadDrops.Load()-drops)
	}
	pricedMemo.Lock()
	memo := pricedMemo.by
	pricedMemo.Unlock()
	if memo != nil {
		t.Fatal("idle catalog's prices kept")
	}
	if pr, ok := PriceOf("openai", "gpt-6.1-sol"); !ok || pr.Output != 2 || len(Provider("openai")) != listedBefore {
		t.Fatal("the catalog isn't read again after the let-go")
	}
}

// Seed unpacks the shipped copy only when it is to write it: a newer
// catalog there costs a stat, not 5.7 MB unpacked.
func TestSeedUnpacksNothingOverANewerCatalog(t *testing.T) {
	writeCatalog(t, `{"openai":{"models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol"}}}}`)
	bundledOn = true
	t.Cleanup(func() { bundledOn = false })
	now := time.Now()
	os.Chtimes(CachePath(), now, now)
	n := bundledUnpacks.Load()
	Seed()
	if got := bundledUnpacks.Load() - n; got != 0 {
		t.Fatalf("Seed unpacked the shipped copy %d times over a newer catalog", got)
	}
}
