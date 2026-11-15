package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Packing1 on Discord: a Google AI Pro account's model list names Argon,
// which Antigravity answers 404 NOT_FOUND. The model isn't the plan's, so
// only it rests on that account; the account itself doesn't, and its
// other models are asked as before.
func TestCodeAssistMissingModelRestsTheModel(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	const notFound = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`
	s, _ := antigravityGroupFor(t, []string{"u@example.com"}, "argon", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, notFound)
	})
	code, out := turnOn(t, s, claudeTurn("You are a helpful assistant."))
	if code != 200 || !strings.Contains(out, "from the other") {
		t.Fatalf("the turn: %d %s", code, out)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 404 || r.Tries[0].Rest == nil {
		t.Fatalf("the 404 try: %+v", r.Tries)
	}
	if _, ok := restOf("antigravity@u@example.com"); ok {
		t.Fatalf("the account rests for a model its plan lacks: %v", rests())
	}
	if _, ok := restOf("antigravity@u@example.com/argon"); !ok {
		t.Fatalf("argon doesn't rest on the account: %v", rests())
	}
	// another vendor's 404 says nothing of the model: as before
	other, err := provider.Find("other")
	if err != nil {
		t.Fatal(err)
	}
	if codeAssistModelMissing(*other, 404, []byte(notFound)) {
		t.Fatal("a key's 404 read as Code Assist's missing model")
	}
}
