package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cindy 0.1.94's global build keeps its data in CindyGlobal, and makes the
// cn build's Cindy too, for the shared-* folders both builds write there.
// Cindy came first whenever it was there, so the Library wrote the global
// build's MCP servers into Cindy/codex-home/config.toml, which nothing
// reads. The data folder is the one holding a build's database; with both
// builds' data here, the installed app's on a Mac, else the one written
// last.
func TestCindyDataFolder(t *testing.T) {
	db := func(t *testing.T, d, name string, at time.Time) {
		t.Helper()
		p := filepath.Join(d, name)
		writeAt(t, p, "")
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	app := func(t *testing.T, apps, id string) {
		writeAt(t, filepath.Join(apps, "Cindy.app", "Contents", "Info.plist"),
			`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>`+id+`</string></dict></plist>`)
	}
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			was := removedOS
			removedOS = goos
			t.Cleanup(func() { removedOS = was })
			first := func(t *testing.T) string {
				t.Helper()
				if a := cindy(); a.Dir != cindyDirs()[0] {
					t.Fatalf("Dir %s, cindyDirs %v", a.Dir, cindyDirs())
				}
				return cindy().Dir
			}

			// the global build: Cindy holds only what both builds share
			_, cn := cindyDetectHome(t)
			apps := noApps(t)
			global := filepath.Join(filepath.Dir(cn), "CindyGlobal")
			mkdirT(t, filepath.Join(cn, "shared-system-skills", "x"))
			mkdirT(t, filepath.Join(cn, "shared-skill-mutation-locks"))
			db(t, global, "cindy-local-v1.db", old)
			app(t, apps, "com.xd.cindy")
			if got := first(t); got != global {
				t.Errorf("global build: %s, want %s", got, global)
			}
			// magpie's own leftovers there are no data of Cindy's
			writeAt(t, filepath.Join(cn, "codex-home", "config.toml"), "[mcp_servers.magpie]\nurl = \"http://127.0.0.1:3425/mcp/magpie/hub/cindy\"\n")
			writeAt(t, filepath.Join(cn, "pi-agent-home", "mcp.json"), "{}")
			if got := first(t); got != global {
				t.Errorf("global build with magpie's leftovers in Cindy: %s", got)
			}
			if CindyData(cn) || !CindyData(global) {
				t.Errorf("CindyData: Cindy %v, CindyGlobal %v", CindyData(cn), CindyData(global))
			}

			// both builds' data here: the installed one's on a Mac, else the
			// one written last, a signed-in account's -wal included
			db(t, cn, "cindy-local-v1.db", recent)
			want := cn
			if goos == "darwin" {
				want = global
			}
			if got := first(t); got != want {
				t.Errorf("both, CindyGlobal's app, Cindy's written last: %s, want %s", got, want)
			}
			db(t, global, "cindy-acc1.db-wal", time.Now())
			if got := first(t); got != global {
				t.Errorf("both, CindyGlobal's written last: %s", got)
			}
			app(t, apps, "com.xd.cindycn")
			want = global
			if goos == "darwin" {
				want = cn
			}
			if got := first(t); got != want {
				t.Errorf("both, Cindy's app, CindyGlobal's written last: %s, want %s", got, want)
			}
		})
	}
	t.Run("no data", func(t *testing.T) {
		// opened before any database: the folder there, Cindy before
		// CindyGlobal, as before
		_, cn := cindyDetectHome(t)
		noApps(t)
		global := filepath.Join(filepath.Dir(cn), "CindyGlobal")
		if got := cindyDirs(); len(got) != 2 || got[0] != cn || got[1] != global {
			t.Errorf("neither there: %v", got)
		}
		mkdirT(t, global)
		if got := cindyDirs()[0]; got != global {
			t.Errorf("CindyGlobal alone there: %s", got)
		}
		mkdirT(t, cn)
		if got := cindyDirs()[0]; got != cn {
			t.Errorf("both there, no data: %s", got)
		}
	})
}
