package agent

// Dim alpha keeps its Providers in a database its daemon owns
// (<home>/rust/dimcode.sqlite, with a version per record that every write
// is checked against), and changes them only through the daemon's
// JSON-RPC API, the one Dim's own CLI and desktop app use
// (crates/dim-app-server/src/provider/management.rs,
// docs/design/appserver/provider-management.md). magpie talks to the
// daemon the same way: newline-delimited JSON-RPC 2.0 after a hello of
// protocol 4, on <home>/rust/daemon.sock on macOS and Linux, and on
// Windows through the authenticated pipe bridge Dim's client starts
// (`dimcode-server app-server --connect-pipe <pipe>`, dim-client's
// Client::open). Without a daemon, `dima models magpie` starts one, as
// any dima command does, and lists nothing.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// errDimAway is Dim's daemon not reachable, and none started.
var errDimAway = errors.New("Dim's daemon isn't running")

// dimOpen connects to the daemon of Dim's home dir, starting it when
// start and it isn't running; tests swap it for a fake daemon.
var dimOpen = openDimDaemon

func openDimDaemon(dir string, start bool) (io.ReadWriteCloser, error) {
	c, err := dimDial(dir)
	if err == nil || !start || proc.Isolated() {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errDimAway, err)
		}
		return c, nil
	}
	bin := proc.FindTool("dima")
	if bin == "" {
		return nil, fmt.Errorf("%w: dima isn't installed", errDimAway)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// any dima command starts the daemon; this one lists the Magpie
	// provider's models and changes nothing
	cmd := proc.CommandContext(ctx, bin, "models", "magpie")
	cmd.Env = append(os.Environ(), "DIMCODE_HOME="+dir)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: dima models: %v", errDimAway, err)
	}
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if c, err = dimDial(dir); err == nil || time.Now().After(end) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDimAway, err)
	}
	return c, nil
}

// dimConn is one connection to Dim's daemon, greeted.
type dimConn struct {
	rw   io.ReadWriteCloser
	r    *bufio.Reader
	id   int
	stop *time.Timer
}

// dimError is an error the daemon answered, with Dim's own code
// (provider_conflict, provider_not_found, provider_invalid_config…).
type dimError struct {
	Message string `json:"message"`
	Data    struct {
		AppError string `json:"appError"`
	} `json:"data"`
}

func (e *dimError) Error() string { return "Dim: " + e.Message }

func dimCode(err error) string {
	var e *dimError
	if errors.As(err, &e) {
		return e.Data.AppError
	}
	return ""
}

// dimConnect is a greeted connection, closed after a minute at most
// whatever it waits on.
func dimConnect(dir string, start bool) (*dimConn, error) {
	rw, err := dimOpen(dir, start)
	if err != nil {
		return nil, err
	}
	c := &dimConn{rw: rw, r: bufio.NewReaderSize(rw, 64<<10)}
	c.stop = time.AfterFunc(time.Minute, func() { rw.Close() })
	var hello struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := c.call("hello", map[string]any{"protocolVersion": 4, "clientKind": "other", "clientName": "magpie"}, &hello); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *dimConn) close() {
	c.stop.Stop()
	c.rw.Close()
}

// call sends method and reads its answer into out (nil to drop it);
// notifications and other answers on the way are skipped.
func (c *dimConn) call(method string, params, out any) error {
	c.id++
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := c.rw.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("Dim %s: %w", method, err)
	}
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("Dim %s: %w", method, err)
		}
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *dimError       `json:"error"`
		}
		if json.Unmarshal(line, &m) != nil || m.ID == nil || *m.ID != c.id {
			continue
		}
		if m.Error != nil {
			return m.Error
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	}
}

// dimProvider is the part of a Provider record (provider.management)
// magpie reads.
type dimProvider struct {
	ID         string `json:"providerId"`
	Version    int    `json:"version"`
	Enabled    bool   `json:"enabled"`
	Connection struct {
		BaseURL              string `json:"baseUrl"`
		CredentialConfigured bool   `json:"credentialConfigured"`
	} `json:"connection"`
}

// dimModel is a model of a provider's catalog (provider.refresh).
type dimModel struct {
	ID            string   `json:"id"`
	MissingLimits []string `json:"missingLimits"`
}

type dimCatalog struct {
	Models  []dimModel `json:"models"`
	Version int        `json:"version"`
}

// dimSelection is Dim's picked model (provider.selection).
type dimSelection struct {
	Provider string `json:"provider_id"`
	Model    string `json:"model_id"`
	Version  int    `json:"version,omitempty"`
}

func (c *dimConn) provider(id string) (*dimProvider, error) {
	var r struct {
		Providers []dimProvider `json:"providers"`
	}
	if err := c.call("provider.management", map[string]any{"providerId": id}, &r); err != nil {
		return nil, err
	}
	for i := range r.Providers {
		if r.Providers[i].ID == id {
			return &r.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("Dim has no %s provider", id)
}

func (c *dimConn) update(p *dimProvider, patch map[string]any) error {
	return c.call("provider.update", map[string]any{"providerId": p.ID, "expectedVersion": p.Version, "patch": patch}, nil)
}

func (c *dimConn) selection() (*dimSelection, error) {
	var r struct {
		Selection *dimSelection `json:"selection"`
	}
	if err := c.call("provider.selection", map[string]any{}, &r); err != nil {
		return nil, err
	}
	return r.Selection, nil
}

// pick makes provider/model Dim's model, over the pick of version was
// (nil: none yet).
func (c *dimConn) pick(provider, model string, was *dimSelection) error {
	p := map[string]any{"providerId": provider, "modelId": model}
	if was != nil {
		p["expectedVersion"] = was.Version
	}
	return c.call("provider.select", p, nil)
}

// dimMu keeps one change of Dim's providers at a time.
var dimMu sync.Mutex

// dimURL is a base URL as Dim compares it.
func dimURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }
