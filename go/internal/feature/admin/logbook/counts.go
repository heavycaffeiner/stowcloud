// Linux only, for the same reason as the rest of the engine tree.
//go:build linux

package logbook

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The counting walk, for the graph above the log list.
//
// Counts rather than records, and a separate walk rather than paging the list
// and adding up what came back: a page is a hundred lines and a graph covers
// a window. A chart drawn from the loaded page would rise and fall with what
// the reader happened to scroll to, which is a chart that lies.

// The bounds a counting walk answers within.
const (
	// maxBuckets is how many bars the answer may carry. A chart wider than
	// this is a chart nobody can read, and the walk is what pays for each.
	maxBuckets = 240

	// countCeiling stops the walk. A window covering a busy month would
	// otherwise decompress the whole store to draw one screen.
	countCeiling = 200_000
)

// Bucket is one interval's counts, keyed by level.
type Bucket struct {
	StartNs int64
	// Levels holds only the levels this interval saw. An absent key is zero,
	// which the renderer already has to handle for a series a window has
	// none of.
	Levels map[string]int
}

// Counts walks the filtered records and totals them per interval.
//
// The window is the query's own Since and Until. bucketNs is the interval
// width; a non-positive one, or one that would need more buckets than the
// ceiling, is replaced by a width the store picks. Truncated reports that the
// walk stopped early, so a caller can say the graph is partial rather than
// drawing a window it did not finish reading.
//
// Buckets come back oldest first and cover the window with no holes: an
// interval with no records is still an interval, and a renderer that had to
// tell a gap from a zero would guess.
func (s *Sink) Counts(
	ctx context.Context, q Query, bucketNs int64,
) (buckets []Bucket, widthNs int64, truncated bool, err error) {
	s.mu.Lock()
	if s.active != nil {
		// The line that just landed counts too, for the same reason Query
		// flushes before it reads.
		if ferr := s.active.gz.Flush(); ferr != nil {
			s.mu.Unlock()
			return nil, 0, false, fmt.Errorf("flushing the active log segment: %w", ferr)
		}
	}
	names, _, _, lerr := s.listLocked()
	s.mu.Unlock()
	if lerr != nil {
		return nil, 0, false, lerr
	}

	since, until := q.Since, q.Until
	if until <= 0 {
		// The clock, not the newest record. A window anchored to the last
		// thing stored would call a week-old event "the last day" on an idle
		// server, so the axis would stop meaning what it says.
		until = s.clk.Nanos()
	}
	if since <= 0 || since >= until {
		// An open-ended window is the last day, which is the window a
		// dashboard opens on.
		since = until - int64(24*60*60)*1e9
	}
	widthNs = bucketWidth(since, until, bucketNs)

	// The frame is laid out before anything is read, so every interval exists
	// whether or not a record lands in it.
	start := (since / widthNs) * widthNs
	count := int((until-start)/widthNs) + 1
	if count > maxBuckets {
		count = maxBuckets
	}
	buckets = make([]Bucket, count)
	for i := range buckets {
		buckets[i] = Bucket{StartNs: start + int64(i)*widthNs, Levels: map[string]int{}}
	}

	// The matcher's own bounds are widened to the frame, so a record just
	// outside the caller's window does not land in a bucket that exists.
	scoped := q
	scoped.Since, scoped.Until = since, until
	want := newMatcher(scoped)

	seen := 0
	for i := len(names) - 1; i >= 0; i-- {
		if seen >= countCeiling {
			truncated = true
			break
		}
		n, hit, cerr := s.countSegment(ctx, names[i], want, buckets, start, widthNs, countCeiling-seen)
		if cerr != nil {
			if errors.Is(cerr, os.ErrNotExist) {
				continue
			}
			return nil, 0, false, cerr
		}
		seen += n
		if hit {
			truncated = true
			break
		}
	}
	return buckets, widthNs, truncated, nil
}

// bucketWidth picks the interval width for a window.
//
// A caller's own width is honoured when it fits the bucket ceiling. Otherwise
// the window is divided into about half the ceiling and rounded up to a width
// a person reads without converting: nobody wants a bar 75 seconds wide.
func bucketWidth(since, until, asked int64) int64 {
	span := until - since
	if span <= 0 {
		span = 1
	}
	if asked > 0 && span/asked <= maxBuckets {
		return asked
	}

	const (
		second = int64(1e9)
		minute = 60 * second
		hour   = 60 * minute
		day    = 24 * hour
	)
	ladder := []int64{
		second, 5 * second, 15 * second, 30 * second,
		minute, 5 * minute, 15 * minute, 30 * minute,
		hour, 3 * hour, 6 * hour, 12 * hour,
		day, 7 * day, 30 * day,
	}
	target := span / (maxBuckets / 2)
	for _, w := range ladder {
		if w >= target {
			return w
		}
	}
	return ladder[len(ladder)-1]
}

// countSegment adds one segment's matching records to the frame.
//
// It reports how many records it counted and whether it stopped on the
// remaining ceiling, so the caller can stop walking older segments.
func (s *Sink) countSegment(
	ctx context.Context, name string, want *matcher,
	buckets []Bucket, start, widthNs int64, remaining int,
) (counted int, hitCeiling bool, err error) {
	//nolint:gosec // G304: the name came from this store's own directory listing.
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return 0, false, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	gz, gerr := gzip.NewReader(f)
	if gerr != nil {
		if errors.Is(gerr, io.EOF) || errors.Is(gerr, io.ErrUnexpectedEOF) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("reading log segment %s: %w", name, gerr)
	}
	// The reader is not closed, for the reason the paging walk does not close
	// its own: Close reports a truncated stream, which is the ordinary state
	// of the segment being appended to.

	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	last := len(buckets) - 1
	for line := 0; sc.Scan(); line++ {
		if line%1024 == 0 && ctx.Err() != nil {
			return counted, false, ctx.Err()
		}
		if counted >= remaining {
			return counted, true, nil
		}
		var w wireRecord
		if uerr := json.Unmarshal(sc.Bytes(), &w); uerr != nil {
			continue
		}
		r := Record(w)
		if !want.matches(r) {
			continue
		}
		counted++

		at := int((r.TSNs - start) / widthNs)
		if at < 0 || at > last {
			// Inside the matcher's window but outside the frame, which the
			// bucket ceiling can produce. Counted, not placed.
			continue
		}
		buckets[at].Levels[strings.ToUpper(r.Level)]++
	}
	if serr := sc.Err(); serr != nil &&
		!errors.Is(serr, io.ErrUnexpectedEOF) && !errors.Is(serr, gzip.ErrChecksum) &&
		!errors.Is(serr, bufio.ErrTooLong) {
		return counted, false, fmt.Errorf("reading log segment %s: %w", name, serr)
	}
	return counted, false, nil
}
