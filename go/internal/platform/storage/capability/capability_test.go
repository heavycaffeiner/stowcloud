package capability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

func TestParsePathValidation(t *testing.T) {
	for _, raw := range []string{"/x", "x/", "x//y", ".", "x/../y", "x\\y", "x\x00y", string([]byte{0xff})} {
		if _, err := ParsePath(raw); err == nil {
			t.Errorf("ParsePath(%q) accepted invalid path", raw)
		}
	}
	p, err := ParsePath("a/b")
	if err != nil || p.String() != "a/b" || p.Name() != "b" || p.Parent().String() != "a" {
		t.Fatalf("valid path result: %q, %v", p, err)
	}
	child, err := p.Join("c")
	if err != nil || !child.Under(p) || child.Components()[2] != "c" {
		t.Fatalf("join result: %q, %v", child, err)
	}
}

type readBackend struct{}

func (readBackend) Stat(context.Context, Path) (Entry, error)      { return Entry{}, nil }
func (readBackend) ReadDir(context.Context, Path) ([]Entry, error) { return nil, nil }
func (readBackend) OpenRead(context.Context, Path) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

type optionalBackend struct{ readBackend }

func (optionalBackend) Rename(context.Context, Path, Path) error { return nil }

func TestCapabilityDiscoveryUsesTypeAssertions(t *testing.T) {
	var base any = readBackend{}
	if _, ok := base.(Renamer); ok {
		t.Fatal("base backend unexpectedly advertises rename")
	}
	var optional any = optionalBackend{}
	if _, ok := optional.(ReadHierarchy); !ok {
		t.Fatal("optional backend lost required read hierarchy")
	}
	if _, ok := optional.(Renamer); !ok {
		t.Fatal("optional backend did not advertise rename")
	}
}

func TestMaterializedReleaseIsIdempotent(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("release failed")
	m := NewMaterialized(bytes.NewReader([]byte("abc")), 3, func() error { calls.Add(1); return wantErr })
	if err := m.Release(); !errors.Is(err, wantErr) {
		t.Fatalf("first Release = %v", err)
	}
	if err := m.Release(); !errors.Is(err, wantErr) || calls.Load() != 1 {
		t.Fatalf("second Release = %v, calls=%d", err, calls.Load())
	}
}

func ExampleReadHierarchy() {
	var backend any = readBackend{}
	if hierarchy, ok := backend.(ReadHierarchy); ok {
		if _, err := hierarchy.Stat(context.Background(), RootPath()); err != nil {
			panic(err)
		}
	}
	// Output:
}
