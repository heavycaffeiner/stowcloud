//go:build linux

// Package events translates platform watcher notifications into the small,
// transport-neutral change stream the application assembles into its products.
package events

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/watch"
)

// Event is one share-relative invalidation. It deliberately contains no
// storage or transport types: consumers decide what the opaque share and
// directory identify in their own layer.
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
func Adapt(ctx context.Context, in <-chan watch.InvalEvent, buffer int, callbacks ...Callback) <-chan Event {
	if buffer < 0 {
		buffer = 0
	}
	out := make(chan Event, buffer)
	task.Go(ctx, "event translation", func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-in:
				if !ok {
					return
				}
				ev := Event{Share: uint32(raw.Share), Dir: raw.Dir, All: raw.All}
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
