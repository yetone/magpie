package gateway

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/netproxy"
)

// mcpClient relays the agents' requests to a remote MCP server magpie is
// signed in to. It has no timeout: a GET's event stream stays open for as
// long as the agent keeps it.
var mcpClient = &http.Client{Transport: mcpTransport()}

func mcpTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	t.ResponseHeaderTimeout = 2 * time.Minute
	return netproxy.Dispatch(t)
}

// mcpHop are the headers a relay doesn't pass on.
var mcpHop = map[string]bool{
	"Authorization": true, "Host": true, "Connection": true, "Keep-Alive": true,
	"Proxy-Authorization": true, "Proxy-Connection": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true, "Content-Length": true, "Accept-Encoding": true,
}

// RunMCP serves /mcp/run/<server>: a server magpie runs by its command,
// for an agent on this computer given it that way (library's ServeRun);
// nil until the library sets it.
var RunMCP func(w http.ResponseWriter, r *http.Request, name string)

// runMCP is RunMCP, for this computer's agents alone: it starts a command
// the user gave their agents, and no other machine, and no web page (one
// posting a form, or of a name pointed at 127.0.0.1), may have it started.
func runMCP(w http.ResponseWriter, r *http.Request) {
	if !local(r) || r.Header.Get("Origin") != "" || !loopbackHost(r.Host) {
		http.Error(w, "magpie runs the Library's MCP servers for the agents on this computer alone", http.StatusForbidden)
		return
	}
	if RunMCP == nil {
		http.Error(w, "magpie runs no MCP server here", http.StatusNotFound)
		return
	}
	RunMCP(w, r, r.PathValue("name"))
}

// mcpProxy is /mcp/<server>: a remote MCP server magpie signed in to once,
// for every agent the Library gives it to (#615). The agent speaks to it as
// to the server itself (streamable HTTP: a POST answered with JSON or an
// event stream, a GET's event stream, a DELETE that ends the session, with
// Mcp-Session-Id), and magpie sends it on with its own token, renewing it
// when it runs out or the server says it has.
func (s *Server) mcpProxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !local(r) && !sharedWith(r) {
		http.Error(w, "magpie lends its MCP sign-ins to another machine only when magpie is shared on the local network and the request carries its API key", http.StatusForbidden)
		return
	}
	// a web page can make the browser send a request here too (a form, or
	// a hostname of its own pointed at 127.0.0.1), and magpie would carry
	// it to the server with the user's sign-in; agents send no Origin and
	// name this computer, as the MCP spec has servers check
	if r.Header.Get("Origin") != "" || !sharedWith(r) && !loopbackHost(r.Host) {
		http.Error(w, "magpie lends its MCP sign-ins to agents, not to web pages", http.StatusForbidden)
		return
	}
	rec, ok := mcpauth.Get(name)
	if !ok {
		http.Error(w, "magpie isn't signed in to an MCP server called "+name+": sign in to it in magpie's Library", http.StatusNotFound)
		return
	}
	// a body cut at the limit would go to the server as a shorter message
	body, ok := readMCPBody(w, r, 32<<20)
	if !ok {
		return
	}
	target, err := url.Parse(rec.URL)
	if err != nil {
		http.Error(w, "the server's URL isn't one: "+err.Error(), http.StatusBadGateway)
		return
	}
	if r.URL.RawQuery != "" {
		q := target.Query()
		for k, v := range r.URL.Query() {
			q[k] = v
		}
		target.RawQuery = q.Encode()
	}
	token, err := mcpauth.Token(r.Context(), name)
	if err != nil {
		mcpRefused(w, name, err)
		return
	}
	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range r.Header {
			// nor what a proxy in front of magpie says of the caller
			if mcpHop[http.CanonicalHeaderKey(k)] || proxyIdentity(k) {
				continue
			}
			// the gateway's own key, which the caller check put there
			if strings.EqualFold(k, "x-api-key") && len(v) == 1 && v[0] == Token {
				continue
			}
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body == nil {
			req.ContentLength = 0
		}
		return mcpClient.Do(req)
	}
	resp, err := send(token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		fresh, rerr := mcpauth.Renew(r.Context(), name, token)
		if rerr != nil {
			mcpRefused(w, name, rerr)
			return
		}
		resp, err = send(fresh)
		if err == nil && resp.StatusCode == http.StatusUnauthorized {
			// a token just renewed and refused all the same: the sign-in
			// has run out, as the Library then says (新v学员 on Discord)
			mcpauth.Rejected(name, fresh)
		}
	}
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("mcp %s: %v", name, err)
		}
		http.Error(w, "couldn't reach the MCP server "+name+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// the agent would start a sign-in of its own, at magpie's address
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		mcpRefused(w, name, mcpauth.ErrExpired)
		return
	}
	for k, v := range resp.Header {
		if mcpHop[http.CanonicalHeaderKey(k)] || strings.EqualFold(k, "WWW-Authenticate") {
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// mcpRefused answers an agent whose request magpie has no token for. It is
// not a 401, which would set the agent signing in itself.
func mcpRefused(w http.ResponseWriter, name string, err error) {
	status := http.StatusBadGateway
	msg := err.Error()
	if errors.Is(err, mcpauth.ErrExpired) || errors.Is(err, mcpauth.ErrNotSignedIn) {
		msg = "magpie's sign-in to the MCP server " + name + " has run out: sign in to it again in magpie's Library"
	}
	http.Error(w, msg, status)
}

// loopbackHost: a Host header naming this computer, localhost or a loopback
// address, with or without a port.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
