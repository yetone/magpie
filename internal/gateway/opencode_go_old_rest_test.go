package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// xzhih on X, #7773: a magpie before v0.1.4818 rested an OpenCode Go key
// out of its windows as a whole, and kept a notice of it under the whole
// key in limit-notices.json (and, refused 402, a credit ladder rest in
// rests.json). After the update the gateway served the free models, but
// the kept notice said the key was out till the reset, and a kept ladder
// rest held the free models for up to a week. Both are let go when read,
// for every user who updates; a model's own notice is kept, and another
// provider's whole-key notice and rest are kept as they were.
func TestOpenCodeGoOldWholeKeyRestsLetGo(t *testing.T) {
	s, _, reached := goWindowServer(t, false, nil)
	keyWindows(t, nil)
	keyedOpenAI(t)
	now := time.Now()

	// what a magpie before v0.1.4818 left: the Go key out as a whole
	limitNotices.Lock()
	for _, k := range []string{"go", "openai"} {
		limitNotices.m[k] = LimitNotice{Key: k, Why: failQuota, Status: 429, Error: "usage limit reached", At: now.Add(-time.Hour), Until: now.Add(14 * time.Hour)}
	}
	limitNotices.m["go/glm-5.3"] = LimitNotice{Key: "go/glm-5.3", Why: failQuota, Status: 429, Model: "glm-5.3", At: now.Add(-time.Minute), Until: now.Add(14 * time.Hour)}
	limitNotices.Unlock()
	saveNotices()
	restingUntil.Lock()
	for _, k := range []string{"go", "openai"} {
		r := Rest{Why: failCredit, Status: http.StatusPaymentRequired, By: "credit", Failures: 3, Key: k, Until: now.Add(8 * time.Hour)}
		restingUntil.m[k], restingUntil.note[k] = r.Until, r
	}
	restingUntil.Unlock()
	saveRests()

	restartRests()
	restartNotices()

	if n, ok := noticeOf(t, "go"); ok {
		t.Errorf("the Go key's whole-key notice was kept: %+v", n)
	}
	if r, ok := restOf("go"); ok {
		t.Errorf("the Go key's whole-key ladder rest was kept: %+v", r)
	}
	if _, ok := noticeOf(t, "go/glm-5.3"); !ok {
		t.Error("glm-5.3's own notice was let go")
	}
	if _, ok := noticeOf(t, "openai"); !ok {
		t.Error("OpenAI's whole-key notice was let go")
	}
	if _, ok := restOf("openai"); !ok {
		t.Error("OpenAI's whole-key ladder rest was let go")
	}
	// the file is written without it, so no other reader shows it
	b, err := os.ReadFile(noticesPath())
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]LimitNotice
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if _, ok := file["go"]; ok {
		t.Errorf("limit-notices.json still has the Go key's: %s", b)
	}
	if _, ok := file["openai"]; !ok {
		t.Errorf("limit-notices.json lost OpenAI's: %s", b)
	}
	if _, ok := file["go/glm-5.3"]; !ok {
		t.Errorf("limit-notices.json lost glm-5.3's: %s", b)
	}

	// and the free model is served
	if rec := askGo(s, "hy4-flash-free"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from go") {
		t.Fatalf("hy4-flash-free answered %d %s", rec.Code, rec.Body)
	}
	if got := reached(); strings.Join(got, " ") != "hy4-flash-free" {
		t.Fatalf("Go reached for %v", got)
	}
}
