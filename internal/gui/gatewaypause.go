package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Pausing the gateway (me2seeks on X): told the gateway runs while magpie
// does, the user quit magpie with Disconnect and quit to have it stopped,
// and asked for a switch. Pause, from the tray's menu, Settings › General
// or the Gateway page, does what Disconnect and quit does without the
// quit: each connected agent is disconnected as its row's Disconnect does
// (its own settings put back, so it works without magpie), then the
// gateway stops listening. Resume serves it again and connects the agents
// that were connected at the pause. The pause is kept in
// gateway-paused.json, this computer's alone (no backup or sync carries
// it), so a magpie started again, or one updated, stays paused, and
// nothing starts the gateway meanwhile (serveGatewayLocked): not the
// watch for a gateway gone, a port change, the Gateway page's Start
// (which resumes) nor a handover. Connecting an agent, or picking one of
// magpie's models for it, while paused resumes: the agent would otherwise
// point at a gateway that isn't there, the 502s of quitask.go. It is the
// app's own: magpie web is a server whose job is the gateway.

// pausable says this process may pause its gateway: the app's (Run).
var pausable atomic.Bool

// pauseMu keeps one pause or resume at a time.
var pauseMu sync.Mutex

// onPaused tells the tray's menu the gateway was paused or resumed; set
// by the process that has the tray.
var onPaused func()

// pauseFile is gateway-paused.json: the agents connected at the pause,
// which a resume connects again.
type pauseFile struct {
	Agents []string `json:"agents"`
}

func pausePath() string { return filepath.Join(settings.Dir(), "gateway-paused.json") }

// gatewayPaused says whether this process's gateway is paused, and the
// agents to connect again on resume. A file that is there but can't be
// read or parsed is a pause whose agents aren't known: only magpie writes
// it, and a gateway started on a bad read would serve agents put back on
// their own settings.
func gatewayPaused() (bool, []string) {
	if !pausable.Load() {
		return false, nil
	}
	b, err := os.ReadFile(pausePath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	var f pauseFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		log.Println("gateway pause:", err)
	}
	return true, f.Agents
}

// pausedNow is gatewayPaused's first answer.
func pausedNow() bool { p, _ := gatewayPaused(); return p }

// pauseJSON is how a pause or resume went: Paused is the state after it;
// Agent and Error say which agent couldn't be disconnected, or connected
// again, and why; Fix is a resume's gateway that couldn't listen.
type pauseJSON struct {
	Paused bool            `json:"paused"`
	Agents []string        `json:"agents,omitempty"`
	Agent  string          `json:"agent,omitempty"`
	Error  string          `json:"error,omitempty"`
	Fix    *gatewayFixJSON `json:"fix,omitempty"`
}

// pause hooks, vars for the tests: the agents connected now, whether one
// is, and an agent's Disconnect and connect.
var (
	pauseWired      = func() []*agent.Agent { return wiredOf(agent.Detected()) }
	pauseIsWired    = func(a *agent.Agent) bool { return a.Wired() }
	pauseDisconnect = func(a *agent.Agent) error { return a.Disconnect() }
	pauseConnect    = func(a *agent.Agent) error { _, err := a.ConnectHow(); return err }
	pauseFind       = agent.Find
)

// wiredOf is those of as with magpie in their config.
func wiredOf(as []*agent.Agent) []*agent.Agent {
	var out []*agent.Agent
	for _, a := range as {
		if a.Wired() {
			out = append(out, a)
		}
	}
	return out
}

// pauseGateway disconnects every connected agent and stops the gateway
// this process serves. An agent that can't be disconnected leaves the
// gateway serving, with those disconnected before it connected again.
func pauseGateway() pauseJSON {
	pauseMu.Lock()
	defer pauseMu.Unlock()
	if p, ids := gatewayPaused(); p {
		return pauseJSON{Paused: true, Agents: ids}
	}
	if !pausable.Load() {
		return pauseJSON{Error: "only the app pauses its gateway"}
	}
	var done []*agent.Agent
	ids := []string{}
	for _, a := range pauseWired() {
		if err := pauseDisconnect(a); err != nil {
			for _, b := range done {
				if err := pauseConnect(b); err != nil {
					log.Printf("gateway pause: %s not connected again: %v", b.ID, err)
				}
			}
			return pauseJSON{Agent: a.Name, Error: err.Error()}
		}
		done = append(done, a)
		ids = append(ids, a.ID)
	}
	b, _ := json.Marshal(pauseFile{Agents: ids})
	err := os.MkdirAll(filepath.Dir(pausePath()), 0o755)
	if err == nil {
		err = edit.WriteAtomic(pausePath(), b)
	}
	if err != nil {
		for _, b := range done {
			_ = pauseConnect(b)
		}
		return pauseJSON{Error: err.Error()}
	}
	gatewayMu.Lock()
	stopServedLocked()
	gatewayMu.Unlock()
	log.Printf("gateway: paused; %d agents disconnected", len(ids))
	if onPaused != nil {
		onPaused()
	}
	return pauseJSON{Paused: true, Agents: ids}
}

// stopServedLocked stops the gateway this process serves and waits until
// it has; under gatewayMu.
func stopServedLocked() {
	gw := served.Load()
	if gw == nil {
		return
	}
	if servedRun.stop != nil {
		servedRun.stop()
		<-servedRun.done
	}
	served.CompareAndSwap(gw, nil)
}

// resumeGateway serves the gateway again and connects the agents the pause
// disconnected. A gateway that can't listen keeps the pause, with what
// holds the port. An agent that can't be connected is said; the rest are.
func resumeGateway() pauseJSON {
	pauseMu.Lock()
	defer pauseMu.Unlock()
	p, ids := gatewayPaused()
	if !p {
		return pauseJSON{}
	}
	if err := os.Remove(pausePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return pauseJSON{Paused: true, Agents: ids, Error: err.Error()}
	}
	gatewayMu.Lock()
	out := startHere()
	gatewayMu.Unlock()
	if !out.OK {
		b, _ := json.Marshal(pauseFile{Agents: ids})
		_ = edit.WriteAtomic(pausePath(), b)
		return pauseJSON{Paused: true, Agents: ids, Fix: &out}
	}
	res := pauseJSON{Agents: ids}
	var errs []error
	for _, id := range ids {
		a, err := pauseFind(id)
		if err != nil || pauseIsWired(a) {
			continue // gone since, or connected by hand meanwhile
		}
		if err := pauseConnect(a); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
			if res.Agent == "" {
				res.Agent = a.Name
			}
		}
	}
	if len(errs) > 0 {
		res.Error = errors.Join(errs...).Error()
	}
	log.Printf("gateway: resumed; %d agents connected again", len(ids)-len(errs))
	if onPaused != nil {
		onPaused()
	}
	return res
}

// resumeOnWire resumes a paused gateway once an agent was put on it.
func resumeOnWire(a *agent.Agent) {
	if a != nil && pausedNow() && pauseIsWired(a) {
		resumeGateway()
	}
}

// gatewayPauseRoutes: POST /api/gateway/pause {"on": true|false}.
func gatewayPauseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/gateway/pause", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.On {
			writeJSON(rw, pauseGateway())
		} else {
			writeJSON(rw, resumeGateway())
		}
	})
}
