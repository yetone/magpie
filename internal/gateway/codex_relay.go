package gateway

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// CodexRelay is what serves Codex's own ChatGPT sign-in at the gateway's
// address once magpie has quit, for the Codex threads still open then
// (codexrelay). magpie takes the failover base URL out of Codex's config
// at quit (agent.EndCodexFailover), but a thread open in the Codex app,
// and the app-server holding it, keep the URL they started with: every
// turn after went to a port nobody listened on, and a system proxy in
// between answered 503 "Unknown error" until magpie was opened again
// (idkatbro on X). The relay passes those turns on to the ChatGPT backend
// as the gateway does a turn on one of Codex's own models, signed by the
// caller's own Authorization; it adds no credential of its own. What only
// the running magpie could do is refused with a reason Codex shows: a
// magpie model, a turn on one of magpie's accounts, masking (Settings ›
// Privacy) that would have masked it. Its answer at "/" never says it is
// magpie (ServedBy), so the next magpie starting asks it to leave.
func CodexRelay() http.Handler {
	// the caller's sign-in goes to the ChatGPT backend's origin only
	client := &http.Client{Transport: upstreamTransport(), CheckRedirect: provider.KeepsCredentials}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) || r.Header.Get("Origin") != "" {
			// as the gateway's corsGuard: a rebound or foreign page, or
			// a frame's null, never reaches the sign-in Codex sends
			relayError(w, http.StatusForbidden, "magpie's Codex relay serves Codex on this computer only")
			return
		}
		if !strings.HasPrefix(r.URL.Path, CodexPath+"/") {
			relayError(w, http.StatusServiceUnavailable, relayGone)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "magpie speaks HTTP", http.StatusUpgradeRequired)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, CodexPath)
		body, status, err := readBoundedRequestBody(w, r, requestLimits{body: codexBodyLimit()})
		if err != nil {
			relayError(w, status, err.Error())
			return
		}
		if r.Method == http.MethodPost && (rest == "/responses" || rest == "/responses/compact") {
			if msg := relayRefuses(r, body); msg != "" {
				// 400, not 503: Codex retries a 5xx five times over for
				// a refusal that a retry doesn't change (seen with codex
				// 0.162.1), and shows a 4xx's message at once
				relayError(w, http.StatusBadRequest, msg)
				return
			}
			// as codexBackend sends a turn of Codex's own to its sign-in
			body = plainItemIDs(boundCallIDs(callItemIDs(body)))
			body, _ = codexInput(body, false)
		}
		body = withoutClaudeSeals(body)
		base := provider.CodexBase
		if apiKey(r.Header) && rest != "/models" {
			base = codexAPIBase
		}
		if r.Method == http.MethodPost && rest == "/responses" && base == provider.CodexBase {
			body = provider.DeclareSearch(body, strings.EqualFold(r.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"), "true"))
		}
		u := base + rest
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		req, err := http.NewRequestWithContext(provider.ViaSignedIn(r.Context(), "codex"), r.Method, u, bytes.NewReader(body))
		if err != nil {
			relayError(w, http.StatusBadGateway, err.Error())
			return
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Del("Accept-Encoding")
		provider.CodexCompressed(req, body)
		res, err := client.Do(req)
		if err != nil {
			relayError(w, http.StatusBadGateway, "OpenAI can't be reached ("+err.Error()+"). magpie isn't running, so this thread goes to OpenAI on Codex's own sign-in.")
			return
		}
		defer res.Body.Close()
		for k, vs := range res.Header {
			if !hopHeader(k) {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(res.StatusCode)
		flush := http.NewResponseController(w)
		buf := make([]byte, 32<<10)
		for {
			n, err := res.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				flush.Flush()
			}
			if err != nil {
				return
			}
		}
	})
}

// relayGone is the relay's answer to anything but Codex's own turns.
const relayGone = "magpie isn't running: only Codex threads open when it quit are passed on, on Codex's own ChatGPT sign-in. Open magpie for everything else."

// relayRefuses says why a turn can't be passed on with magpie gone, ""
// when it can: what only the running gateway serves, and a turn masking
// would have masked, which never goes unmasked.
func relayRefuses(r *http.Request, body []byte) string {
	model := modelOf(body)
	if strings.Contains(model, "/") {
		return model + " is one of magpie's models, and magpie isn't running. Open magpie to go on with this thread, or pick one of Codex's own models."
	}
	if r.Header.Get(AccountHeader) != "" {
		return AccountHeader + " names one of magpie's Codex accounts, and magpie isn't running. Open magpie to go on with this thread."
	}
	if masking.Wanted(settings.Load()) {
		return "magpie isn't running, and Settings › Privacy masks what is sent: this turn is not sent unmasked. Open magpie to go on with this thread."
	}
	return ""
}

// relayError answers as the ChatGPT backend words an error, so Codex
// shows the message (an empty body reads "Unknown error").
func relayError(w http.ResponseWriter, status int, msg string) {
	writeError(w, provider.Responses, status, msg)
}
