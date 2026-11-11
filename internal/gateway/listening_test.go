package gateway

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// The serving process writes down where it listens and who it is, so a
// command run in another shell without MAGPIE_ADDR (ljlwlcz on X) finds it;
// the note goes when it ends.
func TestServingWritesDownWhereItListens(t *testing.T) {
	fresh(t)
	addr := freeAddr(t)
	t.Setenv("MAGPIE_ADDR", addr)
	was := Serves
	Serves = "web"
	t.Cleanup(func() { Serves = was })
	s := New()
	stop, done := listening(t, s)
	// the note is written just after the socket is up, so wait for it
	var l Listening
	var ok bool
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if l, ok = ReadListening(); ok {
			break
		}
	}
	if !ok || l.Addr != addr || l.Pid != os.Getpid() || l.Serves != "web" {
		t.Fatalf("while serving: %+v %v (want %s)", l, ok, addr)
	}
	// what a client without MAGPIE_ADDR is told
	t.Setenv("MAGPIE_ADDR", "")
	if got := ClientURL(); got != "http://"+addr {
		t.Errorf("ClientURL = %s, want http://%s", got, addr)
	}
	stop()
	<-done
	if _, ok := ReadListening(); ok {
		t.Error("the note stayed after the gateway ended")
	}
}

// A magpie that took the gateway over has written its own note by the time
// the old one ends: the old one leaves it.
func TestEndingLeavesAnotherPidsNote(t *testing.T) {
	fresh(t)
	recordListening(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000})
	b, err := os.ReadFile(ListeningPath())
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(string(b), `"pid":`, `"pid":1`, 1) // 1<pid>
	if err := os.WriteFile(ListeningPath(), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	forgetListening()
	if _, ok := ReadListening(); !ok {
		t.Error("a note of another process was removed")
	}
}

// A note that doesn't parse or has no usable address is unknown, never an
// address to ask.
func TestUnreadableNoteIsUnknown(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	os.MkdirAll(ListeningPath()[:strings.LastIndex(ListeningPath(), string(os.PathSeparator))], 0o700)
	for _, body := range []string{`{"addr":"127.0.0.1:99`, `{"addr":"nope","pid":5}`, `{"addr":"127.0.0.1:9","pid":0}`, ``} {
		os.WriteFile(ListeningPath(), []byte(body), 0o600)
		if _, ok := ReadListening(); ok {
			t.Errorf("%q was read as a note", body)
		}
		if got := ClientURL(); got != URL() {
			t.Errorf("%q: ClientURL %s, want %s", body, got, URL())
		}
	}
}

// Moving the gateway (another port) moves the note with it.
func TestRelistenMovesTheNote(t *testing.T) {
	fresh(t)
	s := New()
	first, err := s.listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = first
	to := freeAddr(t)
	t.Setenv("MAGPIE_ADDR", to)
	if err := s.Relisten(); err != nil {
		t.Fatal(err)
	}
	defer s.ln.Close()
	if l, ok := ReadListening(); !ok || l.Addr != to {
		t.Errorf("after moving: %+v %v, want %s", l, ok, to)
	}
}
