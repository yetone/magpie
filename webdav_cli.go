package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// WebDAV and S3 sync from the terminal: what Settings › Sync does, through
// the same davsync.Configure, Now, Off and Dismiss. One of the two is on
// at a time; magpie webdav and magpie s3 each set up their own.

const webdavUsage = `usage:
  magpie webdav                           whether sync is on, where to, and how the last sync went
  magpie webdav on <address> [user=…] [keys=no] [agents=no] [library=no]
                                          keep providers, settings, profiles, agents' models and the library
                                          the same on every computer through a WebDAV folder (a folder named
                                          magpie is made in it), and sync at once; asks for the password
                                          (with a user) and the passphrase
  magpie webdav set k=v…                  change it: address, user, keys, agents, library, usage (yes|no);
                                          password= and passphrase= ask for a new one
  magpie webdav now                       sync now (the gateway also syncs by itself while it runs: see auto)
  magpie webdav auto off|3|15|30|60       how often the gateway syncs by itself, in minutes; off: only when asked
  magpie webdav restore                   make this computer's setup the server's; what is here is kept first
  magpie webdav undo                      put back what the last restore replaced
  magpie webdav upload                    put this computer's setup over a server file that isn't a backup
                                          (empty, cut short, another app's); the server's is kept first
  magpie webdav dismiss                   clear what the last sync said it replaced
  magpie webdav off                       turn it off; the file on the server stays

  passphrase  the same on every computer: the file is sealed with it on this one, and the server only
              ever sees it sealed. Keep it: without it the file can't be opened. Never the password:
              the server is sent that
  password    asked for, unechoed, never given on the command line, as the passphrase isn't. The one saved
              is only sent to the server and user it was given for: change either and it is asked for
              again; user= alone for a server that asks for no sign-in. An app password where the server
              has them
  keys        no: providers go without their API keys, and each computer keeps its own
  usage       yes: this computer's usage goes up too, a file a day, sealed, and the Usage page counts
              the other computers' that share theirs, each named
  first sync  on a computer that had its own setup, each part that differs becomes the server's; what
              was here is kept in the sync folder beside magpie's files, and magpie webdav says so
  moving      magpie webdav on with S3 sync on moves sync to the folder; the bucket, endpoint, access key
              and secret are kept, and magpie s3 on alone moves back to them

  Piped in, the password (when asked) is the first line of stdin and the passphrase the next.
  An S3-compatible bucket instead: magpie s3 help.

  e.g. magpie webdav on https://dav.jianguoyun.com/dav/ user=me@example.com
       magpie webdav set agents=no library=no
`

