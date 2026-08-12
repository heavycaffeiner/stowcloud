// Package task is a fixture standing in for the real internal/task: the one
// package whose go statement vetgo has to accept.
package task

func Go(fn func()) {
	go fn()
}
