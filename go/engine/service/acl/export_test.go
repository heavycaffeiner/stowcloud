package acl

// cacheProbe reports whether the cache would serve this question at the
// evaluator's current generation. A test uses it to tell a reused decision
// from a recomputed one without a counter in production code.
func (e *Evaluator) cacheProbe(user int64, v Vpath, want Perms) (bool, Decision) {
	key := cacheKey{user: user, share: v.Share, path: v.Path.String(), want: want}
	return e.cache.lookup(key, e.genValue())
}

// cacheSize is the number of entries currently held.
func (e *Evaluator) cacheSize() int {
	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()
	return len(e.cache.data)
}

// generation is the evaluator's current generation counter.
func (e *Evaluator) generation() int64 { return e.genValue() }