const s3Usage = `usage:
  magpie s3                               whether sync is on, where to, and how the last sync went
  magpie s3 on s3://<bucket>[/<prefix>] access-key-id=… [endpoint=…] [region=…] [path-style=yes]
               [keys=no] [agents=no] [library=no]
                                          keep providers, settings, profiles, agents' models and the library
                                          the same on every computer through an S3-compatible bucket (AWS S3,
                                          Cloudflare R2, Backblaze B2, MinIO, a NAS…), in <prefix>/magpie/,
                                          and sync at once; asks for the secret and the passphrase
  magpie s3 set k=v…                      change it: address, bucket, prefix, endpoint, region, access-key-id,
                                          path-style, keys, agents, library, usage (yes|no); secret= and passphrase=
                                          ask for a new one
  magpie s3 now                           sync now (the gateway also syncs by itself while it runs: see auto)
  magpie s3 auto off|3|15|30|60           how often the gateway syncs by itself, in minutes; off: only when asked
  magpie s3 restore                       make this computer's setup the server's; what is here is kept first
  magpie s3 undo                          put back what the last restore replaced
  magpie s3 upload                        put this computer's setup over a bucket file that isn't a backup
                                          (empty, cut short, another app's); the bucket's is kept first
  magpie s3 dismiss                       clear what the last sync said it replaced
  magpie s3 off                           turn it off; the file in the bucket stays

  endpoint    the server, https://…; none is AWS. R2: https://<account>.r2.cloudflarestorage.com
  region      none is us-east-1, or auto on R2; AWS says which when it is another
  path-style  yes: the bucket in the path (https://host/bucket/…), as MinIO and most servers of your
              own want; no: in the host name (https://bucket.host/…). Always yes for an IP address
  passphrase  the same on every computer: the file is sealed with it on this one, and the server only
              ever sees it sealed. Keep it: without it the file can't be opened. Never the secret
  secret      the access key's, asked for, unechoed, never given on the command line. The one saved is
              only used with the endpoint and access key it was given for: change either and it is asked
              for again
  keys        no: providers go without their API keys, and each computer keeps its own
  usage       yes: this computer's usage goes up too, a file a day, sealed, and the Usage page counts
              the other computers' that share theirs, each named
  moving      magpie s3 on with WebDAV sync on moves sync to the bucket; the WebDAV address, user and
              password are kept, and magpie webdav on alone moves back to them

  Piped in, the secret (when asked) is the first line of stdin and the passphrase the next.
  A WebDAV folder instead: magpie webdav help.

  e.g. magpie s3 on s3://my-bucket/magpie endpoint=https://<account>.r2.cloudflarestorage.com access-key-id=…
       magpie s3 on s3://backups endpoint=http://nas.local:9000 path-style=yes access-key-id=…
`

// syncKind is magpie webdav's or magpie s3's: which server it sets up.
type syncKind struct {
	name, cmd, usage string
	s3               bool
}

var (
	webdavKind = syncKind{"WebDAV", "webdav", webdavUsage, false}
	s3Kind     = syncKind{"S3", "s3", s3Usage, true}
)

func (k syncKind) turnOn() string {
	if k.s3 {
		return "magpie s3 on s3://<bucket>[/<prefix>] access-key-id=… [endpoint=…] turns it on"
	}
	return "magpie webdav on <address> [user=…] turns it on"
}

// webdavCmd is magpie webdav ….
func webdavCmd(args []string) error { return syncCmd(webdavKind, args) }

// s3Cmd is magpie s3 ….
func s3Cmd(args []string) error { return syncCmd(s3Kind, args) }

func syncCmd(k syncKind, args []string) error {
	if len(args) == 0 || args[0] == "show" {
		return syncShow(k)
	}
	switch args[0] {
	case "on":
		return syncSet(k, args[1:], true)
	case "set":
		return syncSet(k, args[1:], false)
	case "now":
		return syncNow(k)
	case "auto":
		if len(args) != 2 {
			return fmt.Errorf("magpie %s auto off|3|15|30|60", k.cmd)
		}
		m := davsync.Manual
		if args[1] != "off" {
			if _, err := fmt.Sscan(args[1], &m); err != nil {
				return fmt.Errorf("magpie %s auto off|3|15|30|60, not %q", k.cmd, args[1])
			}
		}
		if err := davsync.SetAuto(m); err != nil {
			return err
		}
		return syncShow(k)
	case "restore", "undo":
		if _, ok := davsync.Load(); !ok {
			return fmt.Errorf("%s sync is off: %s", k.name, k.turnOn())
		}
		var parts []string
		var err error
		if args[0] == "restore" {
			parts, err = davsync.Restore(context.Background())
		} else {
			parts, err = davsync.Undo()
		}
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			fmt.Println("nothing differed: nothing was replaced")
		} else if args[0] == "restore" {
			fmt.Printf("restored from the server: %s; what was here is kept, and magpie %s undo puts it back\n", strings.Join(parts, ", "), k.cmd)
		} else {
			fmt.Printf("put back: %s\n", strings.Join(parts, ", "))
		}
		return nil
	case "upload":
		if _, ok := davsync.Load(); !ok {
			return fmt.Errorf("%s sync is off: %s", k.name, k.turnOn())
		}
		if err := davsync.Upload(context.Background()); err != nil {
			return err
		}
		fmt.Println("this computer's setup is on the server now; what was there is kept in the sync folder")
		return nil
	case "dismiss":
		if err := davsync.Dismiss(); err != nil {
			return err
		}
		return syncShow(k)
	case "off":
		// the other's is turned off by its own command, not by a slip
		if c, ok := davsync.Load(); ok && c.S3() != k.s3 {
			return fmt.Errorf("%s sync is on, not %s: magpie %s off turns it off", c.Kind(), k.name, other(k).cmd)
		}
		if err := davsync.Off(); err != nil {
			return err
		}
		if k.s3 {
			fmt.Println("S3 sync is off; the file in the bucket stays")
		} else {
			fmt.Println("WebDAV sync is off; the file on the server stays")
		}
		return nil
	case "help", "-h", "--help":
		fmt.Print(k.usage)
		return nil
	}
	return fmt.Errorf("unknown %s command %q\n\n%s", k.cmd, args[0], k.usage)
}

