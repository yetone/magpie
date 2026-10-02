package provider

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/qoder"
)

// roundTrip moves id's saved accounts out as the plugin keeps them and back
// again, through JSON as plugin-auth.json has them: each account's sign-in
// comes back as it was, the one in use first and the rest on or off as
// they were. Put back where nothing is saved, each is an account again.
func roundTrip(t *testing.T, id string, saved []savedLogin, same func(t *testing.T, a, b savedLogin)) {
	t.Helper()
	claudeHome(t)
	mv := movers[id]
	loginsMu.Lock()
	if err := writeLogins(slicesClone(saved)); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	accts, err := mv.out()
	if err != nil || len(accts) != len(saved) {
		t.Fatalf("%s out: %d accounts, %v", id, len(accts), err)
	}
	for i, a := range accts {
		if a.User != saved[i].User || a.First != saved[i].First || a.On != (saved[i].On || saved[i].First) || a.Own {
			t.Fatalf("%s account %d: %+v", id, i, a)
		}
		// as the host writes it and Go reads it back
		var auth map[string]any
		if err := json.Unmarshal([]byte(jsonText(a.Auth)), &auth); err != nil {
			t.Fatal(err)
		}
		accts[i].Auth = auth
	}
	for _, fresh := range []bool{false, true} {
		ls := slicesClone(saved)
		if fresh {
			ls = nil
		}
		for i, a := range accts {
			user := a.User
			if fresh {
				user = ""
			}
			next, u, err := mv.back(ls, user, a.Auth)
			if err != nil {
				t.Fatalf("%s back %s: %v", id, a.User, err)
			}
			ls = next
			if u != saved[i].User {
				t.Fatalf("%s back wrote %q, want %q", id, u, saved[i].User)
			}
		}
		if len(ls) != len(saved) {
			t.Fatalf("%s back (fresh %v): %d accounts", id, fresh, len(ls))
		}
		for i := range saved {
			if ls[i].Lapsed != "" {
				t.Fatalf("%s back left %s lapsed", id, ls[i].User)
			}
			same(t, saved[i], ls[i])
		}
	}
}

func slicesClone(ls []savedLogin) []savedLogin { return append([]savedLogin(nil), ls...) }

func authOf(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestQoderMover(t *testing.T) {
	c1 := qoder.Credential{UID: "u1", Email: "a@q.com", Name: "A", Token: "t1", RefreshToken: "r1", DeviceToken: "d1", DeviceRefresh: "dr1", ExpiresAt: 1_900_000_000_123, MachineID: "m1", Models: json.RawMessage(`[{"id":"x"}]`)}
	c2 := qoder.Credential{UID: "u2", Token: "t2", RefreshToken: "r2", DeviceToken: "d2", ExpiresAt: 1_900_000_000_456, MachineID: "m2"}
	roundTrip(t, "qoder", []savedLogin{
		{Agent: "qoder", User: "a@q.com", First: true, On: true, Auth: authOf(t, c1), Lapsed: "expired"},
		{Agent: "qoder", User: "u2", Auth: authOf(t, c2)},
	}, func(t *testing.T, a, b savedLogin) {
		ca, _ := qoderSaved(a)
		cb, _ := qoderSaved(b)
		if string(cb.Models) == "" {
			cb.Models = ca.Models // a new entry: the model list is the built-in's own cache
		}
		if !reflect.DeepEqual(ca, cb) {
			t.Fatalf("qoder %s: %+v, want %+v", a.User, cb, ca)
		}
	})
}

// A Qoder CN account keeps its site, and one that chats on its device
// token keeps doing so.
func TestQoderCNMover(t *testing.T) {
	c1 := qoder.Credential{Site: QoderCNID, UID: "u1", Email: "a@q.cn", Token: "d1", RefreshToken: "dr1", DeviceToken: "d1", DeviceRefresh: "dr1", DeviceChat: true, ExpiresAt: 1_900_000_000_123, MachineID: "m1"}
	c2 := qoder.Credential{Site: QoderCNID, UID: "u2", Token: "t2", RefreshToken: "r2", DeviceToken: "d2", ExpiresAt: 1_900_000_000_456, MachineID: "m2"}
	roundTrip(t, QoderCNID, []savedLogin{
		{Agent: QoderCNID, User: "a@q.cn", First: true, On: true, Auth: authOf(t, c1)},
		{Agent: QoderCNID, User: "u2", Auth: authOf(t, c2)},
	}, func(t *testing.T, a, b savedLogin) {
		ca, _ := qoderSaved(a)
		cb, _ := qoderSaved(b)
		if !reflect.DeepEqual(ca, cb) {
			t.Fatalf("qoder-cn %s: %+v, want %+v", a.User, cb, ca)
		}
	})
}

func TestFactoryMover(t *testing.T) {
	c1 := factoryCreds{Access: "a1", Refresh: "r1", ExpiresAt: 1_900_000_000_000, Org: "org_1", Active: "fo1", Email: "a@f.com", UserID: "user_1", Region: "eu", Prem: "llm.example.com"}
	c2 := factoryCreds{Access: "a2", Refresh: "r2", ExpiresAt: 1_900_000_000_001, UserID: "user_2"}
	roundTrip(t, "factory", []savedLogin{
		{Agent: "factory", User: "user_2", First: true, Auth: authOf(t, c2)},
		{Agent: "factory", User: "a@f.com", On: true, Auth: authOf(t, c1)},
	}, func(t *testing.T, a, b savedLogin) {
		ca, _ := factorySaved(a)
		cb, _ := factorySaved(b)
		if ca != cb {
			t.Fatalf("factory %s: %+v, want %+v", a.User, cb, ca)
		}
	})
}

func TestMiMoMover(t *testing.T) {
	issued := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c1 := mimoCreds{UserID: "123", CUserID: "c123", PassToken: "p1", DeviceID: "dev1", Region: "sg", Base: "https://aistudio.xiaomimimo.com", Cookies: map[string]string{"serviceToken": "s1", "userId": "123"}, Issued: issued, Name: "Mi One"}
	c2 := mimoCreds{UserID: "456", PassToken: "p2", DeviceID: "dev2", Base: "https://aistudio.xiaomimimo.com", Cookies: map[string]string{"serviceToken": "s2"}, Issued: issued.Add(time.Hour)}
	roundTrip(t, MiMoID, []savedLogin{
		{Agent: MiMoID, User: "123", First: true, On: true, Auth: authOf(t, c1)},
		{Agent: MiMoID, User: "456", On: true, Auth: authOf(t, c2)},
	}, func(t *testing.T, a, b savedLogin) {
		ca, _ := mimoSaved(a)
		cb, _ := mimoSaved(b)
		if cb.Name == "" {
			cb.Name = ca.Name // a new entry: the name is only the built-in's label
		}
		if !reflect.DeepEqual(ca, cb) {
			t.Fatalf("mimo %s: %+v, want %+v", a.User, cb, ca)
		}
	})
	// the plugin keeps a numeric id as a number
	ls, u, err := movers[MiMoID].back(nil, "", map[string]any{"type": "oauth", "refresh": `{"userId":789,"passToken":"p","base":"https://b"}`, "access": `{}`, "expires": float64(issued.UnixMilli())})
	if c, ok := mimoSaved(ls[0]); err != nil || u != "789" || !ok || c.UserID != "789" {
		t.Fatalf("numeric id: %q %+v %v", u, ls, err)
	}
}
