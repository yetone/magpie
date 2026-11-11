package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// healthcheck: magpie healthcheck [--addr host:port] — exits 0 when a
// magpie gateway answers, for a container's HEALTHCHECK, which has no curl to
// ask it with. It asks where --addr says, else MAGPIE_ADDR, else where the
// running gateway wrote down it listens (gateway.ClientURL), else the port in
// Settings' Gateway address on 127.0.0.1, 3425 unless set.
func healthcheck(args []string) error {
	base, err := clientBase("healthcheck", args)
	if err != nil {
		return err
	}
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get(base + "/")
	if err != nil {
		return fmt.Errorf("no gateway at %s: %w", base, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK || !bytes.Contains(b, []byte(`"magpie"`)) {
		return fmt.Errorf("%s answers %s, not as a magpie gateway", base, res.Status)
	}
	return nil
}

// clientBase is the gateway a command asks: --addr host:port when args has
// it, else gateway.ClientURL. A command that takes no other argument
// refuses one rather than run with it unread.
func clientBase(name string, args []string) (string, error) {
	rest, addr, err := addrFlag(args)
	if err != nil {
		return "", fmt.Errorf("magpie %s: %w", name, err)
	}
	if len(rest) > 0 {
		return "", fmt.Errorf("magpie %s: unknown %q · usage: magpie %s [--addr host:port]", name, rest[0], name)
	}
	if addr == "" {
		return gateway.ClientURL(), nil
	}
	return addr, nil
}

// addrFlag takes --addr host:port (or --addr=host:port) out of args and
// returns the base URL it names, with a gateway on every interface reached
// on loopback. A value that isn't host:port is an error, never an address.
func addrFlag(args []string) (rest []string, base string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, eq := strings.Cut(a, "=")
		if name != "--addr" {
			rest = append(rest, a)
			continue
		}
		if !eq {
			if i+1 >= len(args) {
				return nil, "", errors.New("--addr needs host:port")
			}
			i++
			val = args[i]
		}
		h, p, err := net.SplitHostPort(val)
		if err != nil {
			return nil, "", fmt.Errorf("--addr %q isn't host:port", val)
		}
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, "", fmt.Errorf("--addr %q has no port from 1 to 65535", val)
		}
		if ip := net.ParseIP(h); h == "" || ip != nil && ip.IsUnspecified() {
			h = "127.0.0.1"
		}
		base = "http://" + net.JoinHostPort(h, p)
	}
	return rest, base, nil
}

// makeDirs creates $HOME and the XDG folders the environment names when
// they don't exist yet. The Docker image points them all into its volume
// (/config/home, /config/cache…), and a volume made by an older image has
// none of them: whatever writes there first must not find its parent
// missing.
func makeDirs() {
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		d := os.Getenv(k)
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
			_ = os.MkdirAll(d, 0o700)
		}
	}
}
