package wslrun

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestParseProbe(t *testing.T) {
	out := "bin:/home/me/.local/bin/claude\n" +
		"path:/home/me/.nvm/bin:/usr/bin\n" +
		"lpath:/usr/bin:/bin\n" +
		"mount:/mnt/c/\n" +
		"route:default via 172.20.0.1 dev eth0 proto kernel\n" +
		"net:nat\n"
	tool, ok := parseProbe("Ubuntu", out)
	if !ok {
		t.Fatal("not found")
	}
	if tool.Distro != "Ubuntu" || tool.Path != "/home/me/.local/bin/claude" || tool.Mount != "/mnt/" || tool.Host != "172.20.0.1" {
		t.Fatalf("tool = %+v", tool)
	}
	// the interactive shell's PATH wins, and the bin's folder joins it
	if tool.PATH != "/home/me/.local/bin:/home/me/.nvm/bin:/usr/bin" {
		t.Fatalf("PATH = %q", tool.PATH)
	}

	// mirrored networking: the distro reaches Windows' loopback itself
	tool, _ = parseProbe("d", "bin:/usr/bin/claude\nlpath:/usr/bin\nmount:/win/c/\nroute:default via 10.0.0.1 dev eth0\nnet:mirrored\n")
	if tool.Host != "" || tool.Mount != "/win/" || tool.PATH != "/usr/bin" {
		t.Fatalf("mirrored = %+v", tool)
	}

	// no claude in the distro (the probe skips Windows' own under /mnt)
	if _, ok := parseProbe("d", "mount:/mnt/c/\n"); ok {
		t.Fatal("found nothing, yet ok")
	}
}

func TestProbeSkipsWindowsOwn(t *testing.T) {
	if s := probeScript("claude"); !strings.Contains(s, `/mnt/*|"") continue`) {
		t.Fatalf("probe takes a Windows claude under /mnt: %s", s)
	}
}

func TestLinux(t *testing.T) {
	tool := Tool{Mount: "/mnt/"}
	for in, want := range map[string]string{
		`C:\Users\me\magpie\magpie.exe`: "/mnt/c/Users/me/magpie/magpie.exe",
		`D:\`:                           "/mnt/d/",
		`/already/linux`:                "/already/linux",
	} {
		if got := tool.Linux(in); got != want {
			t.Errorf("Linux(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnv(t *testing.T) {
	tool := Tool{Host: "172.20.0.1"}
	env := tool.Env([]string{
		"CLAUDE_CONFIG_DIR=C:\\cfg",
		"DISABLE_TELEMETRY=1",
		"HTTPS_PROXY=http://127.0.0.1:7890",
		"NO_PROXY=localhost",
		"WSLENV=USERPROFILE/p",
		"OTHER=x",
	}, "CLAUDE_CONFIG_DIR/p", "DISABLE_TELEMETRY", "MISSING")
	got := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		got[k] = v
	}
	if w := got["WSLENV"]; w != "USERPROFILE/p:CLAUDE_CONFIG_DIR/p:DISABLE_TELEMETRY:HTTPS_PROXY:NO_PROXY" {
		t.Fatalf("WSLENV = %q", w)
	}
	if got["HTTPS_PROXY"] != "http://172.20.0.1:7890" || got["NO_PROXY"] != "localhost" || got["OTHER"] != "x" {
		t.Fatalf("env = %v", got)
	}
	// mirrored: the proxy is left as it is
	if p := (Tool{}).proxy("http://127.0.0.1:7890"); p != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", p)
	}
}

func utf16le(s string) []byte {
	b := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func TestFind(t *testing.T) {
	oldOn, oldRun := On, Run
	t.Cleanup(func() { On, Run = oldOn, oldRun; Forget() })
	On = true
	Forget()
	var probed []string
	Run = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l --running -q":
			return utf16le("docker-desktop\r\nFedora\r\nUbuntu\r\n"), nil
		case "-l -q":
			return utf16le("Ubuntu\r\nFedora\r\nStopped\r\n"), nil
		}
		probed = append(probed, args[1])
		if args[1] == "Fedora" {
			return []byte("bin:/usr/bin/claude\nlpath:/usr/bin\n"), nil
		}
		return []byte("mount:/mnt/c/\n"), nil
	}
	if _, ok := Known("claude"); ok {
		t.Fatal("known before any Find")
	}
	tool, ok := Find("claude")
	if !ok || tool.Distro != "Fedora" {
		t.Fatalf("Find = %+v %v", tool, ok)
	}
	// the default distro first, Docker's and stopped ones never
	if strings.Join(probed, ",") != "Ubuntu,Fedora" {
		t.Fatalf("probed %v", probed)
	}
	probed = nil
	if tool, ok := Find("claude"); !ok || tool.Distro != "Fedora" || probed != nil {
		t.Fatalf("not cached: %+v %v %v", tool, ok, probed)
	}
	if tool, ok := Known("claude"); !ok || tool.Distro != "Fedora" {
		t.Fatalf("Known = %+v %v", tool, ok)
	}
}

func TestArgv(t *testing.T) {
	tool := Tool{Distro: "Ubuntu", Path: "/usr/bin/claude", PATH: "/usr/bin"}
	a := tool.argv([]string{"-p"})
	if a[0] != "-d" || a[1] != "Ubuntu" || a[2] != "--exec" || a[4] != "PATH=/usr/bin" || !strings.HasPrefix(a[5], runVar+"=") || a[6] != "/usr/bin/claude" || a[7] != "-p" {
		t.Fatalf("argv = %v", a)
	}
	if b := tool.argv(nil); b[5] == a[5] {
		t.Fatal("two runs share a mark")
	}
}
