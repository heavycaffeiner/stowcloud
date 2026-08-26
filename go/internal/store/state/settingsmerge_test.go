package state_test

import (
	"context"
	"testing"
)

// A save is a patch naming the fields it changes.
//
// The section used to be replaced wholesale, so a caller that mentioned one
// field erased every other field beside it. The way that showed up: an
// operator seeded a bind address, the first-run form saved an app-host list
// into the same section, and the server came back on a port nobody had asked
// for with nothing in the log about it.
func TestSavingOneFieldKeepsTheOthersInTheSection(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	if err := d.MergeSettings(ctx, "network", map[string]any{
		"bind":            "127.0.0.1:9000",
		"trusted_proxies": []any{"10.0.0.0/8"},
	}); err != nil {
		t.Fatalf("seeding the section: %v", err)
	}
	// A later save that talks about one field only.
	if err := d.MergeSettings(ctx, "network", map[string]any{
		"app_hosts": []any{"nas.example.test"},
	}); err != nil {
		t.Fatalf("patching the section: %v", err)
	}

	doc, err := d.Settings(ctx)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	net, ok := doc["network"].(map[string]any)
	if !ok {
		t.Fatalf("the network section is %T", doc["network"])
	}
	if net["bind"] != "127.0.0.1:9000" {
		t.Errorf("bind = %v, want the seeded address to survive", net["bind"])
	}
	if net["trusted_proxies"] == nil {
		t.Error("the proxy ranges were dropped by a save that did not mention them")
	}
	if net["app_hosts"] == nil {
		t.Error("the field the save was about did not land")
	}
}

// Clearing is explicit, which is what makes the merge above safe: a caller
// that means "set this to nothing" says so, and it is not the same request as
// one that says nothing about the field.
func TestAnExplicitEmptyValueClearsTheField(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	if err := d.MergeSettings(ctx, "network", map[string]any{
		"trusted_proxies": []any{"10.0.0.0/8"},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := d.MergeSettings(ctx, "network", map[string]any{
		"trusted_proxies": []any{},
	}); err != nil {
		t.Fatalf("clearing: %v", err)
	}

	doc, err := d.Settings(ctx)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	net, ok := doc["network"].(map[string]any)
	if !ok {
		t.Fatalf("the network section is %T, want a document", doc["network"])
	}
	list, ok := net["trusted_proxies"].([]any)
	if !ok || len(list) != 0 {
		t.Errorf("trusted_proxies = %v, want an empty list", net["trusted_proxies"])
	}
}
