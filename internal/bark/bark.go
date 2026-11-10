// Package bark pushes a message to the iOS Bark app (bark.day.app): one
// POST of JSON to the server's /push, with the device key in the body, so
// a key never sits in an address that a log, a proxy or an error names.
// The server is Bark's own, https://api.day.app, or a self-hosted one.
package bark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// DefaultServer is the Bark app's own server, used when none is set.
const DefaultServer = "https://api.day.app"

// Target is where a push goes: Server ("" is DefaultServer) and the device
// key the Bark app shows.
type Target struct {
	Server, Key string
}

// On is whether there is a device key to push to.
func (t Target) On() bool { return strings.TrimSpace(t.Key) != "" }

func (t Target) url() string {
	s := strings.TrimRight(strings.TrimSpace(t.Server), "/")
	if s == "" {
		s = DefaultServer
	}
	return s + "/push"
}

// client follows the app's proxy, as its other requests to the Internet do;
// a var for tests.
var client = &http.Client{Timeout: 15 * time.Second, Transport: proxied()}

func proxied() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// group names the pushes in the Bark app's list.
const group = "magpie"

// Push sends title and body to t. The error names the server's answer,
// never the device key.
func Push(ctx context.Context, t Target, title, body string) error {
	if !t.On() {
		return errors.New("no Bark device key is set")
	}
	b, _ := json.Marshal(map[string]any{
		"device_key": strings.TrimSpace(t.Key),
		"title":      title,
		"body":       body,
		"group":      group,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.url(), bytes.NewReader(b))
	if err != nil {
		return errors.New("the Bark server's address can't be used")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		// the client's error names the address, which holds no key
		return fmt.Errorf("Bark server: %s", strings.ReplaceAll(err.Error(), strings.TrimSpace(t.Key), "…"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var reply struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	json.Unmarshal(raw, &reply)
	if resp.StatusCode/100 == 2 && (reply.Code == 0 || reply.Code == 200) {
		return nil
	}
	say := strings.TrimSpace(reply.Message)
	if say == "" {
		say = http.StatusText(resp.StatusCode)
	}
	// a server may say the key back
	say = strings.ReplaceAll(say, strings.TrimSpace(t.Key), "…")
	return fmt.Errorf("Bark server answered %d: %s", resp.StatusCode, say)
}
