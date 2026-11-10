package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

const mtlsUsage = `usage: magpie mtls [on|off|issue <device> [--out <folder>]|list|revoke <device or serial>]
  other computers reach this magpie only with a client certificate it issued (mTLS;
  hhdaxia on X, #5596). On, the gateway and magpie web's page are served over https://
  to them with magpie's own CA (or the --tls-cert one), and a request without a good
  certificate is refused, a gateway key or not. This computer, and a WSL distro's agent
  with its key, are answered as before.
    magpie mtls               whether it is on, and the certificates issued
    magpie mtls on|off        require a client certificate from other computers, or stop
    magpie mtls issue <name>  a certificate for one device: client.pem, client-key.pem and
                              ca.pem in Downloads/magpie-<name> (or --out <folder>). Give the
                              device all three; the key is not kept here. Issuing a name
                              again revokes the one before.
    magpie mtls revoke <name> refuses that device's certificate from now on
  In Remote magpie (or a provider) on the other computer, set Client certificate, Client key
  and Server CA to the three files. curl: --cert client.pem --key client-key.pem --cacert ca.pem.`

func mtlsCmd(args []string) error {
	st := gateway.MTLSStore()
	if len(args) == 0 || args[0] == "list" || args[0] == "status" {
		return mtlsList(st)
	}
	switch args[0] {
	case "on", "off":
		on := args[0] == "on"
		if on {
			if _, _, err := st.CA(); err != nil {
				return err
			}
		}
		if err := settings.SetMTLS(on); err != nil {
			return err
		}
		if !on {
			fmt.Println("mTLS off: other computers are answered as before (a gateway key, while shared)")
			return nil
		}
		fmt.Println("mTLS on: other computers need a client certificate magpie issued; this computer is answered as before")
		if l, _ := st.List(); len(l) == 0 {
			fmt.Println("no certificate issued yet: magpie mtls issue <device>")
		}
		return nil
	case "issue":
		rest, out, err := outFlag(args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: magpie mtls issue <device> [--out <folder>]")
		}
		iss, err := st.Issue(rest[0])
		if err != nil {
			return err
		}
		if out == "" {
			out = downloadsDir()
		}
		dir, err := mtls.Export(iss, out)
		if err != nil {
			return err
		}
		fmt.Printf("issued %q (serial %s, valid until %s)\n", iss.Name, iss.Serial, iss.Expires.Format("2006-01-02"))
		fmt.Println("  " + dir + string(filepath.Separator) + " client.pem, client-key.pem, ca.pem")
		if !gateway.MTLSOn() {
			fmt.Println("mTLS is off: magpie mtls on to require it")
		}
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: magpie mtls revoke <device or serial>")
		}
		n, err := st.Revoke(args[1])
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no certificate issued to %q that isn't revoked already", args[1])
		}
		fmt.Printf("revoked %d certificate(s) for %q\n", n, args[1])
		return nil
	}
	return fmt.Errorf("magpie mtls: unknown %q\n%s", args[0], mtlsUsage)
}

func mtlsList(st mtls.Store) error {
	state := "off"
	if gateway.MTLSOn() {
		state = "on"
	}
	fmt.Println("mTLS " + state)
	l, err := st.List()
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range l {
		why := "valid until " + c.Expires.Format("2006-01-02")
		switch {
		case !c.Revoked.IsZero():
			why = "revoked " + c.Revoked.Format("2006-01-02")
		case !c.Good(now):
			why = "ran out " + c.Expires.Format("2006-01-02")
		}
		fmt.Printf("  %-24s %s  issued %s · %s\n", c.Name, c.Serial, c.Issued.Format("2006-01-02"), why)
	}
	return nil
}

// outFlag takes --out <folder> (or --out=<folder>) out of args.
func outFlag(args []string) (rest []string, out string, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--out":
			if i+1 >= len(args) {
				return nil, "", errors.New("--out takes a folder")
			}
			i++
			out = args[i]
		case strings.HasPrefix(a, "--out="):
			out = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "-"):
			return nil, "", fmt.Errorf("unknown flag %s", a)
		default:
			rest = append(rest, a)
		}
	}
	return rest, out, nil
}

// downloadsDir is the Downloads folder the system keeps, else
// home/Downloads, else this folder.
func downloadsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{appdir.Downloads(), filepath.Join(home, "Downloads")} {
		if fi, err := os.Stat(d); d != "" && err == nil && fi.IsDir() {
			return d
		}
	}
	return "."
}
