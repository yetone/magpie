package plugin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// TestTerminalChangeIsNoticedWithoutARequest (Zerlight0653 on X): `magpie
// plugin off` and `on` in a terminal, with magpie running, rewrite
// plugins.json in another process. The running magpie looked at it only
// when something asked its host, so with no request coming it went on
// with the host and the models it had, and the agents' files kept the
// old model, until `magpie tools` asked the gateway. The process serving
// the gateway sees the write once it is made, as it sees the window's.
func TestTerminalChangeIsNoticedWithoutARequest(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"/plugins/oleafly"}]}`)
	defer ListInBackground(ListInBackground(false)) // no host is started here

	told := make(chan struct{}, 16)
	onChangeMu.Lock()
	was := onChange
	onChange = append(append([]func(){}, was...), func() {
		select {
		case told <- struct{}{}:
		default:
		}
	})
	onChangeMu.Unlock()
	t.Cleanup(func() {
		Told()
		onChangeMu.Lock()
		onChange = was
		onChangeMu.Unlock()
	})
	checkList() // the app has seen the plugins as they are
	Told()
	for len(told) > 0 {
		<-told
	}
	prevStale := hostStale.Swap(false)
	t.Cleanup(func() { hostStale.Store(prevStale) })

	ctx, cancel := context.WithCancel(context.Background())
	watched, noticing := make(chan struct{}), make(chan struct{})
	go func() { defer close(watched); filememo.Watch(ctx, listPath()) }()
	// a look for what the watch missed is far off: only the watch tells
	go func() { defer close(noticing); KeepNoticing(ctx, func() time.Duration { return time.Hour }) }()
	t.Cleanup(func() { cancel(); <-watched; <-noticing })
	deadline := time.Now().Add(10 * time.Second)
	for filememo.OthersEvery() != time.Minute {
		if time.Now().After(deadline) {
			t.Fatal("plugins.json's folder is never watched")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-told:
		t.Fatal("nothing changed, and a change was told")
	case <-time.After(100 * time.Millisecond):
	}

	// the terminal turns the plugin off: another process's write, which
	// no request of this one follows
	at := time.Now().Add(time.Minute) // a stamp of its own, however coarse the clock
	writeList(t, `{"plugins":[{"spec":"/plugins/oleafly","off":true}]}`)
	if err := os.Chtimes(listPath(), at, at); err != nil {
		t.Fatal(err)
	}
	select {
	case <-told:
	case <-time.After(10 * time.Second):
		t.Fatal("another process turned a plugin off, and the running magpie never noticed: its host and models stay as they were until a request asks")
	}
	if !hostStale.Load() {
		t.Fatal("the plugins changed, and the running host is not stale")
	}
}
