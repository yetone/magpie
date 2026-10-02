package provider

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

// Zed's sign-in, run by magpie as the editor runs it: a key is made, the
// browser opens zed.dev's sign-in page with its public half and the port
// magpie listens on, and comes back there with the account's id and its
// access token encrypted to the key.

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/zed"
)

func startZedSignIn(s *signInFlow) error {
	key, pub, err := zed.NewKey()
	if err != nil {
		return fmt.Errorf("Zed sign-in: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("Zed sign-in: %w", err)
	}
	systemID := zed.NewSystemID()
	port := ln.Addr().(*net.TCPAddr).Port
	type callback struct{ uid, token string }
	got := make(chan callback, 1)
	mux := http.NewServeMux()
	// Zed's page sends the browser to the port on whatever path; the query is
	// what counts
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		uid, tok := q.Get("user_id"), q.Get("access_token")
		if uid == "" || tok == "" {
			http.NotFound(w, r)
			return
		}
		if !s.claim() {
			// its address was pasted too, and that one is being finished
			signInPage(w, false, "This sign-in is already finishing", "magpie shows the account when it's done.")
			return
		}
		select {
		case got <- callback{uid, tok}:
		default:
		}
		http.Redirect(w, r, zedSite+"/native_app_signin_succeeded", http.StatusFound)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.st.URL = zed.SignInURL(zedSite, port, pub, systemID)
	s.srv, s.stop = srv, cancel
	s.redirect = fmt.Sprintf("http://127.0.0.1:%d", port)
	// a browser that can't reach the port finishes it with its address
	s.st.PasteCallback = true
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()

	go func() {
		defer cancel()
		var cb callback
		select {
		case cb = <-got:
		case <-ctx.Done():
			return
		case <-time.After(signInTimeout):
			s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
			return
		}
		user, err := zedSignedInWith(ctx, key, cb.uid, cb.token, systemID)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: err.Error()})
			return
		}
		s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(zedSide()), user)})
	}()
	return nil
}

// zedSignedInWith reads the callback's token, asks Zed who the account is,
// keeps it and reads its models.
func zedSignedInWith(ctx context.Context, key *rsa.PrivateKey, uid, ciphertext, systemID string) (string, error) {
	access, err := zed.Decrypt(key, ciphertext)
	if err != nil {
		return "", fmt.Errorf("Zed sign-in: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	me, err := zed.FetchMe(ctx, zedClient, zedCloud, uid, access, systemID)
	if err != nil {
		return "", err
	}
	org := me.Org()
	if me.ModelsOff(org) {
		return "", errors.New("Zed: this account's organization has Zed's hosted models turned off")
	}
	c := zedCreds{UserID: uid, Access: access, SystemID: systemID, Org: org,
		Login: firstNonEmpty(me.User.GitHubLogin, me.User.Username), Name: me.User.Name, Plan: me.PlanID(org)}
	who := me.Who()
	// the account's own list, so the picker has it before its first request
	if tok, err := zed.LLMToken(ctx, zedClient, zedCloud, uid, access, systemID, org); err == nil {
		if raw, _, err := zed.FetchModels(ctx, zedClient, zedCloud, tok); err == nil {
			if _, err := zed.ParseModels(raw); err == nil {
				c.Models = raw
			}
		}
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := addSideLogin(savedLogin{Agent: "zed", User: who, Plan: zed.PlanName(c.Plan), Auth: auth}, "", func(savedLogin) {}); err != nil {
		return "", err
	}
	// signed in again: whatever Zed refused before is over
	_ = editSideLogin("zed", who, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = ""
		return ls, nil
	})
	zedTokens.forget(who)
	forgetAccountCaches()
	return who, nil
}
