package cli

import (
	"errors"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// sungt (10-29): with nothing here to open Cindy's links, `magpie cindy
// add` said "✓ Cindy opened" while nothing opened. It says why, and hands
// nothing to the browser.
func TestImportAddSaysNothingOpensIt(t *testing.T) {
	groupsHome(t)
	var handed []string
	old := openInBrowser
	openInBrowser = func(u string) { handed = append(handed, u) }
	t.Cleanup(func() { openInBrowser = old })
	oldLink := appLink
	t.Cleanup(func() { appLink = oldLink })
	for _, id := range []string{"cindy", "aionui", "cherrystudio"} {
		appLink = func(string, func(string) error) error {
			return &agent.NoLinkHandlerError{App: id, Scheme: id}
		}
		var nh *agent.NoLinkHandlerError
		if err := run([]string{id, "add"}); !errors.As(err, &nh) {
			t.Errorf("%s add: %v", id, err)
		}
		appLink = func(link string, open func(string) error) error { return open(link) }
		if err := run([]string{id, "add"}); err != nil {
			t.Errorf("%s add with a handler: %v", id, err)
		}
	}
	if len(handed) != 3 {
		t.Fatalf("handed to the browser: %q", handed)
	}
}
