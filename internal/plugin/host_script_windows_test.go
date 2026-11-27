package plugin

import (
	"bytes"
	"os"
	"syscall"
	"testing"

	"github.com/yetone/magpie/internal/steady"
)

func TestHostScriptDestinationHeld(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		ok   bool
	}{
		{"matching", hostJS, true},
		{"different", []byte("incomplete script"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			p, err := hostScript("host", hostJS)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}

			// The other instance publishes its copy after our cache miss,
			// then holds it without sharing delete, as Windows readers can.
			syncFile := steady.Sync
			t.Cleanup(func() { steady.Sync = syncFile })
			held := false
			steady.Sync = func(f *os.File) error {
				if err := os.WriteFile(p, tc.body, 0o644); err != nil {
					return err
				}
				name, err := syscall.UTF16PtrFromString(p)
				if err != nil {
					return err
				}
				h, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, 0, 0)
				if err != nil {
					return err
				}
				t.Cleanup(func() { syscall.CloseHandle(h) })
				held = true
				return syncFile(f)
			}
			got, err := hostScript("host", hostJS)
			if !held {
				t.Fatalf("destination was never held open during the write: %v", err)
			}
			if tc.ok && (err != nil || got != p) || !tc.ok && err == nil {
				t.Fatalf("write with destination held: got %q, %v, want success %t", got, err, tc.ok)
			}
			if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, tc.body) {
				t.Fatalf("held script changed: got %d bytes, want %d, error %v", len(b), len(tc.body), err)
			}
			assertOnlyHostScript(t, p)
		})
	}
}
