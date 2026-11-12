package proc

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"
)

// PortHolder names the program another process keeps a port with: a child
// of the test, started under a name of its own, listening on a port of its
// own; and this process, which has the port too, is not the answer.
func TestPortHolderNamesTheProgram(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	name := "tailscaled-fake"
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^$")
	cmd.Env = append(os.Environ(), "MAGPIE_PROC_LISTEN=127.0.0.1:"+strconv.Itoa(port))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	line, _ := bufio.NewReader(out).ReadString('\n')
	if line != "listening\n" {
		t.Fatalf("the child said %q", line)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, ok := PortHolder(ctx, port)
	if !ok {
		t.Fatalf("port %d is held by pid %d, and nobody was read", port, cmd.Process.Pid)
	}
	if h.PID != cmd.Process.Pid || h.Name != name {
		t.Fatalf("holder %+v, want pid %d named %q", h, cmd.Process.Pid, name)
	}
	if !h.IsTailscale() {
		t.Fatalf("%+v is not taken for Tailscale's", h)
	}

	// a port this process holds itself has no other holder
	mine, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()
	if h, ok := PortHolder(ctx, mine.Addr().(*net.TCPAddr).Port); ok {
		t.Fatalf("this process was named as another's: %+v", h)
	}
}

// Nothing listening is no holder, and a failed read is no holder either:
// unknown, never a made-up name.
func TestPortHolderUnknownIsNotNamed(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	if h, ok := PortHolder(context.Background(), port); ok {
		t.Fatalf("a free port has a holder: %+v", h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h, ok := PortHolder(ctx, port); ok {
		t.Fatalf("a cancelled read named %+v", h)
	}
}

func TestIsTailscale(t *testing.T) {
	for h, want := range map[Holder]bool{
		{Name: "tailscaled", Path: "/usr/sbin/tailscaled"}:                                true,
		{Name: "Tailscale", Path: "/Applications/Tailscale.app/Contents/MacOS/Tailscale"}: true,
		{Name: "tailscaled", Path: `C:\Program Files\Tailscale\tailscaled.exe`}:           true,
		{Name: "io.tailscale.ipn.macsys.network-extension"}:                               true,
		{Name: "nginx", Path: "/usr/sbin/nginx"}:                                          false,
		{Name: "magpie", Path: "/Applications/Magpie.app/Contents/MacOS/magpie"}:          false,
		{Name: "node", Path: "/Users/u/tools/node"}:                                       false,
		{}: false,
	} {
		if got := h.IsTailscale(); got != want {
			t.Errorf("%+v: %v, want %v", h, got, want)
		}
	}
}

func TestProgramName(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/sbin/tailscaled":                                 "tailscaled",
		`C:\Program Files\Tailscale\tailscaled.exe`:            "tailscaled",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale": "Tailscale",
		"tailscaled": "tailscaled",
		"":           "",
	} {
		if got := programName(in); got != want {
			t.Errorf("programName(%q) = %q, want %q", in, got, want)
		}
	}
}

// `tailscale serve status --json` as Tailscale writes it (ipn.ServeConfig):
// the TCP map by port, Web and AllowFunnel by host:port. The first is a
// serve of port 8848 over HTTPS to magpie, the second a funnel.
func TestServePorts(t *testing.T) {
	serve := `{
  "TCP": {
    "8848": {
      "HTTPS": true
    }
  },
  "Web": {
    "mini.tail1234.ts.net:8848": {
      "Handlers": {
        "/": {
          "Proxy": "http://127.0.0.1:3425"
        }
      }
    }
  }
}`
	funnel := `{
  "TCP": {"443": {"HTTPS": true}, "8443": {"HTTPS": true}},
  "Web": {
    "mini.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}},
    "mini.tail1234.ts.net:8443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3425"}}}
  },
  "AllowFunnel": {"mini.tail1234.ts.net:8443": true}
}`
	// a client older than its daemon prints this before the JSON
	warned := "Warning: client version \"1.94.1-td885b3477\" != tailscaled server version \"1.102.3-t9329c3677-ga522f65e9\"\n" + serve
	for name, c := range map[string]struct {
		in   string
		want []int
	}{
		"serve":     {serve, []int{8848}},
		"funnel":    {funnel, []int{443, 8443}},
		"warning":   {warned, []int{8848}},
		"none":      {"{}\n", nil},
		"garbage":   {"not json", nil},
		"empty":     {"", nil},
		"truncated": {serve[:40], nil},
	} {
		got := servePorts([]byte(c.in))
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
