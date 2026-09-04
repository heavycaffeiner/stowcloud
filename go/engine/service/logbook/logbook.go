// Linux only, for the same reason as the rest of the engine tree.
//go:build linux

// Package logbook keeps the engine's log lines where an operator can read
// them after the fact.
//
// Two things live here and they are deliberately one package: a slog handler
// that captures every line, and the store that handler writes to. Splitting
// them would put a wire format between two halves of one decision, since the
// query has to know exactly how the writer laid the records down.
//
// The store is a series of gzip-compressed JSON-lines segments. A record is
// one line, a segment is a bounded run of them, and retention drops whole
// segments. Nothing indexes anything: a filter scans, newest segment first,
// and stops as soon as the page is full. That is affordable because a page is
// small and the newest segment is the one almost every query wants, and it is
// the whole reason there is no second copy of the data to keep consistent.
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
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// The bounds a deployment gets when it names none.
const (
	defaultSegmentSize int64 = 8 << 20
	defaultTotalSize   int64 = 256 << 20

	// A page the caller did not size, and the most it may ask for. Stated
	// here as well as at the route because the store is what has to survive
	// an unbounded ask.
	defaultLimit = 100
	maxLimit     = 500

	// Attribute names that become their own field. Both are filtered on, and
	// a filter over the attribute map would have to scan every pair of every
	// record to answer.
	attrSubsystem = "subsystem"
	attrRequestID = "request_id"
)

// Options configures the store.
type Options struct {
	// Dir is the log directory, normally <dataDir>/logs.
	Dir string

	// MaxSegmentSize is the compressed bytes one segment holds before the
	// store rotates to a new one. Zero means the default.
	MaxSegmentSize int64

	// MaxTotalSize is the retention budget across every segment. Zero means
	// the default. The newest segment is never dropped, whatever the budget,
	// because a store that discarded what it just wrote would report an
	// empty log on a busy server.
	MaxTotalSize int64

	// Clock stamps the records. Zero means the system clock.
	Clock clock.Clock
}

// Sink is the store. It is safe for concurrent use.
type Sink struct {
	dir      string
	segMax   int64
	totalMax int64
	clk      clock.Clock

	// mu guards the active segment and nothing else. A query holds it only
	// long enough to snapshot the directory listing, so a scan never blocks
	// a log line.
	mu      sync.Mutex
	active  *segment
	dropped uint64
}

// segment is the file being appended to.
type segment struct {
	name    string
	file    *os.File
	gz      *gzip.Writer
	written int64
	lines   int
}

// Record is one log line as the store holds it.
type Record struct {
	TSNs      int64
	Level     string
	Msg       string
	Subsystem string
	RequestID string
	Attrs     map[string]string
}

// wireRecord is the on-disk shape. Short keys: the field names repeat on
// every line of every segment, and this is the one place they are read.
type wireRecord struct {
	TSNs      int64             `json:"t"`
	Level     string            `json:"l"`
	Msg       string            `json:"m"`
	Subsystem string            `json:"s,omitempty"`
	RequestID string            `json:"r,omitempty"`
	Attrs     map[string]string `json:"a,omitempty"`
}

// Query selects records. A zero field is not a filter.
type Query struct {
	Since, Until int64
	Levels       []string
	Text         string
	Subsystem    string
	RequestID    string
	Limit        int
	Cursor       string
}

// Page is one answer, newest record first.
type Page struct {
	Records []Record
	Cursor  string
}

// Stats is what the store is holding.
type Stats struct {
	StoredBytes int64
	Segments    int
}

