//go:build linux

// Package events translates platform watcher notifications into the small,
// transport-neutral change stream the application assembles into its products.
package events

import (
	"context"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
	storage "github.com/stowcloud/storage"
	storagewatch "github.com/stowcloud/storage/watch"
)

const eventQueue = 1024

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

// Share identifies a local filesystem tree that should be watched.
type Share struct {
	ID     uint32
	Host   string
	Broken bool
}

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

// Config controls the platform watcher and the product callback that consumes
// its normalized stream.
type Config struct {
	Backend        storagewatch.Backend
	HotSetMax      int
	FullThreshold  int
	Clock          clock.Clock
	OnCoverageLost func()
	OnEvent        Callback
}

// Manager owns one watcher and its normalized event stream. It contains no
// transport or product permissions, so the application can compose it with
// any broker that understands Event.
type Manager struct {
	watcher *storagewatch.Watcher
	events  <-chan Event
}

// Start creates the watcher, starts event normalization, and registers the
// supplied local shares. A kernel watcher failure is returned to the caller;
// callers may deliberately degrade to polling.
func Start(ctx context.Context, cfg Config, shares []Share) (*Manager, error) {
	in := make(chan storagewatch.Event, eventQueue)
	watcher, err := storagewatch.Start(ctx, storagewatch.Config{
		Backend:        cfg.Backend,
		HotSetMax:      cfg.HotSetMax,
		FullThreshold:  cfg.FullThreshold,
		OnCoverageLost: cfg.OnCoverageLost,
	}, cfg.Clock, in)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		watcher: watcher,
		events:  Adapt(ctx, in, 0, cfg.OnEvent),
	}
	for _, share := range shares {
		m.WatchShare(share)
	}
	return m, nil
}

// Events returns the normalized stream. It closes when the source watcher
// closes or its context is canceled.
func (m *Manager) Events() <-chan Event {
	if m == nil {
		return nil
	}
	return m.events
}

// SetBounds applies new watcher limits after a settings save.
func (m *Manager) SetBounds(hotSet, fullThreshold int) {
	if m != nil && m.watcher != nil {
		m.watcher.SetBounds(hotSet, fullThreshold)
	}
}

// WatchShare registers a local, healthy share. Non-local and broken shares
// have no host tree for this process to watch and are intentionally skipped.
func (m *Manager) WatchShare(share Share) {
	if m == nil || m.watcher == nil || share.Broken || share.Host == "" {
		return
	}
	m.watcher.AddOwner(ownerID(share.ID), share.Host, false)
}

// UnwatchShare removes a share's owner registration.
func (m *Manager) UnwatchShare(id uint32) {
	if m == nil || m.watcher == nil {
		return
	}
	m.watcher.RemoveOwner(ownerID(id))
}

// Subscribe pins a directory in the watcher's sticky set. It returns false
// when the path is not a valid storage path or the manager is unavailable.
func (m *Manager) Subscribe(share uint32, rawPath string) bool {
	if m == nil || m.watcher == nil {
		return false
	}
	path, err := storage.ParsePath(rawPath)
	if err != nil {
		return false
	}
	m.watcher.Subscribe(ownerID(share), path)
	return true
}

// Unsubscribe releases one directory pin.
func (m *Manager) Unsubscribe(share uint32, rawPath string) bool {
	if m == nil || m.watcher == nil {
		return false
	}
	path, err := storage.ParsePath(rawPath)
	if err != nil {
		return false
	}
	m.watcher.Unsubscribe(ownerID(share), path)
	return true
}

// Close stops the watcher. The event stream drains according to its context;
// callers close their transport broker before invoking this method so late
// unsubscribe calls cannot reach a closed watcher.
func (m *Manager) Close() error {
	if m == nil || m.watcher == nil {
		return nil
	}
	err := m.watcher.Close()
	m.watcher = nil
	return err
}

// Pinned reports the number of sticky directory subscriptions.
func (m *Manager) Pinned() int {
	if m == nil || m.watcher == nil {
		return 0
	}
	return m.watcher.Stats().Pinned
}

func ownerID(id uint32) storagewatch.OwnerID {
	return storagewatch.OwnerID(strconv.FormatUint(uint64(id), 10))
}
