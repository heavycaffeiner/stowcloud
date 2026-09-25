//go:build linux

package restart

import "sync"

// Source connects a product restart request to the process supervisor.
type Signal struct {
	mu        sync.Mutex
	onRestart func()
}

func (s *Signal) OnRestart(fn func()) {
	s.mu.Lock()
	s.onRestart = fn
	s.mu.Unlock()
}

func (s *Signal) Request() {
	s.mu.Lock()
	fn := s.onRestart
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
