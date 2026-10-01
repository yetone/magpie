package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// printed is what fn printed, and the error it ended with.
func printed(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(r)
		out.Write(b)
		close(done)
	}()
	err = fn()
	w.Close()
	os.Stdout = old
	<-done
	r.Close()
	return out.String(), err
}

// A providers.json that can't be read is not "no providers yet": magpie
// providers and magpie models end in why, and leave the file as it is
// (#415's review).
func TestProvidersCmdSaysUnreadable(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	whole, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	broken := whole[:len(whole)/2]
	if err := os.WriteFile(provider.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() error{
		"providers": providers,
		"models":    func() error { return models(nil) },
	} {
		out, err := printed(t, fn)
		if !errors.Is(err, provider.ErrUnreadable) || !strings.Contains(err.Error(), "left it unchanged") {
			t.Errorf("%s: %v", name, err)
		}
		if strings.Contains(out, "yet") {
			t.Errorf("%s said there are none yet: %q", name, out)
		}
	}
	if after, _ := os.ReadFile(provider.Path()); !bytes.Equal(after, broken) {
		t.Fatal("the broken providers.json was changed")
	}
}
