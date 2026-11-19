package netproxy

import (
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

var regOverridePredefKey = syscall.NewLazyDLL("advapi32.dll").NewProc("RegOverridePredefKey")

// hkcu is HKEY_CURRENT_USER as the C headers have it, sign-extended
// ((HKEY)(ULONG_PTR)(LONG)0x80000001): RegOverridePredefKey takes no
// other form.
var hkcuLong int32 = -0x7fffffff

var hkcu = uintptr(int(hkcuLong))

// internetSettings points this process's HKEY_CURRENT_USER at a key of
// its own (RegOverridePredefKey) and writes Internet Options' proxy
// there, so system reads the real registry the way it does on a user's
// computer, and the test box's own proxy settings are never touched.
func internetSettings(t *testing.T, vals map[string]any) {
	t.Helper()
	root := fmt.Sprintf(`Software\magpie-test-%d`, time.Now().UnixNano())
	base, _, err := registry.CreateKey(registry.CURRENT_USER, root, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _ := regOverridePredefKey.Call(hkcu, uintptr(base)); r != 0 {
		base.Close()
		registry.DeleteKey(registry.CURRENT_USER, root)
		t.Fatalf("RegOverridePredefKey: %d", r)
	}
	path := []string{"Software", "Microsoft", "Windows", "CurrentVersion", "Internet Settings"}
	t.Cleanup(func() {
		regOverridePredefKey.Call(hkcu, 0)
		base.Close()
		for i := len(path); i >= 0; i-- {
			k := root
			for _, p := range path[:i] {
				k += `\` + p
			}
			registry.DeleteKey(registry.CURRENT_USER, k)
		}
	})
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for n, v := range vals {
		switch v := v.(type) {
		case int:
			err = k.SetDWordValue(n, uint32(v))
		case string:
			err = k.SetStringValue(n, v)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// @bitssly on X, on Windows itself: a VPN app turned off leaves Internet
// Options' proxy on, at its loopback port. Read from the registry, with
// the real check of the port: direct while nothing listens, the proxy
// again once something does.
func TestWindowsDeadSystemProxyGoesDirect(t *testing.T) {
	clearProxyEnv(t)
	shellSays(t, nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	internetSettings(t, map[string]any{"ProxyEnable": 1, "ProxyServer": addr, "ProxyOverride": "<local>"})
	if p, ok := system(); !ok || p.URL != "http://"+addr {
		t.Fatalf("system() = %+v, %v; want the registry's proxy", p, ok)
	}
	oldDial, oldRead := dialShell, readSystem
	dialShell, readSystem = dialPort, system
	t.Cleanup(func() { dialShell, readSystem = oldDial, oldRead })
	sysCache.at = time.Time{}
	shellPorts.Lock()
	shellPorts.seen = nil
	shellPorts.Unlock()

	if got := proxyFor(t, "https://api.example/"); got != "" {
		t.Fatalf("the dead registry proxy was taken: %q", got)
	}
	if got, from := Describe(); from != "down" || got != "http://"+addr {
		t.Fatalf("Describe = %q (%s), want down", got, from)
	}
	if env := Env([]string{"PATH=C:\\"}); envVal(env, "HTTPS_PROXY") != "" {
		t.Fatalf("a CLI was given the dead registry proxy: %q", env)
	}

	l, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	defer l.Close()
	time.Sleep(shellDown + 100*time.Millisecond)
	if got := proxyFor(t, "https://api.example/"); got != "http://"+addr {
		t.Fatalf("the proxy came back and wasn't taken: %q", got)
	}
}