// Open prepares the directory and the first segment.
func Open(opt Options) (*Sink, error) {
	if opt.Dir == "" {
		return nil, errors.New("the log store needs a directory")
	}
	if err := os.MkdirAll(opt.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("preparing the log directory: %w", err)
	}

	s := &Sink{
		dir:      opt.Dir,
		segMax:   opt.MaxSegmentSize,
		totalMax: opt.MaxTotalSize,
		clk:      opt.Clock,
	}
	if s.segMax <= 0 {
		s.segMax = defaultSegmentSize
	}
	if s.totalMax <= 0 {
		s.totalMax = defaultTotalSize
	}
	if s.clk == nil {
		s.clk = clock.System()
	}

	// Opened eagerly, so a directory that cannot be written is a boot-time
	// warning rather than a surprise on the first log line.
	if err := s.rotate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close finishes the active segment. A segment left unclosed still reads up
// to its last flush, so this recovers the tail rather than the file.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeActive()
}

func (s *Sink) closeActive() error {
	if s.active == nil {
		return nil
	}
	seg := s.active
	s.active = nil
	return errors.Join(seg.gz.Close(), seg.file.Close())
}

// rotate closes the active segment and starts a new one, then applies the
// retention budget. The caller holds mu.
func (s *Sink) rotate() error {
	if err := s.closeActive(); err != nil {
		return fmt.Errorf("closing a log segment: %w", err)
	}

	// The name is the segment's first nanosecond, zero padded so a lexical
	// sort is a chronological one. Nanosecond collisions are broken by the
	// open failing and the next reading being taken.
	var (
		name string
		file *os.File
	)
	for attempt := 0; ; attempt++ {
		name = segmentName(s.clk.Nanos(), attempt)
		//nolint:gosec // G304: the name is this store's own, from segmentName.
		f, err := os.OpenFile(filepath.Join(s.dir, name),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			file = f
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("opening a log segment: %w", err)
		}
		if attempt > 64 {
			return fmt.Errorf("opening a log segment: %w", err)
		}
	}

	s.active = &segment{name: name, file: file, gz: gzip.NewWriter(file)}
	s.enforceBudget()
	return nil
}

// segmentName is the file name for a segment starting at this instant.
//
// The suffix disambiguates two segments that started in the same nanosecond,
// which a fake clock in a test does routinely and a real one does when a
// rotation lands on a coarse clock tick.
func segmentName(ns int64, attempt int) string {
	if ns < 0 {
		ns = 0
	}
	return fmt.Sprintf("log-%019d-%02d.jsonl.gz", ns, attempt)
}

// enforceBudget drops the oldest segments until the total fits. The caller
// holds mu.
func (s *Sink) enforceBudget() {
	names, sizes, total, err := s.listLocked()
	if err != nil {
		return
	}
	for i := 0; i < len(names)-1 && total > s.totalMax; i++ {
		if rerr := os.Remove(filepath.Join(s.dir, names[i])); rerr != nil {
			return
		}
		total -= sizes[i]
	}
}

// listLocked reads the segment names in chronological order with their sizes.
func (s *Sink) listLocked() (names []string, sizes []int64, total int64, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("reading the log directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isSegment(entry.Name()) {
			continue
		}
		info, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		names = append(names, entry.Name())
		sizes = append(sizes, info.Size())
		total += info.Size()
	}
	sort.Sort(&segmentOrder{names: names, sizes: sizes})
	return names, sizes, total, nil
}

// segmentOrder sorts the two parallel slices together.
type segmentOrder struct {
	names []string
	sizes []int64
}

func (o *segmentOrder) Len() int           { return len(o.names) }
func (o *segmentOrder) Less(i, j int) bool { return o.names[i] < o.names[j] }
func (o *segmentOrder) Swap(i, j int) {
	o.names[i], o.names[j] = o.names[j], o.names[i]
	o.sizes[i], o.sizes[j] = o.sizes[j], o.sizes[i]
}

func isSegment(name string) bool {
	return strings.HasPrefix(name, "log-") && strings.HasSuffix(name, ".jsonl.gz")
}

// Stats reports what is on disk.
func (s *Sink) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, _, total, err := s.listLocked()
	if err != nil {
		return Stats{}
	}
	return Stats{StoredBytes: total, Segments: len(names)}
}