func other(k syncKind) syncKind {
	if k.s3 {
		return webdavKind
	}
	return s3Kind
}

// syncSet turns sync on (on), or changes it (set): what is not given
// stays as it was. On, with the other kind on, it moves there (to the
// server of its kind kept from before, when nothing else is given), keeping
// the passphrase and what is synced.
func syncSet(k syncKind, args []string, on bool) error {
	c, was := davsync.Load()
	if was && c.S3() != k.s3 {
		if !on {
			return fmt.Errorf("%s sync is on, not %s: magpie %s set changes it; %s", c.Kind(), k.name, other(k).cmd, k.turnOn())
		}
		next := davsync.Config{Passphrase: c.Passphrase, Keys: c.Keys, Agents: c.Agents, Library: c.Library, Usage: c.Usage}
		if o := c.Other; o != nil { // its server, as it was kept when sync moved from it
			next.URL, next.User, next.Endpoint, next.Region, next.PathStyle = o.URL, o.User, o.Endpoint, o.Region, o.PathStyle
		}
		c = next
	}
	if !was {
		if !on {
			return fmt.Errorf("%s sync is off: %s", k.name, k.turnOn())
		}
		c = davsync.Config{Keys: true, Agents: true}
	}
	askPass, askPhrase := false, c.Passphrase == ""
	address := false
	secretName := "password"
	if k.s3 {
		secretName = "secret"
	}
	var bucket, prefix *string
	for _, a := range args {
		key, v, ok := strings.Cut(a, "=")
		if !ok || strings.Contains(key, "://") { // the address, bare, = in it or not
			if address {
				return fmt.Errorf("one address, not %q too\n\n%s", a, k.usage)
			}
			key, v, address = "address", a, true
		}
		switch {
		case key == "address":
			c.URL = v
		case key == "user" && !k.s3, key == "access-key-id" && k.s3:
			c.User = v
		case key == "bucket" && k.s3:
			bucket = &v
		case key == "prefix" && k.s3:
			prefix = &v
		case key == "endpoint" && k.s3:
			c.Endpoint = v
		case key == "region" && k.s3:
			c.Region = v
		case key == secretName, key == "passphrase": // not in the shell's history, nor in ps
			if v != "" {
				return fmt.Errorf("the %s is asked for, not given: %s= alone", key, key)
			}
			if key == secretName {
				askPass = true
			} else {
				askPhrase = true
			}
		case key == "keys", key == "agents", key == "library", key == "usage", key == "path-style" && k.s3:
			if v != "yes" && v != "no" {
				return fmt.Errorf("%s=yes|no, not %q", key, v)
			}
			yes := v == "yes"
			switch key {
			case "keys":
				c.Keys = yes
			case "agents":
				c.Agents = yes
			case "path-style":
				c.PathStyle = yes
			case "usage":
				c.Usage = yes
			default:
				c.Library = &yes
			}
		case k.s3:
			return fmt.Errorf("unknown field %q (address, bucket, prefix, endpoint, region, access-key-id, secret, passphrase, path-style, keys, agents, library, usage)", key)
		default:
			return fmt.Errorf("unknown field %q (address, user, password, passphrase, keys, agents, library, usage)", key)
		}
	}
	if bucket != nil || prefix != nil {
		b, p := s3Parts(c.URL)
		if bucket != nil {
			b = strings.TrimSpace(*bucket)
		}
		if prefix != nil {
			p = strings.Trim(strings.TrimSpace(*prefix), "/")
		}
		c.URL = "s3://" + b
		if p != "" {
			c.URL += "/" + p
		}
	}
	if strings.TrimSpace(c.URL) == "" || c.URL == "s3://" {
		return fmt.Errorf("no address\n\n%s", k.usage)
	}
	// a wrong one said before any secret is typed
	if c.S3() != k.s3 {
		if k.s3 {
			return fmt.Errorf("%q is not an S3 address (s3://bucket or s3://bucket/prefix)", c.URL)
		}
		return fmt.Errorf("%q is not a WebDAV address (https://…): magpie s3 on %s for an S3 bucket", c.URL, c.URL)
	}
	if err := davsync.Check(c); err != nil {
		return err
	}
	if k.s3 && strings.TrimSpace(c.User) == "" {
		return errors.New("no access key: access-key-id=…, the secret is asked for")
	}
	// the password saved goes only to the server and user it was given
	// for: Configure keeps it there, and says when one has to be typed
	c.Password = ""
	if kept, needed := davsync.SavedPassword(c); needed || (c.User != "" || k.s3) && !kept {
		askPass = true
	}
	var err error
	if askPass {
		prompt := "Password for " + host(c.URL) + ": "
		if k.s3 {
			prompt = "Secret for the access key " + c.User + ": "
		}
		if c.Password, err = secret(secretName, prompt, false); err != nil {
			return err
		}
	}
	if askPhrase {
		if c.Passphrase, err = passphrase("Passphrase (the same on every computer): ", true); err != nil {
			return err
		}
	}
	if err := davsync.Configure(c); err != nil {
		return err
	}
	// and sync at once, so a wrong address or password shows now
	if err := syncNow(k); err != nil {
		fmt.Fprintln(os.Stderr, muted.Render(fmt.Sprintf("It is on: magpie %s set k=v… changes it, magpie %s off turns it off.", k.cmd, k.cmd)))
		return err
	}
	return nil
}

