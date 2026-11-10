package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

const httpsUsage = `usage: magpie https [on|off|import <cert.pem> [<key.pem>…]|remove|ca [--out <folder>]]
  other computers reach this magpie's gateway, and magpie web's page, at https://
  (zhangly1 on X: Claude on another computer on the LAN). This computer, and a WSL
  distro's agent, may still call plain http://.
    magpie https              whether it is on, the certificate served and magpie's CA
    magpie https on|off       serve other computers over https://, or plain http:// again
    magpie https import <files>  serve your own certificate (its chain after it) and key,
                              PEM, in one file or several; kept in magpie's folder
    magpie https remove       back to a certificate magpie's own CA signs for this
                              computer's names and addresses
    magpie https ca           save magpie's CA as magpie-ca.crt in Downloads (or --out)
  Without a certificate of your own, each computer that calls magpie trusts its CA once.
  Get it from Settings, or on that computer from https://<this computer>:<port>/magpie-ca.crt
  (check its SHA-256 against the one magpie https prints), then:
    macOS    sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain magpie-ca.crt
    Windows  certutil -addstore -f Root magpie-ca.crt   (as administrator)
    Linux    sudo cp magpie-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates
    Node apps such as Claude Code: NODE_EXTRA_CA_CERTS=/path/to/magpie-ca.crt`

func httpsCmd(args []string) error {
	if len(args) == 0 || args[0] == "status" {
		return httpsStatus()
	}
	switch args[0] {
	case "on", "off":
		on := args[0] == "on"
		if on {
			if c, _ := gateway.ImportedFiles(); c == "" {
				if _, _, err := gateway.CA(); err != nil {
					return err
				}
			}
		}
		if err := settings.SetHTTPS(on); err != nil {
			return err
		}
		if !on {
			fmt.Println("HTTPS off: other computers call magpie at http:// again")
			if gateway.TLSOn() {
				fmt.Println("(still served over https://: --tls-cert or mTLS is on)")
			}
			return nil
		}
		fmt.Println("HTTPS on: other computers call magpie at https://; this computer may still call http://")
		return httpsStatus()
	case "import":
		if len(args) < 2 {
			return errors.New("usage: magpie https import <cert.pem> [<key.pem>…]")
		}
		var files [][]byte
		for _, f := range args[1:] {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			files = append(files, b)
		}
		if err := gateway.ImportCert(files...); err != nil {
			return err
		}
		fmt.Println("imported: other computers are served this certificate over https://")
		if !gateway.HTTPSOn() && !gateway.TLSOn() {
			fmt.Println("HTTPS is off: magpie https on to serve it")
		}
		return httpsStatus()
	case "remove":
		if err := gateway.RemoveImportedCert(); err != nil {
			return err
		}
		fmt.Println("removed: magpie's CA signs the certificate served")
		return nil
	case "ca":
		rest, out, err := outFlag(args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("usage: magpie https ca [--out <folder>]")
		}
		b, fp, err := gateway.CA()
		if err != nil {
			return err
		}
		if out == "" {
			out = downloadsDir()
		}
		p := filepath.Join(out, "magpie-ca.crt")
		if err := mtls.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		fmt.Println(p)
		fmt.Println("  SHA-256 " + fp)
		return nil
	}
	return fmt.Errorf("magpie https: unknown %q\n%s", args[0], httpsUsage)
}

func httpsStatus() error {
	state := "off"
	if gateway.HTTPSOn() {
		state = "on"
	}
	fmt.Println("HTTPS " + state)
	if !gateway.TLSOn() {
		return nil
	}
	c, err := gateway.ServedCert()
	if err != nil {
		return err
	}
	from := map[string]string{"files": "--tls-cert " + c.File, "imported": "imported", "magpie": "magpie's CA"}[c.From]
	fmt.Printf("  certificate  %s · %s · until %s\n", from, strings.Join(c.Names, ", "), c.Until.Format("2006-01-02"))
	if c.From == "magpie" {
		_, fp, err := gateway.CA()
		if err != nil {
			return err
		}
		fmt.Println("  CA           " + gateway.MTLSStore().CAFile())
		fmt.Println("  SHA-256      " + fp)
		fmt.Println("  trust it on each computer that calls magpie: magpie https --help")
	}
	return nil
}
