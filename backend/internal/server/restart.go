//go:build linux

package server

import (
	"errors"
	"sync"

	hanamiprocess "github.com/heavycaffeiner/hanami/process"
)

// RestartSignal carries a product restart request to the process supervisor.
// The product does not replace its own process.
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

// restartSource reports a product restart request without naming the process host.
type restartSource interface {
	OnRestart(func())
}

// bindRestart connects product restart intent to Hanami's external restart protocol.
func bindRestart(source restartSource, controller *hanamiprocess.Controller) {
	if source == nil || controller == nil {
		return
	}
	source.OnRestart(func() {
		controller.RequestExternalRestart(errors.New("product restart requested"))
	})
}