// s3Parts are an s3:// address's bucket and prefix.
func s3Parts(address string) (bucket, prefix string) {
	rest, _ := strings.CutPrefix(strings.TrimSpace(address), "s3://")
	bucket, prefix, _ = strings.Cut(rest, "/")
	return bucket, strings.Trim(prefix, "/")
}

func host(address string) string {
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		return u.Host
	}
	return address
}

// syncNow syncs once, then says how it went.
func syncNow(k syncKind) error {
	if _, ok := davsync.Load(); !ok {
		return fmt.Errorf("%s sync is off: %s", k.name, k.turnOn())
	}
	// no time limit: davsync ends a request that stops moving (#657)
	if err := davsync.SyncNow(context.Background()); err != nil {
		return fmt.Errorf("couldn't sync: %w", err)
	}
	return syncShow(k)
}

// syncShow is sync as Settings shows it, whichever kind is on: never the
// secrets.
func syncShow(k syncKind) error {
	v := davsync.Status()
	if !v.On {
		fmt.Println(k.name + " sync is off")
		if k.s3 {
			fmt.Println(muted.Render("magpie s3 on s3://<bucket> keeps providers, settings, profiles, agents' models and the library the same on every computer (magpie s3 help)"))
		} else {
			fmt.Println(muted.Render("magpie webdav on <address> keeps providers, settings, profiles, agents' models and the library the same on every computer (magpie webdav help)"))
		}
		return nil
	}
	cmd := "webdav"
	var who []string
	if v.Kind == "s3" {
		cmd = "s3"
		at := "AWS"
		if e := v.Endpoint; e != "" {
			if !strings.Contains(e, "://") {
				e = "https://" + e
			}
			at = host(e)
		}
		who = append(who, at)
		if v.Region != "" {
			who = append(who, v.Region)
		}
		if v.PathStyle {
			who = append(who, "path-style")
		}
		if v.User != "" {
			who = append(who, "access key "+v.User)
		}
		if v.PasswordSet {
			who = append(who, "secret saved")
		}
		fmt.Println(bold.Render("S3 sync"), v.URL, muted.Render(strings.Join(who, " · ")))
	} else {
		if v.User != "" {
			who = append(who, v.User)
		}
		if v.PasswordSet {
			who = append(who, "password saved")
		}
		fmt.Println(bold.Render("WebDAV sync"), v.URL, muted.Render(strings.Join(who, " · ")))
	}

	what := []string{"providers without their API keys"}
	if v.Keys {
		what[0] = "providers with their API keys"
	}
	what = append(what, "settings", "profiles")
	if v.Agents {
		what = append(what, "agents' models")
	}
	if v.Library {
		what = append(what, "library")
	}
	if v.Usage {
		what = append(what, "usage")
	}
	fmt.Println("  syncs", strings.Join(what, ", "))
	if o := v.Other; o != nil {
		if o.Kind == "s3" {
			fmt.Println(muted.Render("  S3 " + o.URL + " is kept, not synced to: magpie s3 on moves back to it"))
		} else {
			fmt.Println(muted.Render("  WebDAV " + o.URL + " is kept, not synced to: magpie webdav on moves back to it"))
		}
	}

	switch {
	case v.Error != "":
		fmt.Println("  couldn't sync:", v.Error)
	case v.Last.IsZero():
		fmt.Println("  " + muted.Render("not synced yet"))
	default:
		fmt.Println(" ", green.Render("✓"), "synced", syncWhen(v.Last))
	}
	if v.UsageError != "" {
		fmt.Println("  couldn't share usage:", v.UsageError)
	}
	if n := v.Notice; n != nil && n.Restored {
		fmt.Println("  Restored from the server:", partNames(n.Here))
		fmt.Println(muted.Render("  This computer's setup from before is kept in " + tilde(n.Saved) + " (magpie " + cmd + " undo puts it back; magpie " + cmd + " dismiss clears this)"))
	} else if n != nil {
		if len(n.Here) > 0 {
			fmt.Println("  Replaced here by newer ones from another computer:", partNames(n.Here))
		}
		if len(n.There) > 0 {
			fmt.Println("  Replaced on the server by this computer's newer ones:", partNames(n.There))
		}
		dir := n.Saved
		if dir == "" {
			dir = filepath.Join(settings.Dir(), "sync")
		}
		fmt.Println(muted.Render("  The copies replaced are kept in " + tilde(dir) + " (magpie restore opens one; magpie " + cmd + " dismiss clears this)"))
	}
	switch {
	case v.Auto == 0:
		fmt.Println(muted.Render("  Syncs only when asked: magpie " + cmd + " now syncs once (magpie " + cmd + " auto 3 syncs by itself again)"))
	case !gateway.Running():
		fmt.Println(muted.Render(fmt.Sprintf("  No gateway runs here: nothing syncs by itself until one does (the app, magpie web or magpie serve), every %d minutes; magpie %s now syncs once", v.Auto, cmd)))
	default:
		fmt.Println(muted.Render(fmt.Sprintf("  Syncs by itself every %d minutes (magpie %s auto changes it)", v.Auto, cmd)))
	}
	return nil
}

func syncWhen(t time.Time) string {
	if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return t.Format("at 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// partNames are davsync's parts, as Settings names them.
func partNames(ps []string) string {
	names := map[string]string{"agents": "agents' models"}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p
		if n, ok := names[p]; ok {
			out[i] = n
		}
	}
	return strings.Join(out, ", ")
}
