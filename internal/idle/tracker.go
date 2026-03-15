package idle

import (
	"sync"
	"time"
)

// Tracker records the last time each resource was accessed.
type Tracker struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func NewTracker() *Tracker {
	return &Tracker{lastSeen: make(map[string]time.Time)}
}

func (t *Tracker) Touch(name string) {
	t.mu.Lock()
	t.lastSeen[name] = time.Now()
	t.mu.Unlock()
}

func (t *Tracker) Remove(name string) {
	t.mu.Lock()
	delete(t.lastSeen, name)
	t.mu.Unlock()
}

func (t *Tracker) Get(name string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts, ok := t.lastSeen[name]
	return ts, ok
}

// IdleNames returns names that have been idle longer than the given timeout.
func (t *Tracker) IdleNames(timeout time.Duration) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-timeout)
	var idle []string
	for name, last := range t.lastSeen {
		if last.Before(cutoff) {
			idle = append(idle, name)
		}
	}
	return idle
}