// write appends one record.
//
// A failure is counted and dropped rather than returned: the caller is a log
// call somewhere in the engine, and a request that fails because its own log
// line could not be stored would be a worse outcome than a gap in the log.
func (s *Sink) write(r Record) {
	line, err := json.Marshal(wireRecord(r))
	if err != nil {
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil {
		if rerr := s.rotate(); rerr != nil {
			s.dropped++
			return
		}
	}
	if s.active.written >= s.segMax {
		if rerr := s.rotate(); rerr != nil {
			s.dropped++
			return
		}
	}

	n, werr := s.active.gz.Write(line)
	if werr != nil {
		s.dropped++
		return
	}
	// Flushed per record so a crash costs the last line rather than the
	// whole segment: a gzip stream with no sync point reads as nothing at
	// all, and the log is wanted precisely when the process died.
	if ferr := s.active.gz.Flush(); ferr != nil {
		s.dropped++
		return
	}
	s.active.lines++

	// The compressed size is what the budget is about, so it is measured
	// rather than estimated from the line length.
	if pos, serr := s.active.file.Seek(0, io.SeekCurrent); serr == nil {
		s.active.written = pos
	} else {
		s.active.written += int64(n)
	}
}

// Dropped reports how many records could not be stored. Exported for the
// tests and for a diagnostic; a nonzero value means the log has gaps.
func (s *Sink) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Query scans the store, newest first.
//
// The segment list is snapshotted under the lock and read outside it, so a
// scan never blocks a log line. A segment retired mid-scan reads as absent
// and the walk continues, which is exactly what the cursor's own
// resume-from-older rule does between two pages.
func (s *Sink) Query(ctx context.Context, q Query) (Page, error) {
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}

	s.mu.Lock()
	if s.active != nil {
		// Everything written so far has to be readable, including the line
		// that just landed in the active segment.
		if err := s.active.gz.Flush(); err != nil {
			s.mu.Unlock()
			return Page{}, fmt.Errorf("flushing the active log segment: %w", err)
		}
	}
	names, _, _, err := s.listLocked()
	s.mu.Unlock()
	if err != nil {
		return Page{}, err
	}

	from, fromLine, hasCursor, cerr := parseCursor(q.Cursor)
	if cerr != nil {
		return Page{}, cerr
	}

	want := newMatcher(q)
	out := make([]Record, 0, q.Limit)
	var cursor string

	for i := len(names) - 1; i >= 0 && len(out) < q.Limit; i-- {
		name := names[i]
		// The cursor names where the last page stopped. Anything newer than
		// that segment was already returned.
		if hasCursor && name > from {
			continue
		}
		before := -1
		if hasCursor && name == from {
			before = fromLine
		}

		found, lines, serr := s.scan(ctx, name, want, before, q.Limit-len(out))
		if serr != nil {
			if errors.Is(serr, os.ErrNotExist) {
				continue
			}
			return Page{}, serr
		}
		for j := range found {
			out = append(out, found[j])
			cursor = formatCursor(name, lines[j])
		}
	}

	if len(out) < q.Limit {
		// The walk reached the oldest segment, so there is nothing to resume.
		cursor = ""
	}
	return Page{Records: out, Cursor: cursor}, nil
}

