//go:build !windows

package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// Run the web command with main's signal handler, not just Web.Wait:
// the global probe cleanup used to kill it before it could shut down.
func TestWebShutdown(t *testing.T) {
	if os.Getenv("MAGPIE_TEST_WEB_SHUTDOWN") != "" {
		os.Args = []string{"magpie", "web", "--addr", "127.0.0.1:0", "--no-open"}
		main()
		return
	}
	for _, mode := range []string{"SIGTERM", "SIGINT", "SIGHUP", "quit"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "config", "magpie")
			if err := os.MkdirAll(config, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"noAutoUpdate":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^TestWebShutdown$")
			cmd.Env = []string{
				"MAGPIE_TEST_WEB_SHUTDOWN=1", "HOME=" + home, "USERPROFILE=" + home,
				"XDG_CONFIG_HOME=" + filepath.Dir(config), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
				"XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
				"PATH=" + home, "NO_COLOR=1", "MAGPIE_ADDR=127.0.0.1:0",
				"MAGPIE_WEB_KEY=web-shutdown-test-key", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
				testenv.Marker + "=" + os.Getenv(testenv.Marker), "TMPDIR=" + os.Getenv("TMPDIR"),
			}
			log, err := os.Create(filepath.Join(home, "web.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
				close(done)
			}()
			defer func() {
				cancel()
				// Always reap the child, including a failed readiness check.
				<-done
			}()
			link := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+/\?k=[a-zA-Z0-9-]+`)
			var url string
			for url == "" {
				b, _ := os.ReadFile(log.Name())
				url = link.FindString(string(b))
				select {
				case err := <-done:
					t.Fatalf("web exited before ready: %v\n%s", err, b)
				case <-time.After(10 * time.Millisecond):
				}
			}
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar, Timeout: 3 * time.Second}
			resp, err := client.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("web readiness: HTTP %d", resp.StatusCode)
			}
			if mode == "quit" {
				resp, err := client.Post(resp.Request.URL.String()+"api/window/quit", "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					t.Fatalf("quit: HTTP %d", resp.StatusCode)
				}
			} else {
				sig := map[string]os.Signal{"SIGTERM": syscall.SIGTERM, "SIGINT": os.Interrupt, "SIGHUP": syscall.SIGHUP}[mode]
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
			}
			err = <-done
			b, _ := os.ReadFile(log.Name())
			if ctx.Err() != nil {
				t.Fatalf("web did not stop: %v\n%s", ctx.Err(), b)
			}
			if mode == "SIGHUP" {
				if err == nil {
					t.Fatal("hangup should still terminate the process, not become a clean shutdown")
				}
			} else if err != nil {
				t.Fatalf("%s should exit successfully: %v\n%s", mode, err, b)
			}
		})
	}
}
