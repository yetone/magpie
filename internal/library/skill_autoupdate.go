package library

// Skills kept up to date by themselves (0xYaCo on X: "skill 能自动化升级吗？").
// With the switch on (AutoUpdateSkills), the gateway checks the skills
// from GitHub and other git repositories as the Library page does, a
// little after magpie starts and every checkEvery after, and fetches
// again each one the check found changed there. Everything an update by
// hand does holds: a skill changed here is left as it is and keeps its
// Update button (updateSkills refuses it), one removed there is only
// marked, and a replaced version that was changed goes to the backups.
// Off, the default, the page shows what changed and the user updates it.

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
)

// SetAutoUpdateSkills turns updating the skills by themselves on or off.
func SetAutoUpdateSkills(on bool) (*Result, error) {
	return change(func(l *Library) error {
		l.AutoUpdateSkills = on
		return nil
	})
}

const (
	// autoUpdateFirst is how long after magpie starts the first look is:
	// the start itself has enough to do
	autoUpdateFirst = 2 * time.Minute
	// autoUpdatePoll is how often the loop asks whether a look is due, so
	// switching on is acted on within the hour
	autoUpdatePoll = time.Hour
)

// autoUpdate is when the last look by itself was, and its error, said
// once in the log until it changes.
var autoUpdate struct {
	sync.Mutex
	at  time.Time
	err string
}

// AutoUpdateTick looks for changed skills and updates them, when the
// switch is on and no look was made for checkEvery. It returns what the
// update did, nil when nothing was due or nothing had changed.
func AutoUpdateTick(now time.Time) (*Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !l.AutoUpdateSkills || !slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.Source.remote() }) {
		return nil, nil
	}
	autoUpdate.Lock()
	due := autoUpdate.at.IsZero() || now.Sub(autoUpdate.at) >= checkEvery
	if due {
		autoUpdate.at = now
	}
	autoUpdate.Unlock()
	if !due {
		return nil, nil
	}
	checks, err := CheckSkills()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range checks {
		if c.Status == "update" {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	return UpdateSomeSkills(names)
}

// KeepSkillsCurrent runs AutoUpdateTick while the gateway serves.
func KeepSkillsCurrent(ctx context.Context) {
	t := time.NewTimer(autoUpdateFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(autoUpdatePoll)
		res, err := AutoUpdateTick(time.Now())
		autoUpdate.Lock()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != autoUpdate.err {
			if msg != "" {
				log.Printf("updating skills by themselves: %s", msg)
			}
			autoUpdate.err = msg
		}
		autoUpdate.Unlock()
		if res != nil && len(res.Updated) > 0 {
			log.Printf("skills updated by themselves: %s", strings.Join(res.Updated, ", "))
		}
	}
}