// scan reads one segment and returns its newest matching records.
//
// before bounds the line index exclusively, for resuming inside a segment; -1
// means the whole file. Only the newest `limit` matches are kept, so memory
// is the page size rather than the segment's line count.
func (s *Sink) scan(
	ctx context.Context, name string, want *matcher, before, limit int,
) (out []Record, lines []int, err error) {
	//nolint:gosec // G304: the name came from this store's own directory listing.
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		// A segment whose header is not there yet holds nothing readable.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading log segment %s: %w", name, err)
	}
	// The reader is not closed: its Close reports a truncated or unfinished
	// stream, which is the ordinary state of the segment being appended to,
	// and it holds nothing the file handle above does not already own.

	// A record can carry a long path or a long error string, so the line
	// bound is generous. A line past it is skipped rather than fatal.
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	keptRecords := make([]Record, 0, limit)
	keptLines := make([]int, 0, limit)
	for line := 0; sc.Scan(); line++ {
		if line%1024 == 0 && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if before >= 0 && line >= before {
			break
		}
		var w wireRecord
		if uerr := json.Unmarshal(sc.Bytes(), &w); uerr != nil {
			continue
		}
		r := Record(w)
		if !want.matches(r) {
			continue
		}
		if len(keptRecords) == limit {
			keptRecords = keptRecords[1:]
			keptLines = keptLines[1:]
		}
		keptRecords = append(keptRecords, r)
		keptLines = append(keptLines, line)
	}
	// A truncated tail is the ordinary state of the active segment, and of
	// any segment the process died inside. What was read stands.
	if serr := sc.Err(); serr != nil &&
		!errors.Is(serr, io.ErrUnexpectedEOF) && !errors.Is(serr, gzip.ErrChecksum) &&
		!errors.Is(serr, bufio.ErrTooLong) {
		return nil, nil, fmt.Errorf("reading log segment %s: %w", name, serr)
	}

	// Newest first within the segment.
	reverse(keptRecords)
	reverseInts(keptLines)
	return keptRecords, keptLines, nil
}

func reverse(rs []Record) {
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
}

func reverseInts(xs []int) {
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
}

// formatCursor names the record a page stopped on.
func formatCursor(segment string, line int) string {
	return segment + ":" + strconv.Itoa(line)
}

// parseCursor reads a cursor back. An unparseable one is refused rather than
// ignored: silently restarting from the newest record would loop a pager
// forever.
func parseCursor(raw string) (segment string, line int, ok bool, err error) {
	if raw == "" {
		return "", 0, false, nil
	}
	at := strings.LastIndexByte(raw, ':')
	if at <= 0 {
		return "", 0, false, fmt.Errorf("%w: %q", ErrBadCursor, raw)
	}
	n, cerr := strconv.Atoi(raw[at+1:])
	if cerr != nil || n < 0 {
		return "", 0, false, fmt.Errorf("%w: %q", ErrBadCursor, raw)
	}
	name := raw[:at]
	if !isSegment(name) {
		return "", 0, false, fmt.Errorf("%w: %q", ErrBadCursor, raw)
	}
	return name, n, true, nil
}

// ErrBadCursor reports a cursor this store did not write.
var ErrBadCursor = errors.New("malformed log cursor")

// matcher is the query compiled once, so a scan does not rebuild it per line.
type matcher struct {
	since, until int64
	levels       map[string]struct{}
	text         string
	subsystem    string
	requestID    string
}

func newMatcher(q Query) *matcher {
	m := &matcher{
		since:     q.Since,
		until:     q.Until,
		text:      strings.ToLower(q.Text),
		subsystem: q.Subsystem,
		requestID: q.RequestID,
	}
	if len(q.Levels) > 0 {
		m.levels = make(map[string]struct{}, len(q.Levels))
		for _, l := range q.Levels {
			m.levels[strings.ToUpper(strings.TrimSpace(l))] = struct{}{}
		}
	}
	return m
}

func (m *matcher) matches(r Record) bool {
	switch {
	case m.since != 0 && r.TSNs < m.since:
		return false
	case m.until != 0 && r.TSNs > m.until:
		return false
	case m.subsystem != "" && r.Subsystem != m.subsystem:
		return false
	case m.requestID != "" && r.RequestID != m.requestID:
		return false
	}
	if m.levels != nil {
		if _, ok := m.levels[strings.ToUpper(r.Level)]; !ok {
			return false
		}
	}
	if m.text == "" {
		return true
	}
	// The message first, since that is what a search is usually about, then
	// the attribute values, which is where a path or an error string lands.
	if strings.Contains(strings.ToLower(r.Msg), m.text) {
		return true
	}
	for k, v := range r.Attrs {
		if strings.Contains(strings.ToLower(v), m.text) ||
			strings.Contains(strings.ToLower(k), m.text) {
			return true
		}
	}
	return false
}
