//go:build linux

package server

import "sync"

// RestartSignal carries a product restart request to the listener, which
// ends the process so its supervisor starts a fresh one.
type RestartSignal struct {
	mu        sync.Mutex
	onRestart func()
}

func (s *RestartSignal) OnRestart(fn func()) {
	s.mu.Lock()
	s.onRestart = fn
	s.mu.Unlock()
}

func (s *RestartSignal) Request() {
	s.mu.Lock()
	fn := s.onRestart
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
