package auth

import (
	"context"
	"sync/atomic"
)

// GateConcurrency is how many Argon2 invocations may run at once. Peak memory
// is m_cost times this number: 48 MiB times 4 is 192 MiB, which is the whole
// reason 48 was chosen over 64 (S10: a per-hash setting that lets four
// concurrent logins exhaust the container is not stronger in practice). There
// is exactly one pool of permits and every hash or verify path shares it; a
// second, independent gate would silently double the real cap.
const GateConcurrency = 4

// Gate bounds concurrent Argon2 work with a fixed-size permit pool. A
// buffered channel is the whole primitive: send to acquire, receive to
// release. No reference counting to get wrong.
type Gate struct {
	permits chan struct{}

	// The two counters below exist for the concurrency proof: they observe
	// the peak in-flight count, which is the one number that shows the gate
	// actually bounds peak memory and is not just described as doing so.
	concurrent atomic.Int32
	highWater  atomic.Int32
}

// NewGate returns a gate with GateConcurrency permits.
func NewGate() *Gate {
	return &Gate{permits: make(chan struct{}, GateConcurrency)}
}

// Acquire blocks until a permit is free or ctx is done. A client that gives
// up stops waiting rather than queuing behind logins it no longer wants,
// which is exactly the DoS the gate exists to prevent arriving from the
// cancellation direction.
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case g.permits <- struct{}{}:
		c := g.concurrent.Add(1)
		g.raiseHighWater(c)
		return func() {
			g.concurrent.Add(-1)
			<-g.permits
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// raiseHighWater records the highest in-flight count seen.
func (g *Gate) raiseHighWater(c int32) {
	for {
		hw := g.highWater.Load()
		if c <= hw || g.highWater.CompareAndSwap(hw, c) {
			return
		}
	}
}

// PeakConcurrency is the highest number of simultaneous in-flight
// invocations this gate has admitted. It is the number the concurrency test
// asserts never exceeds GateConcurrency.
func (g *Gate) PeakConcurrency() int32 { return g.highWater.Load() }
