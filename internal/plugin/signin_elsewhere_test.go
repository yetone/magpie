package plugin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Another magpie saves a sign-in again over an account this one's host
// has a loader for (the window, while the gateway runs as a service: the
// same Google account signed in again with agy-cli's "Add another Google
// account", its home now in the sign-in). The host running makes the
// loader again from the new sign-in, and magpie its options, without a
// restart (mayne_wong on X: the gateway kept answering 401 until the
// plugin's process was killed).
func TestSignInChangedElsewhere(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/loaderauth/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	save := func(key, home string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"loaderco": map[string]any{"type": "api", "key": key, "metadata": map[string]any{"home": home}}})
		if err := os.WriteFile(AuthPath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	type answer struct {
		Home  string `json:"home"`
		Loads int    `json:"loads"`
	}
	fetch := func() answer {
		t.Helper()
		res, err := Fetch(ctx, FetchRequest{Provider: "loaderco", Model: "lo-1", NPM: "@ai-sdk/openai-compatible",
			URL: "https://loader.invalid/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var a answer
		if err := json.Unmarshal(b, &a); err != nil {
			t.Fatalf("answered %q", b)
		}
		return a
	}
	apiKey := func() string {
		t.Helper()
		o, err := LoaderOptions(ctx, "loaderco", "")
		if err != nil {
			t.Fatal(err)
		}
		return o.APIKey
	}

	save("k-keyring", "keyring")
	if a := fetch(); a.Home != "keyring" || a.Loads != 1 {
		t.Fatalf("first request: %+v", a)
	}
	if a := fetch(); a.Loads != 1 {
		t.Fatalf("the loader ran again for the same sign-in: %+v", a)
	}
	if k := apiKey(); k != "made-from-k-keyring" {
		t.Fatalf("options: %q", k)
	}

	// the other magpie saves it again; a second write can share the
	// first's time stamp, so the host's read must not trust it alone
	time.Sleep(10 * time.Millisecond)
	save("k-file", "file-home")
	if a := fetch(); a.Home != "file-home" {
		t.Fatalf("the host served the sign-in another magpie replaced: %+v", a)
	}
	if k := apiKey(); k != "made-from-k-file" {
		t.Fatalf("magpie kept the options made from the replaced sign-in: %q", k)
	}
	a := fetch()
	if a.Home != "file-home" {
		t.Fatalf("after: %+v", a)
	}
	if loads := a.Loads; fetch().Loads != loads {
		t.Fatal("the loader runs on every request once a sign-in changed elsewhere")
	}
}
