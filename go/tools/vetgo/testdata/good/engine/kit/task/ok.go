// Package task is a fixture standing in for the rebuilt engine/kit/task, the
// second package whose go statement vetgo has to accept.
package task

func Go(fn func()) {
	go fn()
}
