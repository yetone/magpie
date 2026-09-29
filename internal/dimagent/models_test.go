package dimagent

import (
	"errors"
	"testing"
)

// The upstream answering well with an empty list is what an account whose
// subscription has ended gets; that is its state, not a broken reply, so the
// two say apart (the vendor's dim envelope wraps an OpenAI list).
func TestModelInfosEmptyIsErrNoModels(t *testing.T) {
	for _, body := range []string{
		`{"data":[],"success":true}`,
		`{"object":"list","data":[]}`,
	} {
		_, err := ModelInfos([]byte(body))
		if !errors.Is(err, ErrNoModels) {
			t.Fatalf("%s: err = %v, want ErrNoModels", body, err)
		}
	}
}

// A list whose every entry is a placeholder the upstream keeps for its own
// pickers ("auto", "default", an empty id) says how many it held, rather
// than as if the account had listed nothing.
func TestModelInfosUnroutableSaysCount(t *testing.T) {
	_, err := ModelInfos([]byte(`{"data":[{"id":"auto"},{"id":"default"},{"id":""}]}`))
	if err == nil || errors.Is(err, ErrNoModels) {
		t.Fatalf("err = %v, want the unroutable count", err)
	}
	t.Log(err)
}
