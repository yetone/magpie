package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// `magpie update` said "downloading …" and then nothing until the file
// was in (@ahz2ygnhjmIJ0E0 on X). It now draws how far along the download
// is and how fast, through the real downloader from a fake release: on a
// terminal one line redrawn in place and ended, elsewhere plain lines; a
// file of no known size shows its bytes without a percent.
func TestUpdateDownloadShowsProgress(t *testing.T) {
	body := bytes.Repeat([]byte("magpie-"), 3_000_000/7)
	sum := sha256.Sum256(body)
	for _, c := range []struct {
		name  string
		tty   bool
		sized bool
	}{
		{"terminal", true, true},
		{"terminal, no size", true, false},
		{"pipe", false, true},
		{"pipe, no size", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/asset":
					if !c.sized {
						// chunked: no Content-Length
						for b := body; len(b) > 0; {
							n := min(len(b), 256<<10)
							w.Write(b[:n])
							w.(http.Flusher).Flush()
							b = b[n:]
						}
						return
					}
					http.ServeContent(w, r, "magpie", time.Time{}, bytes.NewReader(body))
				case gateway.RestartPath:
					http.NotFound(w, r)
				default:
					a := update.Asset{URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])}
					if c.sized {
						a.Size = int64(len(body))
					}
					json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", URL: "https://github.com/yetone/magpie-releases/releases/tag/v0.1.11",
						Assets: map[string]update.Asset{update.BinaryAsset(): a}})
				}
			}))
			defer srv.Close()
			t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
			exe := filepath.Join(t.TempDir(), "magpie")
			if err := os.WriteFile(exe, []byte("magpie"), 0o755); err != nil {
				t.Fatal(err)
			}
			out := &syncBuf{}
			var got []byte
			wasV, wasX, wasE, wasP, wasC, wasR, wasO := version, updateExecutable, updateCanElevate, updatePackagedBy, updateInContainer, updateReplaceBinary, updateProgressOut
			t.Cleanup(func() {
				version, updateExecutable, updateCanElevate, updatePackagedBy, updateInContainer, updateReplaceBinary, updateProgressOut = wasV, wasX, wasE, wasP, wasC, wasR, wasO
			})
			version = "0.1.10"
			updateExecutable = func() (string, error) { return exe, nil }
			updateCanElevate = func() bool { return true }
			updateInContainer = func() bool { return false }
			updatePackagedBy = func(string) *update.Packaged { return nil }
			updateProgressOut = func() (io.Writer, bool) { return out, c.tty }
			// the real download, staged and read back; the test binary
			// itself is never replaced
			updateReplaceBinary = func(ctx context.Context, rel *update.Release) error {
				staged, err := update.StageBinary(ctx, rel)
				if staged != "" {
					got, _ = os.ReadFile(staged)
					os.Remove(staged)
				}
				return err
			}
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
			if err := updateCmd([]string{"update", "--addr", "127.0.0.1:" + port}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("staged %d bytes, want %d", len(got), len(body))
			}
			s := out.String()
			if s == "" || !strings.HasSuffix(s, "\n") {
				t.Fatalf("progress not drawn and ended: %q", s)
			}
			if c.tty != strings.Contains(s, "\r") {
				t.Errorf("redrawn in place: %v, want %v: %q", strings.Contains(s, "\r"), c.tty, s)
			}
			lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
			if !c.tty && len(lines) > 4 {
				t.Errorf("%d plain lines, want 4 at most: %q", len(lines), s)
			}
			if c.tty && len(lines) != 1 {
				t.Errorf("the terminal line was ended %d times: %q", len(lines), s)
			}
			// the last thing drawn is the whole file
			last := lines[len(lines)-1]
			if i := strings.LastIndex(last, "\r"); i >= 0 {
				last = last[i+1:]
			}
			last = strings.TrimRight(last, " ")
			want := "  3.0 MB"
			if c.sized {
				want = "  100% 3.0/3.0 MB"
			}
			if !strings.HasPrefix(last, want) {
				t.Errorf("last line %q, want it to start %q", last, want)
			}
			if !c.sized && strings.Contains(s, "%") {
				t.Errorf("a percent with no size known: %q", s)
			}
		})
	}
}

// The terminal line is redrawn about ten times a second, not on every
// read; its speed is the bytes over the time they took; and it is ended
// once, with a newline, when the bytes are in.
func TestDownloadProgressLine(t *testing.T) {
	var out bytes.Buffer
	at := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	p := &dlProgress{w: &out, tty: true, now: func() time.Time { return at }}
	const total = 43_600_000
	p.report(0, total)
	// a read every 10ms for 4s, 5.2 MB a second
	var done int64
	for i := 1; i <= 400; i++ {
		at = at.Add(10 * time.Millisecond)
		done = min(int64(i)*52_000, total)
		p.report(done, total)
	}
	s := out.String()
	if n := strings.Count(s, "\r"); n < 30 || n > 45 {
		t.Errorf("drawn %d times in 4s, want about 40", n)
	}
	if strings.Contains(s, "\n") {
		t.Errorf("ended before the bytes were in: %q", s)
	}
	last := s[strings.LastIndex(s, "\r")+1:]
	if want := "   47% 20.8/43.6 MB · 5.2 MB/s"; strings.TrimRight(last, " ") != want {
		t.Errorf("line %q, want %q", last, want)
	}
	at = at.Add(10 * time.Millisecond)
	p.report(total, total)
	p.step(update.StepVerify)
	p.end()
	if s := out.String(); !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		t.Errorf("not ended once: %q", s)
	}
	if s := out.String(); !strings.Contains(s[strings.LastIndex(s, "\r"):], "100% 43.6/43.6 MB") {
		t.Errorf("no whole line at the end: %q", s[strings.LastIndex(s, "\r"):])
	}

	// no size known: bytes and speed, no percent
	out.Reset()
	p = &dlProgress{w: &out, tty: true, now: func() time.Time { return at }}
	p.report(0, 0)
	at = at.Add(time.Second)
	p.report(2_000_000, 0)
	if s := strings.TrimRight(out.String()[strings.LastIndex(out.String(), "\r")+1:], " "); s != "  2.0 MB · 2.0 MB/s" {
		t.Errorf("no size: %q", s)
	}
	p.end()
	if !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("no size: not ended: %q", out.String())
	}
}
