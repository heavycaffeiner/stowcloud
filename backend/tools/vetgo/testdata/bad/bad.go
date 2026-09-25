// Package bad is a fixture. Every go statement in it is deliberate and vetgo
// has to report all of them.
package bad

import "sync"

func spawn() {
	go func() {}()
}

func spawnWithWait() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { wg.Done() }()
	wg.Wait()
}
