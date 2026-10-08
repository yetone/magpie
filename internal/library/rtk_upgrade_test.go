package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// versionedRTK is an rtk whose version is in the file VERSION names, with
// two days of savings.
const versionedRTK = `#!/bin/sh
case "$1" in
--version) echo "rtk $(/bin/cat "VERSION")"; exit 0 ;;
gain) echo '{"summary":{"total_commands":5,"total_input":1000,"total_saved":800,"avg_savings_pct":80},"daily":[
 {"date":"2026-09-02","commands":2,"input_tokens":600,"output_tokens":100,"saved_tokens":500,"savings_pct":83.3},
 {"date":"2026-09-01","commands":3,"input_tokens":400,"output_tokens":100,"saved_tokens":300,"savings_pct":75}]}'; exit 0 ;;
esac
exit 2
`

func latestServer(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/rtk-ai/rtk/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	old, oldAPI := rtkReleases, rtkReleasesAPI
	rtkReleases, rtkReleasesAPI = srv.URL+"/rtk-ai/rtk/releases/latest", srv.URL+"/nothing"
	t.Cleanup(func() { rtkReleases, rtkReleasesAPI = old, oldAPI })
	rtkLatest.Lock()
	rtkLatest.v, rtkLatest.next = "", time.Time{}
	rtkLatest.Unlock()
	forgetChannels()
	t.Cleanup(forgetChannels)
}

// forgetChannels forgets what winget and Homebrew were found to have.
func forgetChannels() {
	rtkChannels.Lock()
	clear(rtkChannels.m)
	rtkChannels.Unlock()
}

// asJSON is the view as the page gets it.
func asJSON(t *testing.T, v *RTKView) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

// Homebrew's rtk is upgraded with brew; the days it saved come with the
// summary, oldest first; the latest release is read from where GitHub
// redirects to.
func TestRTKUpgradeBrew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	latestServer(t, "v0.50.0")
	cellar := filepath.Join(h, "brew", "Cellar", "rtk", "0.28.2", "bin")
	testenv.Program(t, filepath.Join(cellar, "rtk"), strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(cellar, "VERSION")))
	write(t, filepath.Join(cellar, "VERSION"), "0.28.2")
	bin := filepath.Join(h, "brew", "bin")
	os.MkdirAll(bin, 0o755)
	if err := os.Symlink(filepath.Join(cellar, "rtk"), filepath.Join(bin, "rtk")); err != nil {
		t.Fatal(err)
	}
	// Homebrew so far has 0.49.0
	testenv.Program(t, filepath.Join(bin, "brew"), `#!/bin/sh
[ "$*" = "upgrade rtk" ] || exit 2
echo 0.49.0 > "`+filepath.Join(cellar, "VERSION")+`"
`)
	t.Setenv("PATH", bin)

	v := ReadRTK()
	if v.Version != "0.28.2" || v.Upgrade != "brew upgrade rtk" || v.Gain == nil || v.Gain.Saved != 800 {
		t.Fatalf("view: %+v", v)
	}
	if len(v.Days) != 2 || v.Days[0].Date != "2026-09-01" || v.Days[0].Saved != 300 || v.Days[1].Input != 600 || v.Days[1].Commands != 2 {
		t.Fatalf("days: %+v", v.Days)
	}
	if v.CheckLatest(); v.Latest != "0.50.0" {
		t.Fatalf("latest %q", v.Latest)
	}
	v, err := UpgradeRTK()
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "0.49.0" || !strings.Contains(v.Note, "Homebrew's RTK is 0.49.0") || !strings.Contains(v.Note, "0.50.0") {
		t.Fatalf("after: %q, note %q", v.Version, v.Note)
	}
	// #1025: Homebrew has nothing newer, so the page offers no upgrade —
	// then, and when it reads the card again
	if m := asJSON(t, v); m["waiting"] != "Homebrew" || m["waitingHas"] != "0.49.0" {
		t.Fatalf("after the upgrade: waiting %v, has %v", m["waiting"], m["waitingHas"])
	}
	v = ReadRTK()
	if v.CheckLatest(); asJSON(t, v)["waiting"] != "Homebrew" {
		t.Fatalf("read again: %+v", v)
	}
}

