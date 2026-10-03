//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"golang.org/x/sys/windows"
)

func TestReasonixDesktopRequiresMajorVersionTwo(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		want          bool
	}{
		{"Reasonix Studio", "1.0.3", false},
		{"Reasonix Studio", "", false},
		{"Reasonix Studio 2.24.0", "2.24.0", true},
		{"Reasonix Studio", "v2.24.0", true},
		{"Reasonix Studio Host", "1.18.0", false},
		{"Other app", "2.24.0", false},
	} {
		if got := reasonixStudioRelease(tc.name, tc.version); got != tc.want {
			t.Errorf("%q version %q detected=%v", tc.name, tc.version, got)
		}
	}
}

func TestReasonixLockedCredentialRollsBackAllFiles(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "select", true: "restore"}[restore], func(t *testing.T) {
			a, env := reasonixFixture(t, reasonixNativeConfig)
			if restore {
				if err := a.Field("model").Set("magpie/a/pro"); err != nil {
					t.Fatal(err)
				}
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(provider.Path()), "reasonix-*.json"))
			before := map[string]string{}
			for _, path := range append(files, a.Path, env) {
				b, _ := os.ReadFile(path)
				before[path] = string(b)
			}
			name, _ := windows.UTF16PtrFromString(env)
			h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			value := "magpie/a/pro"
			if restore {
				value = ""
			}
			if err := a.Field("model").Set(value); err == nil {
				t.Fatal("writing a locked credential file unexpectedly succeeded")
			}
			for path, original := range before {
				got, _ := os.ReadFile(path)
				if string(got) != original {
					t.Fatalf("failed transaction changed %s", path)
				}
			}
			after, _ := filepath.Glob(filepath.Join(filepath.Dir(provider.Path()), "reasonix-*.json"))
			if len(after) != len(files) {
				t.Fatal("failed transaction changed restore records")
			}
		})
	}
}
