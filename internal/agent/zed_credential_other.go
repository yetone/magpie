//go:build !windows

package agent

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

func saveZedCredential(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "security"
		// GPUI uses an Internet Password whose server is the full API URL.
		args = []string{"add-internet-password", "-U", "-s", url, "-a", "Bearer", "-w", gateway.TokenFor("zed")}
	case "linux", "freebsd":
		name = "secret-tool"
		// GPUI searches Secret Service by URL and this historical label.
		args = []string{"store", "--label=zed-github-account", "url", url, "username", "Bearer"}
	default:
		return fmt.Errorf("unsupported system credential store on %s", runtime.GOOS)
	}
	cmd := proc.CommandContext(ctx, name, args...)
	if name == "secret-tool" {
		cmd.Stdin = strings.NewReader(gateway.TokenFor("zed"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
