package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// updateExecutable, updateCanElevate and updateInContainer are
// update.Executable, update.CanElevate and gateway.InContainer; tests stand
// in for them.
var (
	updateExecutable  = update.Executable
	updateCanElevate  = update.CanElevate
	updateInContainer = gateway.InContainer
	updatePackagedBy  = update.PackagedBy
)

// updateEveries are the intervals magpie update auto takes, by their
// minutes in settings.UpdateEveries.
var updateEveries = map[string]int{"30m": 30, "1h": 60, "6h": 360, "24h": 1440}

// updateAutoCmd is `magpie update auto [on|off] [download|notify]
// [30m|1h|6h|24h]`: whether the app asks for a newer version by itself, how
// often (#472), and whether one found is downloaded at once or only said to
// be out, downloaded on a click (notify, #2296). With none, it says how it
// is set.
func updateAutoCmd(args []string) error {
	s := settings.Load()
	for _, a := range args {
		switch a {
		case "on", "off":
			s.NoAutoUpdate = a == "off"
		case "download", "notify":
			s.NoAutoDownload = a == "notify"
		default:
			m, ok := updateEveries[a]
			if !ok {
				return fmt.Errorf("magpie update auto takes on, off, download, notify or how often (30m, 1h, 6h or 24h), not %q", a)
			}
			s.UpdateEvery = m
		}
	}
	if len(args) > 0 {
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	every := "6h"
	for k, m := range updateEveries {
		if m == s.UpdateEvery {
			every = k
		}
	}
	if s.NoAutoUpdate {
		fmt.Println("automatic updates are off", muted.Render("· magpie update or Settings' Check looks for one (every "+every+" when on)"))
		if s.NoAutoDownload {
			fmt.Println(muted.Render("  a newer version found is only said to be out; Download update or magpie update downloads it"))
		}
		return nil
	}
	if s.NoAutoDownload {
		fmt.Println("automatic updates are on", muted.Render("· magpie looks for a newer version every "+every+" and says it is out; Download update or magpie update downloads it (magpie update auto download: at once)"))
		return nil
	}
	fmt.Println("automatic updates are on", muted.Render("· magpie looks for a newer version every "+every+" and downloads it (magpie update auto notify: only says it is out)"))
	return nil
}

// updateMirrorCmd is `magpie update mirror [<prefix>|off]`: the GitHub
// download mirror an update goes through when usemagpie.ai's file fails, the app's own among them
// (settings.UpdateMirror). With nothing, it says which.
func updateMirrorCmd(args []string) error {
	s := settings.Load()
	if len(args) > 1 {
		return fmt.Errorf("magpie update mirror takes one prefix, or off")
	}
	if len(args) == 1 {
		switch m := strings.TrimSpace(args[0]); m {
		case "off", "none", "":
			s.UpdateMirror = ""
		default:
			if err := update.CheckMirror(m); err != nil {
				return err
			}
			s.UpdateMirror = m
		}
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	if s.UpdateMirror == "" {
		fmt.Println("updates are downloaded from usemagpie.ai, and from GitHub when that fails", muted.Render("· magpie update mirror <prefix> has GitHub's come through a mirror; the checksum is still usemagpie.ai's"))
		return nil
	}
	fmt.Println("updates are downloaded from usemagpie.ai, and through", bold.Render(s.UpdateMirror), "when that fails", muted.Render("· each is checked against usemagpie.ai's checksum · magpie update mirror off goes back to GitHub"))
	return nil
}

// updateChannelCmd is `magpie update channel [stable|every]`: which
// versions an update is offered (settings.UpdateChannel, ssharpcc on X):
// Stable, one a week and an urgent one at once, or every version as it
// comes out. With none, it says which.
func updateChannelCmd(args []string) error {
	s := settings.Load()
	if len(args) > 1 {
		return fmt.Errorf("magpie update channel takes stable or every")
	}
	if len(args) == 1 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "stable":
			s.UpdateChannel = settings.UpdateStable
		case "every", "all":
			s.UpdateChannel = ""
		default:
			return fmt.Errorf("magpie update channel takes stable or every, not %q", args[0])
		}
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	if s.UpdateChannel == settings.UpdateStable {
		fmt.Println("update channel: stable", muted.Render("· a version a week, the newest out three days as the week began, and an urgent one at once · magpie update channel every: each one as it comes out"))
		return nil
	}
	fmt.Println("update channel: every version", muted.Render("· each one as it comes out · magpie update channel stable: one a week, an urgent one at once"))
	return nil
}

// updateFlags takes --proxy and --mirror (each as --flag value or
// --flag=value) out of args.
func updateFlags(args []string) (rest []string, proxy, mirror string, err error) {
	rest, proxy, mirror, _, err = updateFlagsForce(args)
	return
}

// updateFlagsForce is updateFlags that also takes --force: restart the
// running magpie now, whatever it has in flight.
func updateFlagsForce(args []string) (rest []string, proxy, mirror string, force bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, eq := strings.Cut(a, "=")
		if a == "--force" {
			force = true
			continue
		}
		if name != "--proxy" && name != "--mirror" {
			rest = append(rest, a)
			continue
		}
		if !eq {
			if i+1 >= len(args) {
				return nil, "", "", false, fmt.Errorf("%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if name == "--proxy" {
			if err := update.CheckProxy(val); err != nil {
				return nil, "", "", false, err
			}
			proxy = val
			continue
		}
		if val != "off" {
			if err := update.CheckMirror(val); err != nil {
				return nil, "", "", false, err
			}
		}
		mirror = val
	}
	return rest, proxy, mirror, force, nil
}

// updateCmd is `magpie update [check] [--proxy <url>] [--mirror <prefix>]`:
// the app replaces its bundle, the terminal build its binary.
func updateCmd(args []string) error {
	args, proxy, mirror, force, err := updateFlagsForce(args)
	if err != nil {
		return err
	}
	// --addr: where the running magpie is, when it isn't where MAGPIE_ADDR
	// or the gateway's own record says
	args, addr, err := addrFlag(args)
	if err != nil {
		return fmt.Errorf("magpie update: %w", err)
	}
	base := updateBase(addr)
	if len(args) > 1 && args[1] == "auto" {
		return updateAutoCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "mirror" {
		return updateMirrorCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "channel" {
		return updateChannelCmd(args[2:])
	}
	if len(args) > 2 || len(args) == 2 && args[1] != "check" {
		return fmt.Errorf("usage: magpie update [check] [--proxy <url>] [--mirror <prefix>] [--addr host:port] [--force]")
	}
	// the check is bounded; the download after it is not: a slow one
	// still moving goes on, and one that stops sending is ended by
	// update's stall timer (#5572)
	ctx := update.WithMirror(update.WithProxy(context.Background(), proxy), mirror)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	rel, err := update.Latest(cctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Version, version) {
		if update.Released(version) {
			if update.Stable() {
				fmt.Println(green.Render("✓"), "magpie", version, muted.Render("is up to date on the stable channel (stable is "+rel.Version+")"))
			} else {
				fmt.Println(green.Render("✓"), "magpie", version, muted.Render("is the latest"))
			}
		} else {
			fmt.Println("magpie", version, muted.Render("was built from source; the latest release is "+rel.Version))
		}
		return nil
	}
	fmt.Println("magpie", bold.Render(rel.Version), "is out", muted.Render("(you have "+version+") · "+rel.URL))
	if len(args) > 1 && args[1] == "check" {
		return nil
	}
	if !update.Released(version) {
		return fmt.Errorf("this magpie was built from source; update it the way you built it, or get the release from %s", update.Site)
	}
	if app := update.Bundle(); app != "" {
		if update.Stuck(app) != "" {
			return fmt.Errorf("%s can't be replaced where it is; move magpie to Applications, or download the new version from %s", tilde(app), update.Site)
		}
		fmt.Println(muted.Render("  downloading " + update.AppAsset() + via(ctx) + " …"))
		staged, err := update.Stage(ctx, rel, app)
		if err != nil {
			return err
		}
		err = update.Install(staged, app)
		if update.NeedsAdmin(err) {
			err = update.InstallAsAdmin(staged, app)
		}
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "updated", tilde(app), "to", rel.Version)
		fmt.Println(muted.Render("  " + restartAsk(base, rel.Version, force)))
		if stale := update.StaleCLI(rel.Version); stale != "" {
			// The copy won't follow the app; the fix is the link install.sh
			// makes. `magpie update` run from the copy can't do it (it
			// replaces the copy with another copy), so name the real step.
			bin := filepath.Join(app, "Contents", "MacOS", "magpie")
			fmt.Println(muted.Render("  the `magpie` command at " + tilde(stale) + " is a copy behind this app; re-run the installer, or link it: ln -sf " + tilde(bin) + " " + tilde(stale)))
		}
		return nil
	}
	// the Docker image's /magpie, run as nonroot: the image is what is
	// updated, and a replace would only fail with "permission denied"
	if exe, err := updateExecutable(); err == nil && !update.Writable(filepath.Dir(exe)) && !updateCanElevate() && updateInContainer() {
		return fmt.Errorf("this magpie runs in a container and can't replace itself; pull the new image (docker pull ghcr.io/yetone/magpie:latest) and recreate the container")
	}
	// a package manager's binary (Homebrew's, pacman's magpie-bin from
	// the AUR, a .deb's): replaced under it, the manager's database names
	// the old version and its next upgrade writes over this one
	if exe, err := updateExecutable(); err == nil {
		if p := updatePackagedBy(exe); p != nil {
			return fmt.Errorf("%s", p.Say())
		}
	}
	fmt.Println(muted.Render("  downloading " + update.BinaryAsset() + via(ctx) + " …"))
	if err := update.ReplaceBinary(ctx, rel); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "updated to", rel.Version)
	fmt.Println(muted.Render("  " + restartAsk(base, rel.Version, force)))
	return nil
}

// restartRunning asks the magpie serving the gateway at base to restart
// into version, which this command put in (gateway.RestartPath), and
// says what came of it. Before, the app ran on as the old version until
// it was quit and reopened by hand (gaubeebangeel on X: an agent magpie
// serves ran the update, and restarting magpie cut it off). The app
// restarts as its own update does, handing the gateway over on a Mac or
// Linux, so this command's own agent isn't cut short.
var restartRunning = func(base, version string) string { return restartAsk(base, version, false) }

// restartAsk is restartRunning that can force the restart (--force): at
// once, whatever the magpie running has in flight. Without it a restart
// that has to wait is bounded by the magpie itself (gateway.RestartReply
// Drain), so the old version never runs on for good (ljlwlcz on X).
func restartAsk(base, version string, force bool) string {
	c := &http.Client{Timeout: 10 * time.Second}
	res, err := c.Post(base+gateway.RestartPath, "application/json", strings.NewReader(`{"version":`+strconv.Quote(version)+`,"force":`+strconv.FormatBool(force)+`}`))
	if err != nil {
		return "magpie isn't running; it starts as " + version + " next time"
	}
	defer res.Body.Close()
	var r gateway.RestartReply
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
		return "the magpie running is too old to restart by itself: quit and reopen magpie to use it"
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&r); err != nil || res.StatusCode != http.StatusOK {
		return fmt.Sprintf("the magpie running didn't restart (%s): quit and reopen magpie to use it", res.Status)
	}
	carried := ""
	if r.Handover {
		carried = ", handing its gateway over: nothing in flight is cut"
	}
	if force && !r.Forced && (r.State == "restarting" || r.State == "waiting") {
		carried += "; this magpie is too old to know --force, so it restarts as it would have"
	}
	switch r.State {
	case "restarting":
		if r.Forced {
			return "magpie " + r.Version + " restarts into " + version + " now, whatever it has in flight (--force)"
		}
		return "magpie " + r.Version + " restarts into " + version + " now" + carried
	case "waiting":
		n := 0
		if r.Busy != nil {
			n = r.Busy.Requests + r.Busy.Tools
		}
		if r.Drain > 0 {
			return fmt.Sprintf("magpie %s restarts into %s once the %d request(s) it would cut short are done, or in %d min at the latest, which cuts what is still running (magpie update --force restarts now)%s", r.Version, version, n, (r.Drain+59)/60, carried)
		}
		return fmt.Sprintf("magpie %s restarts into %s once the %d request(s) it would cut short are done%s", r.Version, version, n, carried)
	case "unsupported":
		return "the magpie serving the gateway (" + r.Version + ", magpie serve) doesn't restart by itself: restart it the way you started it to use " + version
	}
	return "the magpie serving the gateway (" + r.Version + ") runs from another copy, which wasn't updated: update that one, or restart it"
}

// via says the mirror a download goes through, if any.
func via(ctx context.Context) string {
	if m := update.Mirror(ctx); m != "" {
		return " through " + m
	}
	return ""
}
