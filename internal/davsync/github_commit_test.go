package davsync

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// GitHub credited every sync commit to the token's owner, sent with no
// author or committer, and the commits filled their contribution graph
// (IceCodeNew, #4579). Each one, through the Contents API (an empty
// repository's first) and the Git Data API alike, is by the bot named, or
// by GitHub Actions' bot when none is.
func TestGitHubCommitsAs(t *testing.T) {
	bot := DefaultCommitName + " <" + DefaultCommitEmail + ">"
	app := "magpie-sync[bot] <123456+magpie-sync[bot]@users.noreply.github.com>"
	for _, tc := range []struct {
		name, gitName, gitEmail, want string
		// old: sync.json as a magpie before the choice wrote it, never saved since
		old bool
	}{
		{name: "default", want: bot},
		{name: "a setup from before the choice", old: true, want: bot},
		{name: "the default typed in", gitName: " " + DefaultCommitName, gitEmail: DefaultCommitEmail + " ", want: bot},
		{name: "a GitHub App's bot", gitName: "magpie-sync[bot]", gitEmail: "123456+magpie-sync[bot]@users.noreply.github.com", want: app},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGithubFixture(t)
			f.empty = true
			newComputer(t).use(t)
			c := githubConfig()
			c.GitName, c.GitEmail = tc.gitName, tc.gitEmail
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "fixture-provider-key"}); err != nil {
				t.Fatal(err)
			}
			if tc.old {
				b, _ := json.Marshal(map[string]any{"url": c.URL, "password": c.Password, "passphrase": c.Passphrase, "keys": true, "agents": true})
				if err := os.WriteFile(path("sync.json"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := Configure(c); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "fixture-second-key"}); err != nil {
				t.Fatal(err)
			}
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			want := []string{"contents: " + tc.want + " | " + tc.want, "git/commits: " + tc.want + " | " + tc.want}
			if !slices.Equal(f.commitsBy, want) {
				t.Fatalf("commits by\n%q\nwant\n%q", f.commitsBy, want)
			}
			if saved, _ := Load(); tc.want == bot && (saved.GitName != "" || saved.GitEmail != "") {
				t.Fatalf("the default is kept as none, to follow magpie's: %q %q", saved.GitName, saved.GitEmail)
			}
		})
	}
	t.Run("a large backup's first commit and its parts", func(t *testing.T) {
		f := newGithubFixture(t)
		f.empty = true
		c := githubConfig()
		c.GitName, c.GitEmail = "magpie-sync[bot]", "123456+magpie-sync[bot]@users.noreply.github.com"
		g, err := newGitHub(c)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, _, err := g.get(ctx, version{}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.put(ctx, make([]byte, githubPart+1), ""); err != nil {
			t.Fatal(err)
		}
		want := []string{"contents: " + app + " | " + app, "git/commits: " + app + " | " + app}
		if !slices.Equal(f.commitsBy, want) {
			t.Fatalf("commits by\n%q\nwant\n%q", f.commitsBy, want)
		}
	})
}

func TestGitHubCommitAsChecked(t *testing.T) {
	newComputer(t).use(t)
	for _, tc := range []struct{ name, email, want string }{
		{"magpie-sync[bot]", "", "both"},
		{"", "123456+magpie-sync[bot]@users.noreply.github.com", "both"},
		{"Ada <ada@example.com>", "ada@example.com", "<, >"},
		{"Ada\nLovelace", "ada@example.com", "line break"},
		{"Ada", "ada", "an address"},
		{"Ada", "ada@localhost", "an address"},
		{"Ada", "ada @example.com", "an address"},
		{"Ada", "<ada@example.com>", "an address"},
	} {
		c := githubConfig()
		c.GitName, c.GitEmail = tc.name, tc.email
		if err := Configure(c); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q %q: %v, want it refused for %q", tc.name, tc.email, err, tc.want)
		}
	}
	if _, ok := Load(); ok {
		t.Fatal("a refused setup was saved")
	}
	// WebDAV and S3 make no commits: nothing of GitHub's is kept for them
	c := Config{URL: "https://dav.example/dav", User: "user", Password: "dav-secret", Passphrase: "fixture-independent-passphrase",
		GitName: "Ada", GitEmail: "nope"}
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if saved, _ := Load(); saved.GitName != "" || saved.GitEmail != "" {
		t.Fatalf("WebDAV kept a commit identity: %q %q", saved.GitName, saved.GitEmail)
	}
	// set on GitHub, kept for moving back when sync moves to WebDAV, and
	// shown, the default beside it
	gh := githubConfig()
	gh.GitName, gh.GitEmail = "magpie-sync[bot]", "123456+magpie-sync[bot]@users.noreply.github.com"
	if err := Configure(gh); err != nil {
		t.Fatal(err)
	}
	v := Status()
	if v.GitName != gh.GitName || v.GitEmail != gh.GitEmail || v.CommitDefault != (CommitAs{DefaultCommitName, DefaultCommitEmail}) {
		t.Fatalf("status: %q %q %+v", v.GitName, v.GitEmail, v.CommitDefault)
	}
	text, err := SetupText()
	if err != nil {
		t.Fatal(err)
	}
	if s, err := OpenSetupText(text, gh.Passphrase); err != nil || s.GitName != gh.GitName || s.GitEmail != gh.GitEmail {
		t.Fatalf("the setup text carries who commits: %+v %v", s, err)
	}
	c.Password, c.GitName, c.GitEmail = "", "", ""
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if s, ok := Status().Servers["github"]; !ok || s.GitName != gh.GitName || s.GitEmail != gh.GitEmail {
		t.Fatalf("the GitHub server kept for moving back lost who commits: %+v", Status().Servers)
	}
	if v := Status(); v.GitName != "" || v.CommitDefault.Name != DefaultCommitName {
		t.Fatalf("status on WebDAV: %+v", v)
	}
}
