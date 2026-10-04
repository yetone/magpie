package gateway

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestPublicURL(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if err := os.Unsetenv("MAGPIE_PUBLIC_URL"); err != nil {
		t.Fatal(err)
	}
	if got := PublicURL(); got != "" {
		t.Fatalf("unset: %q, want no public URL", got)
	}
	for _, c := range []struct{ public, want string }{
		{"", ""},
		{"https://magpie.example.com", "https://magpie.example.com"},
		{"https://magpie.example.com/", "https://magpie.example.com"},
		{"https://magpie.example.com/magpie///", "https://magpie.example.com/magpie"},
		{"http://magpie.example.com:3425/", "http://magpie.example.com:3425"},
		{"http://[::1]:3425/", "http://[::1]:3425"},
		{"magpie.example.com", "http://magpie.example.com"},
		{"nas.lan:3425///", "http://nas.lan:3425"},
		{"[fd00::20]:3425", "http://[fd00::20]:3425"},
		{"///", ""},
		{"ftp://magpie.example.com", ""},
		{"//magpie.example.com", ""},
		{"https:///magpie", ""},
		{"http://", ""},
		{"https:///", ""},
		{"ftp://", ""},
		{"https://magpie.example.com:bad", ""},
		{"https://magpie.example.com/%zz", ""},
		{" https://magpie.example.com", "https://magpie.example.com"},
		{"\thttps://magpie.example.com/magpie/ ", "https://magpie.example.com/magpie"},
		{"https://magpie.example.com/magpie?x=1", ""},
		{"https://magpie.example.com/magpie#frag", ""},
		{"https://user:pass@magpie.example.com", ""},
	} {
		t.Run(c.public, func(t *testing.T) {
			t.Setenv("MAGPIE_PUBLIC_URL", c.public)
			if got := PublicURL(); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
			invalid := c.public != "" && c.want == ""
			if invalid {
				if got := publicURL(); got != "" {
					t.Errorf("invalid public URL: %q", got)
				}
				if got := PublicHost(); got != "" {
					t.Errorf("invalid public host: %q", got)
				}
				if got := ContainerAddrs(); got != inContainer("/") {
					t.Errorf("invalid URL must be treated as unset in a container: %v", got)
				}
				for _, got := range LANURLs() {
					if !strings.HasPrefix(got, "http://") {
						t.Errorf("invalid LAN URL: %q", got)
					}
				}
			} else if c.public != "" {
				if got := LANURLs(); len(got) != 1 || got[0] != c.want {
					t.Errorf("LAN URLs: %v, want [%s]", got, c.want)
				}
				if ContainerAddrs() {
					t.Error("valid public URL still says the addresses are a container's")
				}
			}
			if got := Addr(); got != "0.0.0.0:3499" {
				t.Errorf("listen address changed: %q", got)
			}
			if got := URL(); got != "http://127.0.0.1:3499" {
				t.Errorf("local URL changed: %q", got)
			}
		})
	}
}

func TestPublicURLWarningOnce(t *testing.T) {
	// Reset the warning so the assertion does not depend on test order.
	warn := warnInvalidPublicURL
	warnInvalidPublicURL = sync.OnceFunc(logInvalidPublicURL)
	t.Cleanup(func() { warnInvalidPublicURL = warn })
	var logs bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(writer) })
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3499")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	PublicURL()
	if logs.Len() != 0 {
		t.Fatalf("unset URL warned: %q", logs.String())
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "nas.lan:3425")
	PublicURL()
	if logs.Len() != 0 {
		t.Fatalf("valid URL warned: %q", logs.String())
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "ftp://nas.lan")
	if got := PublicURL(); got != "" {
		t.Fatalf("invalid URL: %q, want no public URL", got)
	}
	if got := strings.Count(logs.String(), "ignoring invalid MAGPIE_PUBLIC_URL"); got != 1 {
		t.Fatalf("first invalid URL: %q, want exactly one warning", logs.String())
	}
	logs.Reset()
	for _, u := range []string{"ftp://nas.lan", "https:///magpie", "https://nas.lan:bad"} {
		t.Setenv("MAGPIE_PUBLIC_URL", u)
		for range 3 {
			PublicURL()
			PublicHost()
			LANURLs()
			ContainerAddrs()
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("subsequent calls warned again: %q", logs.String())
	}
}

// OpenToAnyone: the gateway listens beyond loopback (MAGPIE_ADDR) and no key
// is shared, so anyone who reaches it is let in — the console says so, as
// keyNote does.
func TestOpenToAnyone(t *testing.T) {
	h := t.TempDir()
	testenv.SetHome(t, h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local"))
	for _, c := range []struct {
		addr string
		lan  bool
		want bool
	}{
		{"127.0.0.1:3425", false, false},
		{"127.0.0.1:3425", true, false},
		{"0.0.0.0:3425", false, true},
		{"0.0.0.0:3425", true, false},
		{"10.0.0.5:3425", false, true},
		{"10.0.0.5:3425", true, false},
	} {
		t.Setenv("MAGPIE_ADDR", c.addr)
		if err := settings.Save(settings.Settings{LAN: c.lan}); err != nil {
			t.Fatal(err)
		}
		if got := OpenToAnyone(); got != c.want {
			t.Errorf("%s shared=%v: %v, want %v", c.addr, c.lan, got, c.want)
		}
	}
}
