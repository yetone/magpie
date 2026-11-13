package update

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files, and
// the attestation API and the site's bundles are an address nothing
// listens on: a test that signs a release must not reach GitHub or
// usemagpie.ai (attest_test.go and attest_site_test.go serve their own).
func TestMain(m *testing.M) {
	attestAPI = "http://127.0.0.1:1"
	attestSite = "http://127.0.0.1:1"
	testenv.Main(m)
}
