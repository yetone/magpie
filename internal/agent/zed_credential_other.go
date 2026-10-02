//go:build !windows

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

func saveZedCredential(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "security"
		app, err := zedAppPath(ctx)
		if err != nil {
			return err
		}
		// GPUI uses an Internet Password whose server is the full API URL.
		// Keep security trusted as well so it can update the key on later picks.
		args = []string{"add-internet-password", "-U", "-T", app, "-T", "/usr/bin/security", "-s", url, "-a", "Bearer", "-w", gateway.TokenFor("zed")}
	case "linux", "freebsd":
		return saveZedSecret(ctx, url)
	default:
		return fmt.Errorf("unsupported system credential store on %s", runtime.GOOS)
	}
	cmd := proc.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Trust the installed Zed bundle, including a CLI symlink to a custom location.
func zedAppPath(ctx context.Context) (string, error) {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, "Applications", "Zed.app"), filepath.Join(home, "Applications", "Zed Preview.app"),
		"/Applications/Zed.app", "/Applications/Zed Preview.app",
	}
	if bin, err := exec.LookPath("zed"); err == nil {
		if bin, err = filepath.EvalSymlinks(bin); err == nil {
			for dir := filepath.Dir(bin); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				if strings.HasSuffix(dir, ".app") {
					paths = append([]string{dir}, paths...)
					break
				}
			}
		}
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path, nil
		}
	}
	// Spotlight finds bundles moved outside the usual Applications folders.
	out, err := proc.CommandContext(ctx, "mdfind", "kMDItemCFBundleIdentifier == 'dev.zed.Zed' || kMDItemCFBundleIdentifier == 'dev.zed.Zed-Preview'").Output()
	if err == nil {
		for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if info, err := os.Stat(path); err == nil && info.IsDir() && strings.HasSuffix(path, ".app") {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("cannot find Zed.app to authorize access to its gateway credential")
}

const secretService = "org.freedesktop.Secret.Service"

// The field order is the Secret Service D-Bus signature (oayays).
type zedSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func saveZedSecret(ctx context.Context, url string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("Secret Service session bus: %w", err)
	}
	defer conn.Close()
	service := conn.Object("org.freedesktop.secrets", "/org/freedesktop/secrets")
	var output dbus.Variant
	var session dbus.ObjectPath
	if err := service.CallWithContext(ctx, secretService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session); err != nil {
		return err
	}
	defer conn.Object("org.freedesktop.secrets", session).CallWithContext(ctx, "org.freedesktop.Secret.Session.Close", 0)
	var collection, prompt dbus.ObjectPath
	if err := service.CallWithContext(ctx, secretService+".ReadAlias", 0, "default").Store(&collection); err != nil {
		return err
	}
	if collection == "/" {
		properties := map[string]dbus.Variant{"org.freedesktop.Secret.Collection.Label": dbus.MakeVariant("Default")}
		if err := service.CallWithContext(ctx, secretService+".CreateCollection", 0, properties, "default").Store(&collection, &prompt); err != nil {
			return err
		}
		if prompt != "/" {
			result, err := zedSecretPrompt(ctx, conn, prompt)
			if err != nil {
				return err
			}
			if err := result.Store(&collection); err != nil {
				return err
			}
		}
	}
	var unlocked []dbus.ObjectPath
	if err := service.CallWithContext(ctx, secretService+".Unlock", 0, []dbus.ObjectPath{collection}).Store(&unlocked, &prompt); err != nil {
		return err
	}
	if _, err := zedSecretPrompt(ctx, conn, prompt); err != nil {
		return err
	}
	// GPUI finds credentials by URL, username and this historical label.
	properties := map[string]dbus.Variant{
		"org.freedesktop.Secret.Item.Label": dbus.MakeVariant("zed-github-account"),
		"org.freedesktop.Secret.Item.Attributes": dbus.MakeVariant(map[string]string{
			"url": url, "username": "Bearer",
		}),
	}
	secret := zedSecret{Session: session, Parameters: []byte{}, Value: []byte(gateway.TokenFor("zed")), ContentType: "text/plain; charset=utf8"}
	var item dbus.ObjectPath
	if err := conn.Object("org.freedesktop.secrets", collection).CallWithContext(ctx, "org.freedesktop.Secret.Collection.CreateItem", 0, properties, secret, true).Store(&item, &prompt); err != nil {
		return err
	}
	_, err = zedSecretPrompt(ctx, conn, prompt)
	return err
}

func zedSecretPrompt(ctx context.Context, conn *dbus.Conn, path dbus.ObjectPath) (dbus.Variant, error) {
	if path == "/" {
		return dbus.Variant{}, nil
	}
	const iface = "org.freedesktop.Secret.Prompt"
	match := []dbus.MatchOption{dbus.WithMatchSender("org.freedesktop.secrets"), dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(iface), dbus.WithMatchMember("Completed")}
	if err := conn.AddMatchSignalContext(ctx, match...); err != nil {
		return dbus.Variant{}, err
	}
	defer conn.RemoveMatchSignalContext(ctx, match...)
	signals := make(chan *dbus.Signal, 1)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	object := conn.Object("org.freedesktop.secrets", path)
	if err := object.CallWithContext(ctx, iface+".Prompt", 0, "").Err; err != nil {
		return dbus.Variant{}, err
	}
	for {
		select {
		case signal, ok := <-signals:
			if !ok {
				return dbus.Variant{}, fmt.Errorf("Secret Service connection closed")
			}
			if signal.Path != path || signal.Name != iface+".Completed" {
				continue
			}
			var dismissed bool
			var result dbus.Variant
			if err := dbus.Store(signal.Body, &dismissed, &result); err != nil {
				return dbus.Variant{}, err
			}
			if dismissed {
				return dbus.Variant{}, fmt.Errorf("Secret Service prompt dismissed")
			}
			return result, nil
		case <-ctx.Done():
			// Dismiss with a fresh deadline after the operation timed out.
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			object.CallWithContext(cleanup, iface+".Dismiss", 0)
			return dbus.Variant{}, ctx.Err()
		}
	}
}
