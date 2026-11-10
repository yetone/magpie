package agent

// Every agent's telemetry off, from Settings › Privacy (OipsAnthony on X:
// "支持统一关闭所有 Agent 的遥测功能，这个统一管理的地方优先级最高").
// settings.AgentTelemetryOff is above each agent's own Telemetry row:
//
//   - On, every agent here with a switch (Agent.Telemetry) is turned off
//     through that switch, by its rules: only keys the file lacks are
//     written, and a value of the user's own that keeps telemetry on is
//     never written over (the agent stays on, and says why). An agent
//     found later is turned off as it is seen: on each state the window
//     reads, and by the gateway's loop (KeepTelemetryAllOff).
//   - An agent this switch turned off is marked in the stash
//     (Telemetry.all). One that was off already, by its own row or the
//     user's config, isn't: that is its own choice.
//   - Off, each marked agent is turned back on, which takes out only what
//     magpie wrote; the rest stay as their own rows had them.
//   - While it is on, an agent's own row is held (ErrTelemetryAllOff).
//
// A settings.json that can't be read is unknown: nothing is written.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// telemetryAllMu keeps the switch for every agent and an agent's own row
// from writing one file at once.
var telemetryAllMu sync.Mutex

// ErrTelemetryAllOff: an agent's own row while Settings › Privacy turns
// every agent's telemetry off.
var ErrTelemetryAllOff = errors.New("Settings › Privacy turns off every agent's telemetry; turn that off first")

// TelemetryAllOff says whether Settings › Privacy's switch is on, read
// without a copy (each poll asks).
func TelemetryAllOff() bool {
	s, err := settings.SharedChecked()
	return err == nil && s.AgentTelemetryOff
}

// KeepTelemetryOff turns a's telemetry off when the switch for every agent
// is on and it isn't off yet, marking it as the switch's. It says why it
// couldn't (a value of the user's own, a file that doesn't read).
func KeepTelemetryOff(a *Agent) error {
	if a.Telemetry == nil {
		return nil
	}
	telemetryAllMu.Lock()
	defer telemetryAllMu.Unlock()
	s, err := settings.SharedChecked()
	if err != nil || !s.AgentTelemetryOff {
		return err
	}
	return keepTelemetryOff(a.Telemetry)
}

func keepTelemetryOff(t *Telemetry) error {
	st, err := t.Get()
	if err != nil {
		return err
	}
	if st.Off {
		return nil
	}
	if err := t.Set(true); err != nil {
		return err
	}
	stash(map[string]string{t.all: "1"})
	return nil
}

// SetTelemetryAll saves Settings › Privacy's switch and carries it out:
// on, every agent found here is turned off; off, each the switch turned
// off is turned back on. The setting is saved whatever an agent says; the
// error names each agent that couldn't follow, and why.
func SetTelemetryAll(off bool) error {
	telemetryAllMu.Lock()
	defer telemetryAllMu.Unlock()
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	if s.AgentTelemetryOff != off {
		s.AgentTelemetryOff = off
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	var errs []error
	if off {
		for _, a := range Detected() {
			if a.Telemetry == nil {
				continue
			}
			if err := keepTelemetryOff(a.Telemetry); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
			}
		}
		return errors.Join(errs...)
	}
	// every agent, found here or not: one taken away since keeps no key of
	// magpie's behind
	for _, a := range All() {
		t := a.Telemetry
		if t == nil || stashShared()[t.all] == "" {
			continue
		}
		err := t.Set(false)
		if err != nil {
			// on as far as magpie goes: what keeps it off is the user's
			if st, gerr := t.Get(); gerr == nil && !hasOurs(st) {
				err = nil
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
			continue
		}
		forget(t.all)
	}
	return errors.Join(errs...)
}

// hasOurs: a state off by keys of magpie's, where Own are the user's
func hasOurs(st TelemetryState) bool { return st.Off && len(st.Own) < len(st.Keys) }

// KeepTelemetryAllOff turns off, while the switch for every agent is on,
// the telemetry of an agent found since: with no window open to read the
// state, the gateway's own look.
func KeepTelemetryAllOff(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if TelemetryAllOff() {
			for _, a := range Detected() {
				_ = KeepTelemetryOff(a)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
