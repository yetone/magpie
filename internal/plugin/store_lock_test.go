package plugin

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// Two magpies — the gateway that runs all day, and one run from a
// terminal (`magpie plugin options …`, the plugin manager) — write
// plugins.json from separate processes, and the in-process listMu never
// saw the other one (#4666): the later whole-list write went out with
// what the other had saved since its read. A change is now made under
// the file's lock, so the one waiting reads what the other wrote before
// it goes on, and both changes stand.

func TestPluginOptionsKeepWhatAnotherMagpieSetMeanwhile(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"p-a"},{"spec":"p-b","options":{"x":1}}]}`)
	unlock, held := filelock.Lock(listPath()+".lock", filelock.Wait)
	if !held {
		t.Skip("this file system holds no locks")
	}
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- SetOptions("p-a", map[string]any{"ours": true}) }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("SetOptions went on while another magpie held plugins.json's lock (err %v)", err)
	default:
	}
	// the other magpie's save lands meanwhile: its own options for p-b
	other := Load()
	for i := range other.Plugins {
		if other.Plugins[i].Spec == "p-b" {
			other.Plugins[i].Options = map[string]any{"theirs": float64(1)}
		}
	}
	if err := save(other); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	l := Load()
	var a, b *Entry
	for i := range l.Plugins {
		switch l.Plugins[i].Spec {
		case "p-a":
			a = &l.Plugins[i]
		case "p-b":
			b = &l.Plugins[i]
		}
	}
	if a == nil || a.Options["ours"] != true {
		t.Errorf("p-a's options = %v, want ours kept", a)
	}
	if b == nil || b.Options["theirs"] != float64(1) {
		t.Errorf("p-b's options = %v, want what the other magpie saved kept", b)
	}
}

func TestPluginPreferKeepsWhatAnotherMagpieSetMeanwhile(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"p-a"},{"spec":"p-b"}]}`)
	unlock, held := filelock.Lock(listPath()+".lock", filelock.Wait)
	if !held {
		t.Skip("this file system holds no locks")
	}
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- Prefer("p-a", "prov") }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Prefer went on while another magpie held plugins.json's lock (err %v)", err)
	default:
	}
	// the other magpie's save lands meanwhile: p-b turned off
	other := Load()
	for i := range other.Plugins {
		if other.Plugins[i].Spec == "p-b" {
			other.Plugins[i].Off = true
		}
	}
	if err := save(other); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	l := Load()
	var a, b *Entry
	for i := range l.Plugins {
		switch l.Plugins[i].Spec {
		case "p-a":
			a = &l.Plugins[i]
		case "p-b":
			b = &l.Plugins[i]
		}
	}
	if a == nil || l.Prefer["prov"] != "p-a" {
		t.Errorf("prefer = %v, want p-a kept", l.Prefer)
	}
	if b == nil || !b.Off {
		t.Errorf("p-b off = %v, want what the other magpie saved kept", b)
	}
}
