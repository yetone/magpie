package update

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files, and
// the attestation API is an address nothing listens on: a test that signs a
// release must not reach GitHub (attest_test.go serves its own).
func TestMain(m *testing.M) {
	attestAPI = "http://127.0.0.1:1"
	testenv.Main(m)
}
