package access

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestUsersAndKeyLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Update("add-user", Change{Name: "   "}); err == nil {
		t.Fatal("accepted empty user")
	}
	for _, name := range []string{"Alice", "Bob"} {
		if _, err := Update("add-user", Change{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	users, _ := List()
	alice, bob := users[0].ID, users[1].ID
	var secrets []string
	for _, in := range []Change{{User: alice, Name: "Laptop"}, {User: alice, Name: "Server"}, {User: bob, Name: "Work"}} {
		key, err := Update("add-key", in)
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, key)
	}
	users, _ = List()
	if len(users[0].Keys) != 2 || len(users[1].Keys) != 1 {
		t.Fatal(users)
	}
	for _, u := range users {
		for _, k := range u.Keys {
			if k.Secret != "" || !strings.Contains(k.Masked, "…") {
				t.Fatal("unmasked credential", k.ID)
			}
		}
	}
	who, ok := Authenticate(secrets[0])
	if !ok || who.UserID != alice || who.UserName != "Alice" || who.KeyName != "Laptop" {
		t.Fatal(who, ok)
	}
	if _, ok := Authenticate(secrets[0] + "x"); ok {
		t.Fatal("accepted invalid key")
	}
	key := users[0].Keys[0].ID
	if _, err := Update("rename-user", Change{User: alice, Name: "Alice renamed"}); err != nil {
		t.Fatal(err)
	}
	Update("rename-key", Change{User: alice, Key: key, Name: "Main"})
	who, ok = Authenticate(secrets[0])
	if !ok || who.UserName != "Alice renamed" || who.KeyName != "Main" {
		t.Fatal(who)
	}
	Update("off-key", Change{User: alice, Key: key})
	if _, ok := Authenticate(secrets[0]); ok {
		t.Fatal("disabled key works")
	}
	if _, ok := Authenticate(secrets[1]); !ok {
		t.Fatal("other key disabled")
	}
	Update("on-key", Change{User: alice, Key: key})
	Update("off-user", Change{User: alice})
	for _, secret := range secrets[:2] {
		if _, ok := Authenticate(secret); ok {
			t.Fatal("disabled user's key works")
		}
	}
	if _, ok := Authenticate(secrets[2]); !ok {
		t.Fatal("other user disabled")
	}
	Update("on-user", Change{User: alice})
	got, err := Update("copy-key", Change{User: alice, Key: key})
	if err != nil || got != secrets[0] {
		t.Fatal("copy", err)
	}
	if _, err := Update("remove-key", Change{User: bob, Key: key}); err == nil {
		t.Fatal("accepted another user's key")
	}
	Update("remove-key", Change{User: alice, Key: key})
	if _, ok := Authenticate(secrets[0]); ok {
		t.Fatal("deleted key works")
	}
	Update("remove-user", Change{User: alice})
	if _, ok := Authenticate(secrets[1]); ok {
		t.Fatal("deleted user's key works")
	}
	st, err := os.Stat(Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("credential file permissions", err)
	}
}

func TestConcurrentKeyCreationAndCorruptStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Update("add-user", Change{Name: "Team"})
	users, _ := List()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Update("add-key", Change{User: users[0].ID, Name: "Key"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	users, _ = List()
	if len(users[0].Keys) != 12 {
		t.Fatal("lost concurrent changes")
	}
	secret, _ := Update("copy-key", Change{User: users[0].ID, Key: users[0].Keys[0].ID})
	os.WriteFile(Path(), []byte("broken"), 0o600)
	if _, ok := Authenticate(secret); ok {
		t.Fatal("corrupt store accepted a key")
	}
	if _, err := Update("add-user", Change{Name: "New"}); err == nil {
		t.Fatal("overwrote corrupt store")
	}
}
