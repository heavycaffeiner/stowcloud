//go:build linux

// Package restart translates product restart intent into Hanami's external
// process restart request. The product does not replace its own process.
package restart

import (
	"errors"

	hanamiprocess "github.com/heavycaffeiner/hanami/process"
)

// Source reports a product restart request without naming the process host.
type Source interface {
	OnRestart(func())
}

// Bind connects product restart intent to Hanami's external restart protocol.
func Bind(source Source, controller *hanamiprocess.Controller) {
	if source == nil || controller == nil {
		return
	}
	source.OnRestart(func() {
		controller.RequestExternalRestart(errors.New("product restart requested"))
	})
}
