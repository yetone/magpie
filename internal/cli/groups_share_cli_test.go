package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/groupshare"
	"github.com/yetone/magpie/internal/provider"
)

// `magpie group share <id>` puts a group at a usemagpie.ai link and says
// it; sharing again keeps the words it was shared with but for the flags
// named anew, on the same link. --text prints the group with nothing
// sent; --stop takes the link down. `magpie group import <link>` adds the
// group the link names and remembers where it came from. A flag either
// command doesn't know is refused, never taken as a value.
func TestGroupShareCLI(t *testing.T) {
	groupsHome(t)
	var mu sync.Mutex
	var asked []string
	var posted map[string]json.RawMessage
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		const id = "abcdefghijkl"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/groups", r.Method == http.MethodPut:
			_ = json.Unmarshal(b, &posted)
			v := 1
			if r.Method == http.MethodPut {
				v = 2
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "http://" + r.Host + "/g/" + id, "token": "tok", "version": v})
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"deleted":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/groups/"+id:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "http://" + r.Host + "/g/" + id, "title": "Shared fast", "version": 2, "group": posted["group"]})
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"gone"}`))
		}
	}))
	t.Cleanup(site.Close)
	t.Setenv("MAGPIE_SHARE_SITE", site.URL)
	if err := provider.SaveGroup(provider.Group{ID: "fast", Name: "Fast", Members: []string{"a/m", "b/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}

	out, err := stdoutOf(t, func() error { return groupCmd([]string{"group", "share", "fast", "--about", "m first", "--by", "ann"}) })
	if err != nil || !strings.Contains(out, site.URL+"/g/abcdefghijkl") || !strings.Contains(out, "version 1") {
		t.Fatalf("share: %v %q", err, out)
	}
	sent := mustMarshal(posted)
	if string(posted["title"]) != `"Fast"` || string(posted["description"]) != `"m first"` || string(posted["author"]) != `"ann"` || string(posted["public"]) != "true" {
		t.Fatalf("what went: %s", sent)
	}
	// the providers groupsHome saved are the user's own: named custom,
	// their keys nowhere
	if !strings.Contains(sent, `"custom/m"`) || strings.Contains(sent, `"ka"`) || strings.Contains(sent, `"kb"`) || strings.Contains(sent, "127.0.0.1") {
		t.Fatalf("what went: %s", sent)
	}
	// again, unlisted: the same link, the words it had
	if out, err = stdoutOf(t, func() error { return groupCmd([]string{"group", "share", "fast", "--unlisted"}) }); err != nil || !strings.Contains(out, "unlisted") || !strings.Contains(out, "version 2") {
		t.Fatalf("share again: %v %q", err, out)
	}
	if m := groupshare.Shared()["fast"]; m.Description != "m first" || m.Author != "ann" || m.Public {
		t.Fatalf("shared again as %+v, want the words kept and unlisted", m)
	}

	mu.Lock()
	n := len(asked)
	mu.Unlock()
	if out, err = stdoutOf(t, func() error { return groupCmd([]string{"group", "share", "fast", "--text"}) }); err != nil || !strings.Contains(out, `"routing-group"`) {
		t.Fatalf("--text: %v %q", err, out)
	}
	mu.Lock()
	if len(asked) != n {
		t.Errorf("--text reached the site: %v", asked[n:])
	}
	mu.Unlock()

	if out, err = stdoutOf(t, func() error {
		return groupCmd([]string{"group", "import", site.URL + "/g/abcdefghijkl"})
	}); err != nil || !strings.Contains(out, "added") {
		t.Fatalf("import: %v %q", err, out)
	}
	from := groupshare.Sources()
	if len(from) != 1 {
		t.Fatalf("the import doesn't remember where it came from: %v", from)
	}
	for gid, f := range from {
		if f.ID != "abcdefghijkl" || f.Version != 2 || gid == "fast" {
			t.Fatalf("from %s: %+v", gid, f)
		}
	}

	if out, err = stdoutOf(t, func() error { return groupCmd([]string{"group", "share", "fast", "--stop"}) }); err != nil || !strings.Contains(out, "no longer shared") {
		t.Fatalf("--stop: %v %q", err, out)
	}
	if _, ok := groupshare.Shared()["fast"]; ok {
		t.Fatal("--stop left the link recorded")
	}

	before := len(provider.Groups())
	for _, args := range [][]string{
		{"group", "share", "fast", "--bogus"},
		{"group", "import", "--bogus"},
		{"group", "import", site.URL + "/g/abcdefghijkl", "--bogus"},
	} {
		if err := groupCmd(args); err == nil || !strings.Contains(err.Error(), "--bogus") {
			t.Errorf("%v: %v, want --bogus refused", args, err)
		}
	}
	if len(provider.Groups()) != before {
		t.Fatal("a refused command added a group")
	}
	if out, err = stdoutOf(t, func() error { return groupCmd([]string{"group", "import", "--help"}) }); err != nil || !strings.Contains(out, "magpie group import") {
		t.Fatalf("import --help: %v %q", err, out)
	}
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
