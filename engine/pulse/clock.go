package pulse

import (
	"sort"
	"sync"
	"time"
)

// Stopper cancels a scheduled function.
type Stopper interface{ Stop() bool }

// Clock abstracts time so timers can be tested deterministically.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f in its own goroutine (RealClock) or inside Advance
	// (ManualClock) once d has elapsed.
	AfterFunc(d time.Duration, f func()) Stopper
}

// RealClock uses the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) AfterFunc(d time.Duration, f func()) Stopper {
	return time.AfterFunc(d, f)
}

// ManualClock is a deterministic clock for tests: time only moves when
// Advance is called, and due functions run synchronously inside Advance.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
	seq    int
}

type manualTimer struct {
	c    *ManualClock
	due  time.Time
	seq  int
	f    func()
	done bool
}

func NewManualClock(start time.Time) *ManualClock { return &ManualClock{now: start} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) AfterFunc(d time.Duration, f func()) Stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &manualTimer{c: c, due: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.done {
		return false
	}
	t.done = true
	return true
}

// Pending is the number of timers that have neither fired nor been stopped.
func (c *ManualClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves time forward by d, running every timer that becomes due
// (including timers scheduled by the functions it runs) in due order.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var live []*manualTimer
		for _, t := range c.timers {
			if !t.done {
				live = append(live, t)
			}
		}
		c.timers = live
		sort.Slice(c.timers, func(i, j int) bool {
			if c.timers[i].due.Equal(c.timers[j].due) {
				return c.timers[i].seq < c.timers[j].seq
			}
			return c.timers[i].due.Before(c.timers[j].due)
		})
		if len(c.timers) == 0 || c.timers[0].due.After(target) {
			c.now = target
			c.mu.Unlock()
			return
		}
		t := c.timers[0]
		t.done = true
		if t.due.After(c.now) {
			c.now = t.due
		}
		c.mu.Unlock()
		t.f()
	}
}
