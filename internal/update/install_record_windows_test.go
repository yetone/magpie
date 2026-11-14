package update

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// The copy kept uninstalled is read back as written, and uninstalling
// takes it out with the folder chosen, and the key with them.
func TestDeclinedRecord(t *testing.T) {
	installKey = fmt.Sprintf(`Software\magpie-test-%d`, os.Getpid())
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, installKey)
		installKey = `Software\magpie`
	})
	if got := readDeclined(); got != "" {
		t.Fatalf("declined %q with no key", got)
	}
	const kept = `D:\下载\magpie-windows-amd64.exe`
	if err := writeDeclined(kept); err != nil {
		t.Fatal(err)
	}
	if err := writeInstallDir(`D:\Apps\magpie`); err != nil {
		t.Fatal(err)
	}
	if got := readDeclined(); got != kept {
		t.Fatalf("declined %q, want %q", got, kept)
	}
	if err := ForgetInstallDir(); err != nil {
		t.Fatal(err)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, installKey, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Fatal("the key is left after uninstalling")
	}
	// only the copy kept: still all taken out
	if err := writeDeclined(kept); err != nil {
		t.Fatal(err)
	}
	if err := ForgetInstallDir(); err != nil {
		t.Fatal(err)
	}
	if readDeclined() != "" {
		t.Fatal("the copy kept is left after uninstalling")
	}
}
