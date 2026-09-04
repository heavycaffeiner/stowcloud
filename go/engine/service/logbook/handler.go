// Linux only, for the same reason as the rest of the engine tree.
//go:build linux

package logbook

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Handler returns a slog handler that stores every record it is given from
// this level up.
//
// The level is the caller's, and the engine passes one at or below Debug: the
// console shows what an operator watches live, and the store holds what the
// dashboard filters afterwards. A line the console was configured to drop
// must not also be the line the store never had.
func (s *Sink) Handler(level slog.Leveler) slog.Handler {
	if level == nil {
		level = slog.LevelDebug
	}
	return &sinkHandler{sink: s, level: level}
}

// sinkHandler adapts slog to the store.
type sinkHandler struct {
	sink  *Sink
	level slog.Leveler

	// The attributes and group prefix accumulated by With and WithGroup.
	// Held flattened, because that is the shape a record is stored in and
	// re-deriving it per line would repeat the work on every call.
	attrs  map[string]string
	prefix string
}

func (h *sinkHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *sinkHandler) Handle(_ context.Context, r slog.Record) error {
	// The store's own clock, not the record's. Both are the system clock in a
	// deployment, so nothing moves; what changes is that one store has one
	// source of time. Rotation and the counting window already read this
	// clock, and a record stamped from somewhere else can sort against the
	// segment it lands in and the window that selects it inconsistently: a
	// window ending a moment before a record that was already written drops
	// it, and the graph loses a bar the list still shows.
	rec := Record{
		TSNs:  h.sink.clk.Nanos(),
		Level: r.Level.String(),
		Msg:   r.Message,
	}

	// The handler's own attributes first, so a record's attribute of the
	// same name wins: the nearer one is the more specific.
	attrs := make(map[string]string, len(h.attrs)+r.NumAttrs())
	for k, v := range h.attrs {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		flatten(attrs, h.prefix, a)
		return true
	})

	// The two promoted names, taken out of the map so a filter on them is a
	// field comparison rather than a scan of every pair.
	if v, ok := attrs[attrSubsystem]; ok {
		rec.Subsystem = v
		delete(attrs, attrSubsystem)
	}
	if v, ok := attrs[attrRequestID]; ok {
		rec.RequestID = v
		delete(attrs, attrRequestID)
	}
	if len(attrs) > 0 {
		rec.Attrs = attrs
	}

	h.sink.write(rec)
	return nil
}

func (h *sinkHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	next := &sinkHandler{
		sink:   h.sink,
		level:  h.level,
		prefix: h.prefix,
		attrs:  make(map[string]string, len(h.attrs)+len(as)),
	}
	for k, v := range h.attrs {
		next.attrs[k] = v
	}
	for _, a := range as {
		flatten(next.attrs, h.prefix, a)
	}
	return next
}

func (h *sinkHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := &sinkHandler{
		sink:   h.sink,
		level:  h.level,
		attrs:  h.attrs,
		prefix: h.prefix + name + ".",
	}
	return next
}

// flatten writes one attribute, and every attribute inside a group, into the
// map under its dotted name.
//
// A group is flattened rather than nested because the store's record carries
// a flat map: the dashboard filters on a value, and a nested shape would make
// "does any value contain this text" a tree walk at every line.
func flatten(into map[string]string, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Key
		inner := prefix
		if group != "" {
			inner = prefix + group + "."
		}
		for _, sub := range a.Value.Group() {
			flatten(into, inner, sub)
		}
		return
	}
	if a.Key == "" {
		return
	}
	into[prefix+a.Key] = a.Value.String()
}

// Fanout hands every record to each handler.
//
// The console and the store are two destinations for one line, and neither is
// a filter on the other. Enabled reports true when any leg wants the level,
// so the store's lower threshold does not silence the console and the
// console's higher one does not empty the store.
func Fanout(handlers ...slog.Handler) slog.Handler {
	kept := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			kept = append(kept, h)
		}
	}
	return &fanout{legs: kept}
}

type fanout struct {
	legs []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.legs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

// Handle delivers to every leg that wants the record.
//
// A failing leg does not stop the others: the console and the store fail for
// unrelated reasons, and losing both because one of them broke is the outcome
// this exists to avoid. The errors join, so a caller that checks still sees
// what happened.
func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.legs {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Each leg gets its own clone: a handler may consume the record's
		// attributes, and slog.Record is not safe to hand to two of them.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs and WithGroup fan out too. A logger built with .With() that kept
// its attributes on one leg only would write two different lines for one
// call, which is the bug this shape exists to prevent.
func (f *fanout) WithAttrs(as []slog.Attr) slog.Handler {
	next := &fanout{legs: make([]slog.Handler, len(f.legs))}
	for i, h := range f.legs {
		next.legs[i] = h.WithAttrs(as)
	}
	return next
}

func (f *fanout) WithGroup(name string) slog.Handler {
	if strings.TrimSpace(name) == "" {
		return f
	}
	next := &fanout{legs: make([]slog.Handler, len(f.legs))}
	for i, h := range f.legs {
		next.legs[i] = h.WithGroup(name)
	}
	return next
}
