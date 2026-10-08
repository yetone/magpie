package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestRegisterScheme(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flatpak  bool
		existing bool
	}{
		{"flatpak without entry", true, false},
		{"flatpak with entry", true, true},
		{"native without entry", false, false},
		{"native with entry", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FLATPAK_ID", "")
			if tc.flatpak {
				t.Setenv("FLATPAK_ID", "ai.usemagpie.Magpie")
			}
			root := t.TempDir()
			data := filepath.Join(root, "data")
			t.Setenv("XDG_DATA_HOME", data)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			t.Setenv("MAGPIE_SCHEME_TEST_CALLS", calls)
			t.Setenv("PATH", bin)
			for _, name := range []string{"update-desktop-database", "xdg-mime"} {
				script := "#!/bin/sh\nprintf '%s\\n' '" + name + "' \"$@\" >> \"$MAGPIE_SCHEME_TEST_CALLS\"\n"
				testenv.Program(t, filepath.Join(bin, name), script)
			}
			dir := filepath.Join(data, "applications")
			path := filepath.Join(dir, "magpie.desktop")
			const old = "[Desktop Entry]\nExec=/usr/bin/magpie %u\nMimeType=x-scheme-handler/magpie;\n"
			if tc.existing {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := registerScheme(); err != nil {
				t.Fatal(err)
			}
			if tc.flatpak {
				if tc.existing {
					if got, err := os.ReadFile(path); err != nil || string(got) != old {
						t.Fatalf("existing desktop entry changed: %q, %v", got, err)
					}
				} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("applications directory was created: %v", err)
				}
				if got, err := os.ReadFile(calls); !os.IsNotExist(err) {
					t.Fatalf("desktop registration ran: %q, %v", got, err)
				}
				return
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(got), "Exec="+exe+" %u\n") || !strings.Contains(string(got), "MimeType=x-scheme-handler/magpie;\n") {
				t.Fatalf("desktop entry: %q, %v", got, err)
			}
			// An entry for this executable is already registered on the next run.
			if err := registerScheme(); err != nil {
				t.Fatal(err)
			}
			want := "update-desktop-database\n" + dir + "\nxdg-mime\ndefault\nmagpie.desktop\nx-scheme-handler/magpie\n"
			if got, err := os.ReadFile(calls); err != nil || string(got) != want {
				t.Fatalf("registration calls: %q, want %q (%v)", got, want, err)
			}
		})
	}
}
