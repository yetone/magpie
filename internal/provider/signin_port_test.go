package provider

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// #1782 (nianlee-official): port 1455, where ChatGPT sends the browser back,
// couldn't be opened though nothing of ChatGPT's ran, and the sign-in
// stopped there with "is busy". The sign-in goes on: it says why the port
// can't be had, and the address the browser ends on, pasted, finishes it.
func TestCodexSignInGoesOnWithItsPortTaken(t *testing.T) {
	home := claudeHome(t)
	trades := fakeCodexTokens(t, nil)
	// another program on the port, which isn't a Codex sign-in: it doesn't
	// stop when asked to. Its port is the callback's (fakeCodexTokens put
	// the old one back when the test ends)
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	codexCallbackAddr = other.Addr().String()
	holder := &http.Server{Handler: http.NotFoundHandler()}
	go holder.Serve(other)
	t.Cleanup(func() { holder.Close() })

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatalf("the sign-in stopped at the taken port: %v", err)
	}
	defer CancelSignIn(st.ID)
	_, port, _ := net.SplitHostPort(codexCallbackAddr)
	if st.State != "waiting" || !st.PasteCallback || st.CallbackBlocked != "busy" || strconv.Itoa(st.CallbackPort) != port {
		t.Fatalf("state %+v", st)
	}
	// the page still sends the browser back to the one port ChatGPT allows
	if u, _ := url.Parse(st.URL); u.Query().Get("redirect_uri") != "http://localhost:"+port+"/auth/callback" {
		t.Fatalf("redirect_uri %q", u.Query().Get("redirect_uri"))
	}
	if err := SubmitSignInCallback(st.ID, callbackAddress(t, st, "cx", "")); err != nil {
		t.Fatal(err)
	}
	if st, _ = SignInStatus(st.ID); st.State != "done" || st.User != "cx@example.com" {
		t.Fatalf("state %+v", st)
	}
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r-cx" || trades.Load() != 1 {
		t.Fatalf("auth.json %+v, %d trades", a, trades.Load())
	}
	// the other program's port is left to it
	resp, err := http.Get("http://" + codexCallbackAddr + "/")
	if err != nil {
		t.Fatalf("the other program lost its port: %v", err)
	}
	resp.Body.Close()
}

// Windows refuses a port in a range it excludes for Hyper-V, WSL or Docker
// with WSAEACCES, which is told apart from a port another program holds.
func TestCallbackBlockedTellsReservedFromBusy(t *testing.T) {
	listenErr := func(errno syscall.Errno) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{listenErr(10013), "reserved"}, // WSAEACCES
		{listenErr(10048), "busy"},     // WSAEADDRINUSE
		{listenErr(syscall.EADDRINUSE), "busy"},
		{errors.New("something else"), "busy"},
	} {
		if got := callbackBlocked(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}
