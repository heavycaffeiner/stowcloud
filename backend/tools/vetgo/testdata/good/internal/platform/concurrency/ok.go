// Package concurrency is a fixture standing in for the real internal/platform/concurrency,
// the package whose go statement vetgo has to accept.
package concurrency

func Go(fn func()) {
	go fn()
}
