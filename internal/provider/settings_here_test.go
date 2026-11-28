package provider

import (
	"reflect"
	"slices"
	"testing"
)

// keyAndListFields are the fields of Provider that sync with
// ProviderSettingsHere on: its place in the list and its keys, with what
// goes with a key (settingFields' comment). hereFields are kept on each
// computer whatever sync is set to (Mirror, ThisDevice), and Account is
// never stored.
var (
	keyAndListFields = []string{
		"ID", "Name", "Was", "Gave", "Icon", "IconFrom", "Preset", "Catalog", "Website", "KeysURL", "IconURL",
		"Hidden", "Quiet", "Tucked", "Forgot", "AccountNames",
		"Key", "AccessKeyID", "SecretAccessKey", "SessionToken", "KeyName", "Keys", "KeyProtocol", "KeyWeight", "KeyPlan",
		"Chat", "Responses", "Anthropic", "Gemini", "GeminiFilled", "Decide", "BaseAPI", "ModelsURL", "Addresses",
		"AuthHeader", "ClientCert", "ClientKey", "ServerCA", "ServerPin", "ZhipuTeam", "Vertex",
	}
	hereFields = []string{"KeepLogin", "KeepLoginAs", "SwitchOnly", "AutoSwitch", "RestartApp", "KeepSelf", "ThisDevice", "Account"}
)

// Every field of Provider is either a setting, kept on each computer when
// only the list and keys sync, or goes with the list and keys: a field
// added to Provider fails here until it is put in one.
func TestProviderFieldsSortedForSync(t *testing.T) {
	seen := map[string]int{}
	for _, l := range [][]string{settingFields, keyAndListFields, hereFields} {
		for _, f := range l {
			seen[f]++
		}
	}
	typ := reflect.TypeFor[Provider]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		switch seen[f.Name] {
		case 0:
			t.Errorf("Provider.%s is in neither settingFields nor keyAndListFields: does it sync with only the list and keys?", f.Name)
		case 1:
		default:
			t.Errorf("Provider.%s is listed %d times", f.Name, seen[f.Name])
		}
		delete(seen, f.Name)
	}
	for f := range seen {
		t.Errorf("%s is listed but Provider has no such field", f)
	}
}

// WithSettingsOf takes the other's settings and keeps p's list place and
// keys, its secret headers among them; WithoutSettings leaves the key
// and the secret headers alone.
func TestWithSettingsOf(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://new.example/v1", Key: "k2",
		Headers: map[string]string{"Authorization": "Bearer new", "X-Team": "theirs"}, Proxy: "http://theirs", MaxRPM: 5, Models: []string{"a"}}
	here := Provider{ID: "relay", Name: "Old", Chat: "https://old.example/v1", Key: "k1",
		Headers: map[string]string{"Authorization": "Bearer old", "X-Team": "mine", "X-Mine": "1"}, Proxy: "socks5://mine", MaxRPM: 60, Off: true, Models: []string{"b", "c"}}
	got := WithSettingsOf(p, here)
	if got.Name != "Relay" || got.Chat != "https://new.example/v1" || got.Key != "k2" {
		t.Errorf("list and key not kept: %+v", got)
	}
	if got.Proxy != "socks5://mine" || got.MaxRPM != 60 || !got.Off || !slices.Equal(got.Models, []string{"b", "c"}) {
		t.Errorf("settings not taken: proxy %q rpm %d off %t models %v", got.Proxy, got.MaxRPM, got.Off, got.Models)
	}
	if want := map[string]string{"Authorization": "Bearer new", "X-Team": "mine", "X-Mine": "1"}; !reflect.DeepEqual(got.Headers, want) {
		t.Errorf("headers %v, want %v", got.Headers, want)
	}
	if p.Headers["X-Team"] != "theirs" || here.Headers["Authorization"] != "Bearer old" {
		t.Error("a map passed in was changed")
	}
	// a secret header the incoming copy dropped is gone, never kept from here
	dropped := WithSettingsOf(Provider{ID: "relay", Key: "k3"}, here)
	if want := map[string]string{"X-Team": "mine", "X-Mine": "1"}; !reflect.DeepEqual(dropped.Headers, want) {
		t.Errorf("headers after the secret one was dropped %v, want %v", dropped.Headers, want)
	}
	bare := WithoutSettings(here)
	if bare.Key != "k1" || bare.Proxy != "" || bare.MaxRPM != 0 || bare.Off || bare.Models != nil {
		t.Errorf("WithoutSettings: %+v", bare)
	}
	if want := map[string]string{"Authorization": "Bearer old"}; !reflect.DeepEqual(bare.Headers, want) {
		t.Errorf("WithoutSettings' headers %v, want %v", bare.Headers, want)
	}
}
