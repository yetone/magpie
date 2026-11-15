package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// A withdrawn plugin (Antigravity's, owner 11-15) is still installed for
// the users who have it: a move of their own built-in accounts onto it, and
// keepMovedCurrent bringing a moved user's up to its min. Adopt, which signs
// in to it with no account to bring, is a new user's install, and turned
// away.
func TestWithdrawnPluginKeptForItsUsers(t *testing.T) {
	claudeHome(t)
	fakeMover(t)
	const pkg = "@magpie-community/opencode-antigravity-auth"
	movers["fakeco"].pkg, movers["fakeco"].min = pkg, "0.2.1"
	var said []error
	installPlugin = func(ctx context.Context, p, _ string) error {
		err := plugin.RefuseWithdrawn(ctx, p)
		said = append(said, err)
		return err
	}
	oldProviders, oldRemove := pluginProviders, removePlugin
	pluginProviders = func(context.Context) ([]plugin.Provider, error) { return nil, errors.New("stop here") }
	removePlugin = func(context.Context, string) error { return nil }
	t.Cleanup(func() { pluginProviders, removePlugin = oldProviders, oldRemove })
	ctx := context.Background()

	if err := Adopt(ctx, "fakeco"); err == nil || len(said) != 1 || said[0] == nil {
		t.Fatalf("Adopt with no account = %v (install said %v), want it turned away", err, said)
	}

	loginsMu.Lock()
	if err := writeLogins([]savedLogin{fakeLogin("a@fake", "r-a", true, true)}); err != nil {
		loginsMu.Unlock()
		t.Fatal(err)
	}
	loginsMu.Unlock()
	said = nil
	_ = Move(ctx, "fakeco")
	if len(said) != 1 || said[0] != nil {
		t.Fatalf("a move of the user's own account: install said %v, want it allowed", said)
	}

	said = nil
	_ = setMigration("fakeco", func(m *Migration) { m.State = MovePlugin })
	keepMovedCurrent(ctx)
	if len(said) != 1 || said[0] != nil {
		t.Fatalf("keepMovedCurrent: install said %v, want it allowed", said)
	}
}
