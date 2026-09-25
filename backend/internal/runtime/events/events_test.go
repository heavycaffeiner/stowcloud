//go:build linux

package events

import (
	"context"
	"reflect"
	"testing"

	"github.com/stowcloud/storage"
	storagewatch "github.com/stowcloud/storage/watch"
)

func TestAdaptRunsCallbacksInOrderBeforeEmission(t *testing.T) {
	in := make(chan storagewatch.Event, 1)
	in <- storagewatch.Event{Owner: "7", Path: mustPath(t, "docs")}
	close(in)

	var order []string
	out := Adapt(context.Background(), in, 1,
		func(_ context.Context, ev Event) {
			order = append(order, "cache:"+ev.Dir)
		},
		func(_ context.Context, ev Event) {
			order = append(order, "search:"+ev.Dir)
		},
	)

	got, ok := <-out
	if !ok {
		t.Fatal("adapter closed without emitting the input")
	}
	want := Event{Share: 7, Dir: "docs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(order, []string{"cache:docs", "search:docs"}) {
		t.Fatalf("callback order %v", order)
	}
	if _, ok := <-out; ok {
		t.Fatal("adapter output remained open after input closed")
	}
}

func TestAdaptStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan storagewatch.Event)
	out := Adapt(ctx, in, 0)
	cancel()
	if _, ok := <-out; ok {
		t.Fatal("adapter emitted after cancellation")
	}
}

func mustPath(t *testing.T, raw string) storage.Path {
	t.Helper()
	p, err := storage.ParsePath(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