// An rtk its own script put somewhere is upgraded with that script again,
// into the same folder.
func TestRTKUpgradeScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	latestServer(t, "v0.50.0")
	dir := filepath.Join(h, ".local", "bin")
	testenv.Program(t, filepath.Join(dir, "rtk"), strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(dir, "VERSION")))
	write(t, filepath.Join(dir, "VERSION"), "0.40.1")
	tools := filepath.Join(h, "tools")
	// curl hands over an installer that puts 0.50.0 in RTK_INSTALL_DIR
	testenv.Program(t, filepath.Join(tools, "curl"), `#!/bin/sh
echo 'echo 0.50.0 > "$RTK_INSTALL_DIR/VERSION"'
`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+"/usr/bin")

	v := ReadRTK()
	if v.Path != filepath.Join(dir, "rtk") || !strings.Contains(v.Upgrade, "install.sh | sh") {
		t.Fatalf("view: %+v", v)
	}
	if v, err := UpgradeRTK(); err != nil || v.Version != "0.50.0" || v.Note != "" {
		t.Fatalf("after: %+v, %v", v, err)
	}
}

func TestRTKNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.50.0", "0.28.2", true}, {"0.28.2", "0.50.0", false}, {"1.0.0", "0.99.9", true}, {"0.50.0", "0.50.0", false}, {"0.50.1", "0.50.0", true}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// #741: a failed upgrade said only its command line, cut short on the page
// ("winget upgrade --id rtk-ai.rtk --exact --silent ..."). It now says the
// tool, its exit code (winget's HRESULT in hex, with what it means) and the
// last lines it printed that say something — not the spinner frames and
// progress bars winget writes to a pipe.
func TestRTKUpgradeFailureSaid(t *testing.T) {
	winget := []string{"winget", "upgrade", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	out := "   - \r   \\ \r   | \r   / \rFound RTK [rtk-ai.rtk] Version 0.51.0\r\n" +
		"This application is licensed to you by its owner.\r\nMicrosoft is not responsible for, nor does it grant any licenses to, third-party packages.\r\n" +
		"Downloading https://github.com/rtk-ai/rtk/releases/download/v0.51.0/rtk-x86_64-pc-windows-msvc.zip\r\n" +
		"  ██████████████▒▒▒▒▒▒▒▒▒▒▒▒  1.20 MB / 3.40 MB\r  ██████████████████████████  3.40 MB / 3.40 MB\r\n" +
		"Successfully verified installer hash\r\nExtracting archive...\r\n\x1b[31mThe file cannot be accessed by the system.\x1b[0m\r\n   - \r"
	// ExitCode is the HRESULT's uint32 on 64-bit Windows; negative as an int32
	for _, code := range []int{0x8A150101, -1978334975} {
		msg := installFailed(winget, out, code, nil).Error()
		want := "winget failed (exit code 0x8A150101): RTK is in use — close the agents running it and try again — Successfully verified installer hash Extracting archive... The file cannot be accessed by the system."
		if msg != want {
			t.Fatalf("code %d:\n got %q\nwant %q", code, msg, want)
		}
	}
	if msg := installFailed(winget, "", 0x8A15002B, nil).Error(); msg != "winget failed (exit code 0x8A15002B): winget has no newer RTK than this one yet" {
		t.Fatalf("not applicable: %q", msg)
	}
	if msg := installFailed([]string{"sh", "-c", "curl … | sh"}, "curl: (6) Could not resolve host: raw.githubusercontent.com\n", 6, nil).Error(); msg != "RTK's install script failed (exit code 6) — curl: (6) Could not resolve host: raw.githubusercontent.com" {
		t.Fatalf("script: %q", msg)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// a real process: its exit code and output; one that can't start, and
	// one that runs too long, say so
	_, err := runInstaller([]string{"sh", "-c", "printf '50%%\\r100%%\\r\\nno space left on device\\n'; exit 3"}, time.Minute)
	if err == nil || err.Error() != "RTK's install script failed (exit code 3) — no space left on device" {
		t.Fatalf("exit 3: %v", err)
	}
	if _, err := runInstaller([]string{"/nonexistent/winget"}, time.Minute); err == nil || !strings.HasPrefix(err.Error(), "/nonexistent/winget couldn't run: ") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := runInstaller([]string{"sleep", "5"}, 100*time.Millisecond); err == nil || err.Error() != "sleep didn't finish in 100ms" {
		t.Fatalf("timeout: %v", err)
	}
}

// #1025 (Fermin214): RTK 0.51.0 was on GitHub while winget had 0.50.0, the
// one installed. Upgrade ran winget upgrade, which said so, and the card
// then offered "v0.51.0 is out" and Upgrade again. The card now says winget
// hasn't got it yet and offers no upgrade, from an upgrade's answer or from
// asking winget itself (winget show), until winget has it; a winget that
// can't be asked leaves Upgrade offered. Homebrew's lag is in
// TestRTKUpgradeBrew.
func TestRTKWaitsForWinget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	h := sandbox(t)
	latestServer(t, "v0.51.0")
	links := filepath.Join(h, "AppData", "Local", "Microsoft", "WinGet", "Links")
	testenv.Program(t, filepath.Join(links, "rtk"), strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(links, "VERSION")))
	write(t, filepath.Join(links, "VERSION"), "0.50.0")
	has, broken := filepath.Join(h, "winget-has"), filepath.Join(h, "winget-broken")
	write(t, has, "0.50.0")
	tools := filepath.Join(h, "tools")
	// winget show, as Windows in Chinese prints it; winget upgrade installs
	// what it has, when that is newer, and says when it isn't
	testenv.Program(t, filepath.Join(tools, "winget"), `#!/bin/sh
[ -e "`+broken+`" ] && { echo "Failed when searching source: winget"; exit 1; }
v=$(/bin/cat "`+has+`")
case "$1" in
show) printf '已找到 RTK [rtk-ai.rtk]\r\n版本: %s\r\n发布者: rtk-ai\r\n描述: RTK 0.99.0 compatible\r\n' "$v" ;;
upgrade)
  if [ "$v" = "$(/bin/cat "`+filepath.Join(links, "VERSION")+`")" ]; then echo "No available upgrade found."; exit 0; fi
  echo "$v" > "`+filepath.Join(links, "VERSION")+`"; echo "Successfully installed" ;;
*) exit 2 ;;
esac
`)
	t.Setenv("PATH", links+string(os.PathListSeparator)+tools)
	up := upgraderOf
	upgraderOf = func(string) []string {
		return []string{"winget", "upgrade", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	}
	t.Cleanup(func() { upgraderOf = up })
	card := func() map[string]any {
		v := ReadRTK()
		v.CheckLatest()
		return asJSON(t, v)
	}

	// winget can't be asked: not known, so Upgrade stays offered
	write(t, broken, "")
	if m := card(); m["latest"] != "0.51.0" || m["waiting"] != nil {
		t.Fatalf("winget unasked: %v", m)
	}
	os.Remove(broken)
	forgetChannels()

	// asked, winget has the one installed
	if m := card(); m["waiting"] != "winget" || m["waitingHas"] != "0.50.0" {
		t.Fatalf("winget asked: %v", m)
	}

	// the reporter's click: winget upgrade changes nothing, and says so; the
	// card read again afterwards still waits, without asking winget again
	forgetChannels()
	v, err := UpgradeRTK()
	if err != nil {
		t.Fatal(err)
	}
	if m := asJSON(t, v); v.Version != "0.50.0" || m["waiting"] != "winget" || !strings.Contains(v.Note, "winget's RTK is 0.50.0 so far") {
		t.Fatalf("after the upgrade: %v", m)
	}
	write(t, broken, "")
	if m := card(); m["waiting"] != "winget" {
		t.Fatalf("read again: %v", m)
	}
	os.Remove(broken)

	// winget has 0.51.0 now: once asked again, Upgrade is back, and works
	write(t, has, "0.51.0")
	if m := card(); m["waiting"] != "winget" {
		t.Fatalf("before winget is asked again: %v", m)
	}
	rtkChannels.Lock()
	for k, e := range rtkChannels.m {
		e.next = time.Now().Add(-time.Second)
		rtkChannels.m[k] = e
	}
	rtkChannels.Unlock()
	if m := card(); m["waiting"] != nil || m["latest"] != "0.51.0" || m["version"] != "0.50.0" {
		t.Fatalf("winget has it: %v", m)
	}
	if v, err := UpgradeRTK(); err != nil || v.Version != "0.51.0" || v.Waiting != "" || v.Note != "" {
		t.Fatalf("upgraded: %+v, %v", v, err)
	}
}

// winget show's version, in any language, and brew info's.
func TestRTKChannelVersion(t *testing.T) {
	for _, c := range []struct{ tool, out, want string }{
		{"winget", "Found RTK [rtk-ai.rtk]\r\nVersion: 0.50.0\r\nPublisher: rtk-ai\r\n", "0.50.0"},
		{"winget", "   - \r   \\ \rFound RTK [rtk-ai.rtk]\r\nVersion: 0.50.0\r\n", "0.50.0"},
		{"winget", "Gefunden RTK [rtk-ai.rtk]\nVersion: 0.49.1\n", "0.49.1"},
		{"winget", "已找到 RTK [rtk-ai.rtk]\n版本: 0.50.0\n描述: 1.2.3\n", "0.50.0"},
		{"winget", "No package found matching input criteria.\r\n", ""},
		{"brew", `{"formulae":[{"name":"rtk","versions":{"stable":"0.50.0","head":"HEAD"}}],"casks":[]}`, "0.50.0"},
		{"brew", `{"formulae":[],"casks":[]}`, ""},
		{"brew", `Error: No available formula`, ""},
	} {
		got, err := rtkChannelVersion(c.tool, c.out)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s %q: %q, %v", c.tool, c.out, got, err)
		}
	}
}
