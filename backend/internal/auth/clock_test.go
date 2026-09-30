package auth_test

import (
	"sync"
	"time"
)

// steppingClock is a clock a test moves by hand, so an expiry can be reached
// without the test sleeping for it.
type steppingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func (c *steppingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *steppingClock) Now() time.Time                  { return c.now() }
func (c *steppingClock) Since(t time.Time) time.Duration { return c.now().Sub(t) }
func (c *steppingClock) Nanos() int64                    { return c.now().UnixNano() }
