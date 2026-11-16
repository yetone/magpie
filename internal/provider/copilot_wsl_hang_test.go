package provider

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// vsoullai on X, #6167: the Copilot CLI's sign-in in a WSL distro is read
// on every request's path (the accounts), and was read under
// copilotWSL's lock: a WSL that hung held the first request and queued
// every other behind it until the gateway sent 504s. A read that hangs
// holds no request past copilotWSLWait, all of them at once included,
// and the sign-in read before is answered meanwhile.
func TestCopilotWSLHangHoldsNoRequest(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "no-copilot-here"))
	oldSecret, oldDirs, oldWait := copilotCLISecret, CopilotWSLDirs, copilotWSLWait
	copilotCLISecret = func(string) string { return "" }
	copilotWSLWait = 200 * time.Millisecond
	distro := filepath.Join(t.TempDir(), "home", "jia")
	os.MkdirAll(filepath.Join(distro, ".copilot"), 0o700)
	os.WriteFile(filepath.Join(distro, ".copilot", "config.json"), []byte(`{
  "lastLoggedInUser": {"host": "https://github.com", "login": "jia2"},
  "loggedInUsers": [{"host": "https://github.com", "login": "jia2"}],
  "copilotTokens": {"https://github.com:jia2": "gho_wsl"}
}`), 0o600)
	release := make(chan struct{})
	var mu sync.Mutex
	hang := false
	CopilotWSLDirs = func() []string {
		mu.Lock()
		h := hang
		mu.Unlock()
		if h {
			<-release
		}
		return []string{filepath.Join(distro, ".copilot")}
	}
	copilotWSL.Lock()
	copilotWSL.at = time.Time{}
	copilotWSL.Unlock()
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		free()
		for {
			copilotWSL.Lock()
			busy := copilotWSL.done != nil
			copilotWSL.Unlock()
			if !busy {
				break
			}
			time.Sleep(time.Millisecond)
		}
		copilotCLISecret, CopilotWSLDirs, copilotWSLWait = oldSecret, oldDirs, oldWait
		copilotWSL.Lock()
		copilotWSL.at = time.Time{}
		copilotWSL.Unlock()
	})

	if app, ok := copilotCLILogin(); !ok || app.User != "jia2" {
		t.Fatalf("first read: %+v %v", app, ok)
	}
	// older than copilotWSLAge, and WSL hangs now
	mu.Lock()
	hang = true
	mu.Unlock()
	copilotWSL.Lock()
	copilotWSL.at = time.Now().Add(-copilotWSLAge - time.Second)
	copilotWSL.Unlock()

	// a read again answers the one before at once, without waiting
	// copilotWSLWait for it: each wait held the Sessions page's first ask
	// every copilotWSLAge on a slow WSL (shangyuan0818 on X)
	copilotWSLWait = 10 * time.Second
	bar := 2 * time.Second
	var wg sync.WaitGroup
	start := time.Now()
	errs := make(chan string, 13)
	for range 13 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if app, ok := copilotCLILogin(); !ok || app.User != "jia2" {
				errs <- "the sign-in read before wasn't answered"
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(bar):
		t.Errorf("13 requests waited on a hung WSL past %v", bar)
		free()
		<-done
	}
	if d := time.Since(start); d > bar {
		t.Errorf("13 requests at once took %v", d)
	}
	close(errs)
	for e := range errs {
		t.Error(e)
		break
	}
}
