package proc

import (
	"fmt"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files.
// With MAGPIE_PROC_RAN set, the test binary stands in for a program a
// probe asks its version (guiexe_windows_test.go): it notes that it was
// started there and answers.
func TestMain(m *testing.M) {
	if ran := os.Getenv("MAGPIE_PROC_RAN"); ran != "" {
		if f, err := os.OpenFile(ran, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, os.Args[0])
			f.Close()
		}
		fmt.Println("2.2.5")
		os.Exit(0)
	}
	testenv.Main(m)
}
