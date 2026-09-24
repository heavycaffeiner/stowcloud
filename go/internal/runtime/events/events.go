//go:build linux

// Package events translates platform watcher notifications into the small,
// transport-neutral change stream the application assembles into its products.
package events

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	storagewatch "github.com/stowcloud/storage/watch"
)

// Event is one owner-relative invalidation. The product adapter maps the
// public watch owner's opaque ID back to its share identity at the boundary.
type Event struct {
	Share uint32
	Dir   string
	All   bool
}

// Callback observes an event before it is emitted. Adapt invokes callbacks in
// the order supplied, sequentially, so product work can establish the order
// required by its consumers before a notification is published.
type Callback func(context.Context, Event)

// Adapt translates watcher events and runs callbacks before publishing each
// translated event. The returned stream is closed when the source closes or
// ctx is canceled. buffer bounds the pending translated events; a caller can
// use zero for an unbuffered stream.
func Adapt(ctx context.Context, in <-chan storagewatch.Event, buffer int, callbacks ...Callback) <-chan Event {
	if buffer < 0 {
		buffer = 0
	}
	out := make(chan Event, buffer)
	concurrency.Go(ctx, "event translation", func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-in:
				if !ok {
					return
				}
				ev := Event{Dir: raw.Path.String(), All: raw.All}
				if raw.Owner != "" {
					// Product owner IDs are decimal share IDs. Invalid values are
					// deliberately mapped to zero and ignored by cache policy.
					var share uint64
					for _, digit := range raw.Owner {
						if digit < '0' || digit > '9' {
							share = 0
							break
						}
						share = share*10 + uint64(digit-'0')
					}
					ev.Share = uint32(share)
				}
				for _, callback := range callbacks {
					if callback != nil {
						callback(ctx, ev)
					}
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	})
	return out
}
