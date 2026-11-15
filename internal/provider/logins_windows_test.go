package provider

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/steady"
)

// Another magpie writing logins.json (a temp file renamed over it: the one
// an update is replacing, a second one started) has Windows say, for a
// moment, that it isn't there: read so, it was no accounts, and the write
// that followed left every account out (Packing1 on Discord, Windows 11:
// 「整行都没了，供应商里面antigravity直接消失了」 after an update).
func TestLoginsKeptWhileAnotherMagpieWrites(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	googleSandbox(t, &fakeGoogle{})
	if err := addGoogleLogin("antigravity", "a@x.com", "", googleAuth{RefreshToken: "1//a", Project: "pa"}); err != nil {
		t.Fatal(err)
	}
	p := loginsPath()
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	done := make(chan struct{})
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			for !stop.Load() {
				tmp, err := os.CreateTemp(filepath.Dir(p), ".logins.json.*")
				if err != nil {
					continue
				}
				tmp.Write(body)
				tmp.Close()
				if steady.Rename(tmp.Name(), p) != nil {
					os.Remove(tmp.Name())
				}
			}
		}()
	}
	has := func(ls []savedLogin) bool {
		return slices.ContainsFunc(ls, func(l savedLogin) bool { return l.Agent == "antigravity" && l.User == "a@x.com" })
	}
	misses := 0
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
		loginsMu.Lock()
		if ls := readLogins(); !has(ls) {
			misses++
			// what every change to an account does next: the list read,
			// changed, written back
			_ = writeLogins(ls)
		}
		loginsMu.Unlock()
	}
	stop.Store(true)
	<-done
	<-done
	if !has(readLogins()) {
		t.Fatalf("the Antigravity account is gone from logins.json (read as none %d times)", misses)
	}
	if misses > 0 {
		t.Fatalf("logins.json read as no accounts %d times while another magpie wrote it", misses)
	}
}
