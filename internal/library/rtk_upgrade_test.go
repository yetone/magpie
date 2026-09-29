package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	write(t, filepath.Join(cellar, "rtk"), strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(cellar, "VERSION")))
	write(t, filepath.Join(cellar, "VERSION"), "0.28.2")
	bin := filepath.Join(h, "brew", "bin")
	os.MkdirAll(bin, 0o755)
	if err := os.Symlink(filepath.Join(cellar, "rtk"), filepath.Join(bin, "rtk")); err != nil {
		t.Fatal(err)
	}
	// Homebrew so far has 0.49.0
	write(t, filepath.Join(bin, "brew"), `#!/bin/sh
[ "$*" = "upgrade rtk" ] || exit 2
echo 0.49.0 > "`+filepath.Join(cellar, "VERSION")+`"
`)
	os.Chmod(filepath.Join(cellar, "rtk"), 0o755)
	os.Chmod(filepath.Join(bin, "brew"), 0o755)
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
	write(t, filepath.Join(dir, "rtk"), strings.ReplaceAll(versionedRTK, "VERSION", filepath.Join(dir, "VERSION")))
	write(t, filepath.Join(dir, "VERSION"), "0.40.1")
	os.Chmod(filepath.Join(dir, "rtk"), 0o755)
	tools := filepath.Join(h, "tools")
	// curl hands over an installer that puts 0.50.0 in RTK_INSTALL_DIR
	write(t, filepath.Join(tools, "curl"), `#!/bin/sh
echo 'echo 0.50.0 > "$RTK_INSTALL_DIR/VERSION"'
`)
	os.Chmod(filepath.Join(tools, "curl"), 0o755)
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
