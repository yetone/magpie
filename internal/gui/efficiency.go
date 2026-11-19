package gui

import (
	"sync"

	"github.com/yetone/magpie/internal/proc"
)

// efficiencyCoordinator applies Windows' efficiency mode from power and
// foreground changes, once per change, under one lock so two events never
// apply their outcomes out of order.
type efficiencyCoordinator struct {
	mu      sync.Mutex
	power   func() proc.PowerState
	inFront func() bool
	apply   func(bool)
	applied bool
	stopped bool
}

func newEfficiencyCoordinator(power func() proc.PowerState, inFront func() bool, apply func(bool)) *efficiencyCoordinator {
	return &efficiencyCoordinator{power: power, inFront: inFront, apply: apply}
}

func (c *efficiencyCoordinator) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applied
}

func (c *efficiencyCoordinator) update() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	next := proc.EfficiencyActive(c.power(), c.inFront())
	if next == c.applied {
		return
	}
	c.applied = next
	c.apply(next)
}

func (c *efficiencyCoordinator) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.stopped = true
	if c.applied {
		c.applied = false
		c.apply(false)
	}
}
