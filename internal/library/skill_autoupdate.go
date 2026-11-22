package library

// Skills kept up to date by themselves (0xYaCo on X: "skill 能自动化升级吗？").
// With the switch on (AutoUpdateSkills), the gateway checks the skills
// from GitHub and other git repositories as the Library page does, a
// little after magpie starts and every checkEvery after, and fetches
// again each one the check found changed there. Switching it on looks at
// once, and a check the page makes while it is on updates what it finds
// changed too (CHENGJIAHEGOXIS on X: switched on, the skills kept their
// old content: the look waited for the loop's next hour, and an update a
// check found between two looks waited up to checkEvery). Everything an
// update by hand does holds: a skill changed here is left as it is and keeps its
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
// Switched on, it looks at once and updates what changed there: its
// answer is that update's when one was made.
func SetAutoUpdateSkills(on bool) (*Result, error) {
	res, err := change(func(l *Library) error {
		l.AutoUpdateSkills = on
		return nil
	})
	if err != nil || !on {
		return res, err
	}
	_, up, err := lookNow(time.Now())
	if err != nil {
		log.Printf("updating skills by themselves: %s", err)
	}
	if up != nil {
		return up, nil
	}
	return res, nil
}

// CheckSkillsUpdating is CheckSkills, and with the switch on the update
// of each skill it found changed there, as a look by itself makes: up is
// that update, nil when none was made.
// An update that fails is logged: the check still answers what it found.
func CheckSkillsUpdating() (found []SkillCheck, up *Result, err error) {
	found, up, err = checkUpdating(time.Now())
	if err != nil && found != nil {
		log.Printf("updating skills by themselves: %s", err)
		return found, up, nil
	}
	return found, up, err
}

// lookNow is a look by itself made now, due or not: nothing with the
// switch off or no skill from a repository.
func lookNow(now time.Time) ([]SkillCheck, *Result, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if !l.AutoUpdateSkills || !slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.Source.remote() }) {
		return nil, nil, nil
	}
	return checkUpdating(now)
}

// checkUpdating checks the skills and, with the switch on, updates those
// found changed there. A check made with the switch on counts as a look
// by itself, so the next is checkEvery after it.
func checkUpdating(now time.Time) ([]SkillCheck, *Result, error) {
	found, err := CheckSkills()
	if err != nil {
		return nil, nil, err
	}
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil || !l.AutoUpdateSkills {
		return found, nil, nil
	}
	autoUpdate.Lock()
	if now.After(autoUpdate.at) {
		autoUpdate.at = now
	}
	autoUpdate.Unlock()
	var names []string
	for _, c := range found {
		if c.Status == "update" {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return found, nil, nil
	}
	up, err := UpdateSomeSkills(names)
	return found, up, err
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
	_, up, err := checkUpdating(now)
	return up, err
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
