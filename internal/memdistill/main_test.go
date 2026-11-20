package memdistill

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own: the conversations listed and the
// gateway's records are read from it, never the user's.
func TestMain(m *testing.M) { testenv.Main(m) }
