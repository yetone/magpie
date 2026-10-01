package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// healthcheck: magpie healthcheck — exits 0 when a magpie gateway answers
// at MAGPIE_ADDR (127.0.0.1:3425 unless set), for a container's
// HEALTHCHECK, which has no curl to ask it with.
func healthcheck() error {
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get(gateway.URL() + "/")
	if err != nil {
		return fmt.Errorf("no gateway at %s: %w", gateway.URL(), err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK || !bytes.Contains(b, []byte(`"magpie"`)) {
		return fmt.Errorf("%s answers %s, not as a magpie gateway", gateway.URL(), res.Status)
	}
	return nil
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
