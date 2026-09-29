package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestParseGoogleImportFormats(t *testing.T) {
	cases := []struct {
		name, data string
		want       []googleImport // n and err not compared unless set
		errs       []string       // per entry, "" for none
	}{
		{"cockpit export", `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"b@x.com","refresh_token":"1//b"}]`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}, {email: "b@x.com", refreshToken: "1//b"}}, []string{"", ""}},
		{"single object, camelCase", `{"email":"a@x.com","refreshToken":"1//a","projectId":"p1"}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "p1"}}, []string{""}},
		{"manager export response", `{"accounts":[{"email":"a@x.com","refresh_token":"1//a"}]}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}}, []string{""}},
		{"manager account file", `{"id":"u1","email":"a@x.com","name":"A","token":{"access_token":"ya29.x","refresh_token":"1//a","expires_in":3599,"expiry_timestamp":1790000000,"token_type":"Bearer","email":"a@x.com","project_id":"proj-a"}}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "proj-a"}}, []string{""}},
		{"cliproxyapi auth file", `{"type":"antigravity","access_token":"ya29.x","refresh_token":"1//a","expires_in":3599,"timestamp":1790000000000,"expired":"2026-09-29T10:00:00Z","email":"a@x.com","project_id":"proj-a"}`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a", project: "proj-a"}}, []string{""}},
		{"cliproxyapi gemini file refused", `{"type":"gemini","refresh_token":"1//g","email":"g@x.com"}`,
			[]googleImport{{email: "g@x.com", refreshToken: "1//g"}}, []string{"a gemini sign-in, not Antigravity's"}},
		{"bare tokens, one a line", "\xef\xbb\xbf1//a\n\n# a comment\n1//b\r\n",
			[]googleImport{{refreshToken: "1//a"}, {refreshToken: "1//b"}}, []string{"", ""}},
		{"array of token strings", `["1//a","1//b"]`,
			[]googleImport{{refreshToken: "1//a"}, {refreshToken: "1//b"}}, []string{"", ""}},
		{"duplicates and missing tokens", `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"A@x.com","refresh_token":"1//z"},{"email":"b@x.com","refresh_token":"1//a"},{"email":"c@x.com"},3]`,
			[]googleImport{{email: "a@x.com", refreshToken: "1//a"}, {email: "A@x.com", refreshToken: "1//z"}, {email: "b@x.com", refreshToken: "1//a"}, {email: "c@x.com"}, {}},
			[]string{"", "in the file twice", "in the file twice", "no refresh_token in it", "not an account"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseGoogleImport("antigravity", c.data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d entries, want %d: %+v", len(got), len(c.want), got)
			}
			for i, g := range got {
				w := c.want[i]
				if g.email != w.email || g.refreshToken != w.refreshToken || g.project != w.project || g.err != c.errs[i] || g.n != i+1 {
					t.Errorf("entry %d = %+v, want %+v err %q", i, g, w, c.errs[i])
				}
			}
		})
	}
	for _, bad := range []string{"", "  ", "{not json", "[]", "{}"} {
		es, err := parseGoogleImport("antigravity", bad)
		if bad == "{}" {
			// an object with nothing in it is one entry, without a token
			if err != nil || len(es) != 1 || es[0].err == "" {
				t.Errorf("%q: %+v, %v", bad, es, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: no error (%+v)", bad, es)
		}
	}
}

// fakeGoogleImport is Google's token and userinfo endpoints and Code
// Assist, knowing a few refresh tokens.
type fakeGoogleImport struct {
	mu        sync.Mutex
	users     map[string]string // refresh token → email
	clients   []string
	refreshed []string
}

func (f *fakeGoogleImport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	switch {
	case r.URL.Path == "/token":
		form, _ := url.ParseQuery(string(b))
		f.clients = append(f.clients, form.Get("client_id"))
		rt := form.Get("refresh_token")
		f.refreshed = append(f.refreshed, rt)
		if rt == "1//garbage" { // what Google answers for a token it doesn't know
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
			return
		}
		if form.Get("grant_type") != "refresh_token" || f.users[rt] == "" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		io.WriteString(w, `{"access_token":"at-`+rt+`","expires_in":3600}`)
	case r.URL.Path == "/userinfo":
		at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
		if f.users[at] == "" || strings.HasPrefix(f.users[at], "nouser") {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"email": f.users[at]})
	case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
		io.WriteString(w, `{"cloudaicompanionProject":"found-proj","paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"}}`)
	case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
		io.WriteString(w, "version: 3.1.4\n")
	default:
		http.NotFound(w, r)
	}
}

func TestImportGoogleAccounts(t *testing.T) {
	googleSandbox(t, &fakeGoogle{})
	f := &fakeGoogleImport{users: map[string]string{"1//a": "a@x.com", "1//b": "b@x.com", "1//c": "c@x.com", "1//n": "nouser"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	oldToken, oldProd, oldDaily, oldWho := googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL
	googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = srv.URL+"/token", srv.URL+"/prod", srv.URL+"/daily", srv.URL+"/userinfo"
	t.Cleanup(func() {
		googleTokenURL, codeAssistProd, codeAssistDaily, googleUserInfoURL = oldToken, oldProd, oldDaily, oldWho
	})

	// b is in magpie already, with the same sign-in
	if err := addGoogleLogin("antigravity", "b@x.com", "", googleAuth{RefreshToken: "1//b", Project: "pb"}); err != nil {
		t.Fatal(err)
	}
	cockpit := `[{"email":"a@x.com","refresh_token":"1//a"},{"email":"b@x.com","refresh_token":"1//b"},{"email":"dead@x.com","refresh_token":"1//dead"}]`
	cpa := `{"type":"antigravity","refresh_token":"1//n","email":"n@x.com","project_id":"file-proj"}`
	res, err := ImportGoogleAccounts(context.Background(), "antigravity", []string{cockpit, cpa, "1//c"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportedAccount{
		{User: "a@x.com", Status: "added", Plan: "Google AI Pro"},
		{User: "b@x.com", Status: "exists"},
		{User: "dead@x.com", Status: "failed"},
		{User: "n@x.com", Status: "added", Plan: "Google AI Pro"}, // userinfo refused: the file's email
		{User: "c@x.com", Status: "added", Plan: "Google AI Pro"},
	}
	if len(res) != len(want) {
		t.Fatalf("results = %+v", res)
	}
	for i, r := range res {
		if r.User != want[i].User || r.Status != want[i].Status || r.Plan != want[i].Plan {
			t.Errorf("result %d = %+v, want %+v", i, r, want[i])
		}
	}
	if !strings.Contains(res[2].Error, "expired or revoked") {
		t.Errorf("dead's error = %q", res[2].Error)
	}
	// no token in what is said back
	out, _ := json.Marshal(res)
	for _, tok := range []string{"1//", "at-"} {
		if strings.Contains(string(out), tok) {
			t.Errorf("results carry a token: %s", out)
		}
	}
	// refreshed with Antigravity's own client; b, already here, wasn't asked for
	for _, c := range f.clients {
		if c != antigravityApp.clientID {
			t.Errorf("refreshed with client %q", c)
		}
	}
	for _, rt := range f.refreshed {
		if rt == "1//b" {
			t.Error("an account already in magpie was refreshed")
		}
	}
	// kept as signed-in accounts are, with their project
	users := map[string]string{}
	for _, l := range googleLogins("antigravity") {
		users[l.User] = l.acct.auth.Project
	}
	if len(users) != 4 || users["a@x.com"] != "found-proj" || users["c@x.com"] != "found-proj" || users["b@x.com"] != "pb" {
		t.Errorf("logins = %v", users)
	}
	b, _ := os.ReadFile(loginsPath())
	if !strings.Contains(string(b), `"1//a"`) {
		t.Error("a's sign-in not kept in logins.json")
	}

	// again: nothing new; a new sign-in of a is taken in its place
	f.mu.Lock()
	f.users["1//a2"] = "a@x.com"
	f.mu.Unlock()
	res, err = ImportGoogleAccounts(context.Background(), "antigravity", []string{cockpit, `[{"refresh_token":"1//a2"}]`})
	if err != nil {
		t.Fatal(err)
	}
	st := []string{}
	for _, r := range res {
		st = append(st, r.User+":"+r.Status)
	}
	if strings.Join(st, " ") != "a@x.com:exists b@x.com:exists dead@x.com:failed a@x.com:updated" {
		t.Errorf("again = %v", st)
	}
	for _, l := range googleLogins("antigravity") {
		if l.User == "a@x.com" && l.acct.auth.RefreshToken != "1//a2" {
			t.Error("a's newer sign-in wasn't kept")
		}
	}

	// Google's bare "Bad Request" is explained
	res, err = ImportGoogleAccounts(context.Background(), "antigravity", []string{"1//garbage"})
	if err != nil || len(res) != 1 || res[0].Status != "failed" || !strings.Contains(res[0].Error, "isn't a refresh token") || strings.Contains(res[0].Error, "1//garbage") {
		t.Errorf("bad request = %+v, %v", res, err)
	}

	// only Antigravity's; a file of nothing says so
	if _, err := ImportGoogleAccounts(context.Background(), "gemini", []string{cockpit}); err == nil {
		t.Error("imported into Gemini CLI")
	}
	if _, err := ImportGoogleAccounts(context.Background(), "antigravity", []string{"{broken"}); err == nil {
		t.Error("broken JSON imported")
	}
}
