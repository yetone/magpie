package main

import (
	"context"
	"fmt"
	"path/filepath"
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
)

// updateEveries are the intervals magpie update auto takes, by their
// minutes in settings.UpdateEveries.
var updateEveries = map[string]int{"30m": 30, "1h": 60, "6h": 360, "24h": 1440}

// updateAutoCmd is `magpie update auto [on|off] [30m|1h|6h|24h]`: whether
// the app asks for a newer version (and downloads it) by itself, and how
// often (#472). With neither, it says how it is set.
func updateAutoCmd(args []string) error {
	s := settings.Load()
	for _, a := range args {
		switch a {
		case "on", "off":
			s.NoAutoUpdate = a == "off"
		default:
			m, ok := updateEveries[a]
			if !ok {
				return fmt.Errorf("magpie update auto takes on, off or how often (30m, 1h, 6h or 24h), not %q", a)
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
		return nil
	}
	fmt.Println("automatic updates are on", muted.Render("· magpie looks for a newer version every "+every+" and downloads it"))
	return nil
}

// updateMirrorCmd is `magpie update mirror [<prefix>|off]`: the GitHub
// download mirror every update goes through, the app's own among them
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
		fmt.Println("updates are downloaded from GitHub", muted.Render("· magpie update mirror <prefix> has them come through a mirror; the checksum is still usemagpie.ai's"))
		return nil
	}
	fmt.Println("updates are downloaded through", bold.Render(s.UpdateMirror), muted.Render("· each is checked against usemagpie.ai's checksum · magpie update mirror off goes back to GitHub"))
	return nil
}

// updateFlags takes --proxy and --mirror (each as --flag value or
// --flag=value) out of args.
func updateFlags(args []string) (rest []string, proxy, mirror string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, eq := strings.Cut(a, "=")
		if name != "--proxy" && name != "--mirror" {
			rest = append(rest, a)
			continue
		}
		if !eq {
			if i+1 >= len(args) {
				return nil, "", "", fmt.Errorf("%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if name == "--proxy" {
			if err := update.CheckProxy(val); err != nil {
				return nil, "", "", err
			}
			proxy = val
			continue
		}
		if val != "off" {
			if err := update.CheckMirror(val); err != nil {
				return nil, "", "", err
			}
		}
		mirror = val
	}
	return rest, proxy, mirror, nil
}

// updateCmd is `magpie update [check] [--proxy <url>] [--mirror <prefix>]`:
// the app replaces its bundle, the terminal build its binary.
func updateCmd(args []string) error {
	args, proxy, mirror, err := updateFlags(args)
	if err != nil {
		return err
	}
	if len(args) > 1 && args[1] == "auto" {
		return updateAutoCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "mirror" {
		return updateMirrorCmd(args[2:])
	}
	if len(args) > 2 || len(args) == 2 && args[1] != "check" {
		return fmt.Errorf("usage: magpie update [check] [--proxy <url>] [--mirror <prefix>]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ctx = update.WithMirror(update.WithProxy(ctx, proxy), mirror)
	rel, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Version, version) {
		if update.Released(version) {
			fmt.Println(green.Render("✓"), "magpie", version, muted.Render("is the latest"))
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
		fmt.Println(green.Render("✓"), "updated", tilde(app), "to", rel.Version, muted.Render("· quit and reopen magpie to use it"))
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
	if exe, err := update.Executable(); err == nil && update.Homebrew(exe) {
		return fmt.Errorf("this magpie was installed with Homebrew; update it with: brew upgrade magpie")
	}
	fmt.Println(muted.Render("  downloading " + update.BinaryAsset() + via(ctx) + " …"))
	if err := update.ReplaceBinary(ctx, rel); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "updated to", rel.Version)
	return nil
}

// via says the mirror a download goes through, if any.
func via(ctx context.Context) string {
	if m := update.Mirror(ctx); m != "" {
		return " through " + m
	}
	return ""
}
