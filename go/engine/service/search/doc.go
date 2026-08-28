// Package search implements the tiered filename search: the shared
// vocabulary, the parallel walk that is the base tier, and the corpus
// estimator that sizes an index before one is built.
//
// Search works with no index at all. The walk is the base tier; the trigram
// index under index/ is an escalation, and a cache whose directory can be
// deleted with no data loss. The service under svc/ chooses between them.
package search
