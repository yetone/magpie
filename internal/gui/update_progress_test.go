package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// esir_tech on Discord: after Update, the pill said 17% and sat there.
// While a download goes on, /api/update says its step and how fast the
// bytes come, so the pill and Settings' row show it moving.
func TestUpdateDownloadSaysStepAndSpeed(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))

	const chunk, chunks = 8 << 10, 40
	body := make([]byte, chunk*chunks)
	for i := range body {
		body[i] = byte(i * 7)
	}
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			for i := 0; i < chunks; i++ {
				if _, err := w.Write(body[i*chunk : (i+1)*chunk]); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(60 * time.Millisecond)
			}
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	Version = "0.1.10"
	defer func() { Version = oldV }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer func() {
		u.mu.Lock()
		staged := u.staged
		u.mu.Unlock()
		if staged != "" {
			os.Remove(staged)
		}
	}()
	if !u.begin() {
		t.Fatal("no check began")
	}
	ran := make(chan struct{})
	go func() { u.run(); close(ran) }()

	var moving []updateJSON
	deadline := time.After(time.Minute)
	for watching := true; watching; {
		select {
		case <-ran:
			watching = false
		case <-deadline:
			t.Fatalf("still downloading: %+v", u.json())
		case <-time.After(20 * time.Millisecond):
			if j := u.json(); j.State == "downloading" && j.Step == update.StepDownload && j.Speed > 0 {
				moving = append(moving, j)
			}
		}
	}
	if j := u.json(); j.State != "ready" {
		t.Fatalf("after the download: %+v", j)
	}
	if len(moving) == 0 {
		t.Fatal("the download never said how fast it went")
	}
	for _, j := range moving {
		// at most every byte in the shortest span speedOf takes, a second
		if j.Done <= 0 || j.Total != int64(len(body)) || j.Speed > j.Total {
			t.Fatalf("while downloading: %+v", j)
		}
	}
}

// A download's speed is over its last seconds: none until a second is
// known, and falling to 0 while no bytes come.
func TestUpdateSpeedFallsWhenStalled(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	var marks []progMark
	for i := range 41 { // 100 KB every 100ms for 4s
		marks = marked(marks, int64(i)*100_000, t0.Add(time.Duration(i)*100*time.Millisecond))
	}
	at := t0.Add(4 * time.Second)
	if s := speedOf(marks, 4_000_000, at); s < 900_000 || s > 1_100_000 {
		t.Fatalf("speed = %d, want about 1 MB/s", s)
	}
	if s := speedOf(marked(nil, 5, t0), 5, t0.Add(500*time.Millisecond)); s != 0 {
		t.Fatalf("speed after half a second = %d, want none yet", s)
	}
	// stalled: the bytes stay at 4 MB
	if s := speedOf(marks, 4_000_000, at.Add(3*time.Second)); s <= 0 || s >= 900_000 {
		t.Fatalf("speed 3s into a stall = %d, want falling", s)
	}
	if s := speedOf(marks, 4_000_000, at.Add(10*time.Second)); s != 0 {
		t.Fatalf("speed 10s into a stall = %d, want 0", s)
	}
	// a host that sent the file again from its start: the marks before go
	if m := marked(marks, 1000, at.Add(time.Second)); len(m) != 1 {
		t.Fatalf("marks after the bytes went back = %d, want 1", len(m))
	}
}

// The update's check is bounded (checkWait), its download isn't: one
// still moving past that bound finishes (#5572, an 83 MB download under
// ~140 KB/s failed at 10 minutes part way). Here the bound is 200ms and
// the bytes take ~600ms.
func TestUpdateSlowDownloadOutlastsTheCheckBound(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	old := checkWait
	checkWait = 200 * time.Millisecond
	t.Cleanup(func() { checkWait = old })

	const chunk, chunks = 4 << 10, 10
	body := make([]byte, chunk*chunks)
	for i := range body {
		body[i] = byte(i * 5)
	}
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			for i := 0; i < chunks; i++ {
				if _, err := w.Write(body[i*chunk : (i+1)*chunk]); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(60 * time.Millisecond)
			}
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.11", Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	Version = "0.1.10"
	defer func() { Version = oldV }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer func() {
		u.mu.Lock()
		staged := u.staged
		u.mu.Unlock()
		if staged != "" {
			os.Remove(staged)
		}
	}()
	if !u.begin() {
		t.Fatal("no check began")
	}
	u.run()
	if j := u.json(); j.State != "ready" {
		t.Fatalf("after the download: %+v", j)
	}
}
