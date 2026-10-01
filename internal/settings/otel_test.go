package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestOTelSettingsFilePermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.Chmod(Path(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := Save(Settings{OTel: OTel{Headers: map[string]string{"Authorization": "Basic test-secret"}}}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(Path())
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("existing=%v: settings mode %o, want 600", existing, info.Mode().Perm())
		}
		if Load().OTel.Headers["Authorization"] != "Basic test-secret" {
			t.Fatal("credentials did not persist")
		}
	}
}

func TestOTelSettingsPreservesReadOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	for _, mode := range []os.FileMode{0o400, 0o444} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
			if err := Save(Settings{OTel: OTel{Headers: map[string]string{"Authorization": "Basic original"}}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(Path(), mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(Path(), 0o600) })
			f, err := os.OpenFile(Path(), os.O_WRONLY, 0)
			writable := err == nil
			if writable {
				f.Close()
			}
			err = Save(Settings{OTel: OTel{Headers: map[string]string{"Authorization": "Basic changed"}}})
			if !writable {
				if !os.IsPermission(err) {
					t.Fatalf("save to read-only settings: %v", err)
				}
				after, err := os.ReadFile(Path())
				if err != nil || string(after) != string(before) {
					t.Fatalf("read-only settings changed: %v", err)
				}
			}
			info, err := os.Stat(Path())
			if err != nil || info.Mode().Perm() != 0o400 {
				t.Fatalf("owner read-only permissions changed: %v, %v", info, err)
			}
		})
	}
}

func TestOTelSettingsAndOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	for _, k := range []string{"MAGPIE_OTEL_ENABLED", "MAGPIE_OTEL_ENDPOINT", "MAGPIE_OTEL_HEADERS", "MAGPIE_OTEL_METRICS"} {
		t.Setenv(k, "")
	}
	// Empty overrides are explicit; a malformed enabled value fails closed.
	if _, err := OTelExport(); err == nil {
		t.Fatal("empty enabled override accepted")
	}
	t.Setenv("MAGPIE_OTEL_ENABLED", "false")
	t.Setenv("MAGPIE_OTEL_METRICS", "false")
	t.Setenv("MAGPIE_OTEL_ENDPOINT", "http://localhost:4318")
	if o, err := OTelExport(); err != nil || o.Enabled {
		t.Fatalf("endpoint enabled export: %+v %v", o, err)
	}
	t.Setenv("MAGPIE_OTEL_ENABLED", "true")
	t.Setenv("MAGPIE_OTEL_METRICS", "true")
	t.Setenv("MAGPIE_OTEL_HEADERS", "Authorization=Basic%20YWJjZA==,X-Tag=a%2Cb%2Bc")
	o, err := OTelExport()
	if err != nil || !o.Enabled || !o.Metrics || o.Headers["Authorization"] != "Basic YWJjZA==" || o.Headers["X-Tag"] != "a,b+c" {
		t.Fatalf("%+v %v", o, err)
	}
	if err := Save(Settings{OTel: OTel{Endpoint: "https://collector.test/", Headers: map[string]string{"Authorization": "Bearer test"}}}); err != nil {
		t.Fatal(err)
	}
	if got := Load().OTel; got.Enabled || got.Endpoint != "https://collector.test" || !reflect.DeepEqual(got.Headers, map[string]string{"Authorization": "Bearer test"}) {
		t.Fatalf("saved: %+v", got)
	}
	if Load().OTel.Metrics {
		t.Fatal("environment override persisted")
	}
}

func TestOTelSettingsRejectInvalid(t *testing.T) {
	for _, o := range []OTel{
		{Enabled: true}, {Endpoint: "file:///tmp/data"}, {Endpoint: "https://user:secret@collector.test"}, {Endpoint: "http://collector.test?key=secret"}, {Endpoint: "http://collector.test#fragment"},
		{Headers: map[string]string{"bad name": "x"}}, {Headers: map[string]string{"Authorization": "Bearer test\r\nX-Other: injected"}}, {Headers: map[string]string{"Content-Type": "text/plain"}},
	} {
		if o.Check() == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}
