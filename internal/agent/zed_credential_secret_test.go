//go:build !windows

package agent

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestZedSecretService(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Skip("Secret Service platform")
	}
	for _, tc := range []struct {
		name                           string
		missing                        bool
		prompt                         string
		dismissed, timeout, storeError bool
	}{
		{name: "existing"},
		{name: "new collection", missing: true},
		{name: "collection prompt", missing: true, prompt: "collection"},
		{name: "unlock prompt", prompt: "unlock"},
		{name: "item prompt", prompt: "item"},
		{name: "dismissed", prompt: "unlock", dismissed: true},
		{name: "timeout", prompt: "unlock", timeout: true},
		{name: "write failure", storeError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := zedTestBus(t)
			// No helper executable is available to the credential writer.
			t.Setenv("PATH", t.TempDir())
			const service = "org.freedesktop.Secret.Service"
			const root dbus.ObjectPath = "/org/freedesktop/secrets"
			const collection dbus.ObjectPath = "/collection/custom"
			const session dbus.ObjectPath = "/session/42"
			const prompt dbus.ObjectPath = "/prompt/7"
			const url = "http://127.0.0.1:7654/v1"
			calls := make(chan string, 16)
			export := func(path dbus.ObjectPath, iface string, methods map[string]any) {
				if err := conn.ExportMethodTable(methods, path, iface); err != nil {
					t.Fatal(err)
				}
			}
			export(root, service, map[string]any{
				"OpenSession": func(algorithm string, input dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
					if algorithm != "plain" || input.Value() != "" {
						t.Errorf("session negotiation: %s %v", algorithm, input)
					}
					return dbus.MakeVariant(""), session, nil
				},
				"ReadAlias": func(alias string) (dbus.ObjectPath, *dbus.Error) {
					if alias != "default" {
						t.Errorf("alias: %s", alias)
					}
					if tc.missing {
						return "/", nil
					}
					return collection, nil
				},
				"CreateCollection": func(properties map[string]dbus.Variant, alias string) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
					calls <- "collection"
					if alias != "default" || properties["org.freedesktop.Secret.Collection.Label"].Value() != "Default" {
						t.Errorf("new collection: %s %v", alias, properties)
					}
					if tc.prompt == "collection" {
						return "/", prompt, nil
					}
					return collection, "/", nil
				},
				"Unlock": func(paths []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
					if !reflect.DeepEqual(paths, []dbus.ObjectPath{collection}) {
						t.Errorf("unlock paths: %v", paths)
					}
					if tc.prompt == "unlock" {
						return []dbus.ObjectPath{}, prompt, nil
					}
					return paths, "/", nil
				},
			})
			export(session, "org.freedesktop.Secret.Session", map[string]any{
				"Close": func() *dbus.Error { calls <- "close"; return nil },
			})
			export(collection, "org.freedesktop.Secret.Collection", map[string]any{
				"CreateItem": func(properties map[string]dbus.Variant, secret struct {
					Session     dbus.ObjectPath
					Parameters  []byte
					Value       []byte
					ContentType string
				}, replace bool) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
					calls <- "item"
					want := map[string]string{"url": url, "username": "Bearer"}
					if properties["org.freedesktop.Secret.Item.Label"].Value() != "zed-github-account" ||
						!reflect.DeepEqual(properties["org.freedesktop.Secret.Item.Attributes"].Value(), want) || !replace {
						t.Errorf("item properties: %v, replace %v", properties, replace)
					}
					if secret.Session != session || len(secret.Parameters) != 0 || string(secret.Value) != "magpie-zed" || secret.ContentType != "text/plain; charset=utf8" {
						t.Errorf("incorrect Secret wire value: %+v", secret)
					}
					if tc.storeError {
						return "/", "/", dbus.MakeFailedError(errors.New("store refused"))
					}
					if tc.prompt == "item" {
						return "/", prompt, nil
					}
					return "/item/9", "/", nil
				},
			})
			export(prompt, "org.freedesktop.Secret.Prompt", map[string]any{
				"Prompt": func(window string) *dbus.Error {
					calls <- "prompt"
					if window != "" {
						t.Errorf("window: %s", window)
					}
					if tc.timeout {
						return nil
					}
					result := dbus.MakeVariant([]dbus.ObjectPath{collection})
					if tc.prompt == "collection" {
						result = dbus.MakeVariant(collection)
					} else if tc.prompt == "item" {
						result = dbus.MakeVariant(dbus.ObjectPath("/item/9"))
					}
					// Emit before returning: subscribing after Prompt loses this signal.
					if err := conn.Emit(prompt, "org.freedesktop.Secret.Prompt.Completed", tc.dismissed, result); err != nil {
						return dbus.MakeFailedError(err)
					}
					return nil
				},
				"Dismiss": func() *dbus.Error { calls <- "dismiss"; return nil },
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if tc.timeout {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
			}
			var err error
			if tc.timeout {
				err = saveZedSecret(ctx, url)
			} else {
				err = saveZedCredential(url)
			}
			switch {
			case tc.timeout:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout: %v", err)
				}
			case tc.dismissed:
				if err == nil || !strings.Contains(err.Error(), "dismissed") {
					t.Fatalf("dismissed: %v", err)
				}
			case tc.storeError:
				if err == nil || !strings.Contains(err.Error(), "store refused") {
					t.Fatalf("write failure: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			seen := map[string]bool{}
			for len(calls) > 0 {
				seen[<-calls] = true
			}
			if seen["collection"] != tc.missing || seen["prompt"] != (tc.prompt != "") || seen["dismiss"] != tc.timeout || seen["item"] != (!tc.dismissed && !tc.timeout) || (!tc.timeout && !seen["close"]) {
				t.Fatalf("unexpected operations: %v", seen)
			}
		})
	}
}

func zedTestBus(t *testing.T) *dbus.Conn {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for isolated Secret Service tests")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("no private bus address: %v", scanner.Err())
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", scanner.Text())
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if reply, err := conn.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claim Secret Service: %v %v", reply, err)
	}
	return conn
}
