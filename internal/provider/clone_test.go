package provider

import "testing"

// A held list is shared by the request's callers: one that changes a key
// of its copy (SetKeyOn through Find) leaves the others' as they were.
func TestHeldAllUnshared(t *testing.T) {
	n := 3
	p := Provider{ID: "a", Keys: []KeyAccount{{Name: "k"}}, Headers: map[string]string{"h": "1"},
		Models: []string{"m"}, AccountModels: map[string][]string{"x": {"m"}}, MaxConcurrency: &n}
	q := p.clone()
	q.Keys[0].Name = "changed"
	q.Headers["h"] = "2"
	q.Models[0] = "n"
	q.AccountModels["x"][0] = "n"
	*q.MaxConcurrency = 9
	if p.Keys[0].Name != "k" || p.Headers["h"] != "1" || p.Models[0] != "m" || p.AccountModels["x"][0] != "m" || n != 3 {
		t.Fatalf("clone shares with the original: %+v", p)
	}
}
