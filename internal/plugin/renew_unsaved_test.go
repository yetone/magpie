package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A renewal whose save failed lost what the vendor gave: plugin-auth.json
// still had the refresh token the vendor had spent, and the next renewal
// sent it again. A vendor that spends a refresh token once (MiniMax)
// answered invalid_grant, and the account was signed out (Tiemo404 on X).
// What the vendor gave is kept and saved at the next try, with nothing
// asked of the vendor again.
func TestRenewalWhoseSaveFailedIsSavedNotSpentAgain(t *testing.T) {
	sandbox(t)
	v := newRaceVendor(t)
	said := make(chan string, 16)
	OnSignIn(func(_, _, s string) { said <- s })
	t.Cleanup(func() { OnSignIn(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := raceAccount(t, ctx, "r1")

	// plugin-auth.json can't be replaced: the copy kept before each write
	// (plugin-auth.json.bak) is a folder, so every write fails
	bak := AuthPath() + ".bak"
	if err := os.RemoveAll(bak); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bak, "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	raceSend(ctx, key) // its renewal's save fails; the request may too
	Told()
	if exchanges, _ := v.seen(); len(exchanges) != 1 || exchanges[0] != "r1" {
		t.Fatalf("exchanges %v, want r1 once", exchanges)
	}
	if a := raceStored(key); a["refresh"] != "r1" {
		t.Fatalf("stored %v: the save was meant to fail", a)
	}
	select {
	case s := <-said:
		t.Fatalf("a renewal whose save failed said %q", s)
	default:
	}

	// the file can be written again: the next request saves what r1 gave
	if err := os.RemoveAll(bak); err != nil {
		t.Fatal(err)
	}
	if err := raceSend(ctx, key); err != nil {
		t.Fatal(err)
	}
	if s := waitSaid(t, said); s != "renewed" {
		exchanges, _ := v.seen()
		t.Fatalf("said %q after exchanges %v: the spent token was sent again", s, exchanges)
	}
	exchanges, auths := v.seen()
	if len(exchanges) != 1 {
		t.Fatalf("exchanges %v: the spent r1 was sent again", exchanges)
	}
	if a := raceStored(key); a["refresh"] != "r2" || a["access"] != "a-r2" {
		t.Fatalf("stored %v, want what r1 gave", a)
	}
	if auths[len(auths)-1] != "Bearer a-r2" {
		t.Fatalf("requests went with %v, want a-r2 last", auths)
	}
}
