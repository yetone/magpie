package library

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// starballl on X: a Remote magpie added with a key that may use all of
// the magpie it reaches, the user's own hub, has both Library switches on
// at once, those the user left alone. A key held to anything, one the
// hub can't judge, a switch the user set, a provider switched off and one
// whose switch is on already are left as they are; nothing is turned off.
func TestAddedRemoteTurnsOnForAFullKey(t *testing.T) {
	sandbox(t)
	var mcpAsked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case provider.RemoteKeyLimitPath:
			switch r.Header.Get("Authorization") {
			case "Bearer full":
				io.WriteString(w, `{"limited":false,"key":"Laptop","full":true}`)
			case "Bearer held":
				io.WriteString(w, `{"limited":true,"key":"Friend","heldTo":["budget"]}`)
			default:
				io.WriteString(w, `{"limited":false,"key":"Old"}`)
			}
		case "/v1":
			mcpAsked.Add(1)
			io.WriteString(w, `{"name":"magpie"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	add := func(id, key string, off bool) {
		t.Helper()
		if err := provider.Save(provider.Provider{ID: id, Name: id, Preset: provider.RemoteMagpiePreset, Key: key, Chat: strings.TrimPrefix(srv.URL, "http://"), Off: off}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	on := func(id string) (bool, bool) { return RemoteMCPOn()[id], BorrowedOn()[id] }

	add("hub", "full", false)
	if m, s := AddedRemote(ctx, "hub", true, true); !m || !s {
		t.Fatalf("a full key: turned on %v %v", m, s)
	}
	if m, s := on("hub"); !m || !s {
		t.Fatalf("a full key: switches %v %v", m, s)
	}
	if mcpAsked.Load() == 0 {
		t.Fatal("the hub wasn't asked for its router")
	}

	// the user unticked "Add its MCP servers to the Library" on Add
	add("hub2", "full", false)
	if m, s := AddedRemote(ctx, "hub2", false, true); m || !s {
		t.Fatalf("MCP set by hand: turned on %v %v", m, s)
	}
	if m, s := on("hub2"); m || !s {
		t.Fatalf("MCP set by hand: switches %v %v", m, s)
	}

	for _, c := range []struct {
		what, id, key string
		off           bool
	}{
		{"a key with a budget", "friend", "held", false},
		{"a magpie that doesn't say", "old", "old", false},
		{"a provider switched off", "offhub", "full", true},
	} {
		add(c.id, c.key, c.off)
		if m, s := AddedRemote(ctx, c.id, true, true); m || s {
			t.Errorf("%s: turned on %v %v", c.what, m, s)
		}
		if m, s := on(c.id); m || s {
			t.Errorf("%s: switches %v %v", c.what, m, s)
		}
	}

	// on already: nothing to turn on, and nothing turned off
	if m, s := AddedRemote(ctx, "hub", true, true); m || s {
		t.Fatalf("on already: turned on %v %v", m, s)
	}
	if m, s := on("hub"); !m || !s {
		t.Fatalf("on already: switches %v %v", m, s)
	}
}
