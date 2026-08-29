// Linux only, for the same reason as the rest of this package.
//go:build linux

// The batch runner: one result per input, in input order.
package handler

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
)

// BatchResult is one input's outcome.
//
// Error is nil on success. A failed item carries the same wire shape a single
// request would have produced, so a batch cannot become the surface that
// reveals what a single request hid.
type BatchResult struct {
	Index int          `json:"index"`
	OK    bool         `json:"ok"`
	Error *apierr.Wire `json:"error,omitempty"`
	Value any          `json:"value,omitempty"`
}

// RunBatch executes op for each item and returns one result per input.
//
// Not parallel, deliberately. Preserving input order and avoiding several
// simultaneous destructive operations on one directory is worth more than the
// throughput of a single request.
//
// Every item runs. One item's malformed input cannot shift another item's
// result, because each result carries the index it came from rather than its
// position in a filtered list.
func RunBatch[In any, Out any](
	ctx context.Context,
	items []In,
	visibility apierr.Visibility,
	op func(context.Context, In) (Out, error),
) []BatchResult {
	out := make([]BatchResult, 0, len(items))
	for i, item := range items {
		// A cancelled context stops the batch, and the rest report the
		// cancellation rather than being silently absent: a caller comparing
		// lengths must not read a short list as "the rest succeeded".
		if err := ctx.Err(); err != nil {
			for j := i; j < len(items); j++ {
				w := apierr.WireOf(err, visibility)
				out = append(out, BatchResult{Index: j, Error: &w})
			}
			return out
		}

		value, err := op(ctx, item)
		if err != nil {
			w := apierr.WireOf(err, visibility)
			out = append(out, BatchResult{Index: i, Error: &w})
			continue
		}
		out = append(out, BatchResult{Index: i, OK: true, Value: value})
	}
	return out
}
