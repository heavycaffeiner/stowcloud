//go:build linux

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

func TestFreeSpaceReportsTheFilesystemHoldingThePath(t *testing.T) {
	c, _, _, root := writable(t)

	fs, err := c.FreeSpace(context.Background(), root)
	if err != nil {
		t.Fatalf("FreeSpace: %v", err)
	}
	if fs.Total == 0 {
		t.Fatal("the filesystem reports a total of zero")
	}
	// Available is f_bavail and Used includes the root-reserved blocks, so
	// the two plus the reserve are the total; available alone can never
	// exceed what is unused.
	if fs.Available > fs.Total-fs.Used {
		t.Fatalf("available %d exceeds the unused %d", fs.Available, fs.Total-fs.Used)
	}
	if fs.Used > fs.Total {
		t.Fatalf("used %d exceeds the total %d", fs.Used, fs.Total)
	}
}

func TestFreeSpaceRefusesWithoutRead(t *testing.T) {
	c, _, _, root := writable(t)
	stranger := Resolved{
		user: root.user, share: root.share, root: root.root,
		path: root.path, perms: acl.Download,
	}
	if _, err := c.FreeSpace(context.Background(), stranger); !errors.Is(err, ErrDenied) {
		t.Fatalf("FreeSpace without Read = %v, want ErrDenied", err)
	}
}

func TestAttachQuotaSinkIsOneShot(t *testing.T) {
	c, _ := newCore(t)
	if err := c.AttachQuotaSink(&countingSink{}); err != nil {
		t.Fatalf("attaching the first sink: %v", err)
	}
	if err := c.AttachQuotaSink(&countingSink{}); err == nil {
		t.Fatal("a second sink was accepted; a replaced ledger orphans its reservations")
	}
}

func TestChargeQuotaSplitsBySign(t *testing.T) {
	c, _ := newCore(t)
	sink := &countingSink{}
	if err := c.AttachQuotaSink(sink); err != nil {
		t.Fatalf("attaching: %v", err)
	}
	ctx := context.Background()

	// A negative delta is bytes freed and credits through Release with the
	// magnitude. This is the fix: the reference passed the negative delta
	// straight through and Release refused it as a caller bug, so no delete
	// ever reached the ledger.
	c.chargeQuota(ctx, 1, -400)
	if len(sink.released) != 1 || sink.released[0] != 400 {
		t.Fatalf("a negative delta released %v, want the magnitude 400", sink.released)
	}
	if len(sink.reserved) != 0 {
		t.Fatalf("a negative delta booked %v", sink.reserved)
	}

	c.chargeQuota(ctx, 1, 250)
	if len(sink.reserved) != 1 || sink.reserved[0] != 250 {
		t.Fatalf("a positive delta booked %v, want 250", sink.reserved)
	}

	c.chargeQuota(ctx, 1, 0)
	if len(sink.reserved) != 1 || len(sink.released) != 1 {
		t.Fatalf("a zero delta touched the ledger: %v %v", sink.reserved, sink.released)
	}
}

func TestChargeQuotaSwallowsEveryFailure(t *testing.T) {
	c, _ := newCore(t)
	sink := &countingSink{fail: errors.New("the ledger is down")}
	if err := c.AttachQuotaSink(sink); err != nil {
		t.Fatalf("attaching: %v", err)
	}
	// Nothing propagates: the filesystem change is already durable, and
	// failing the request over its bookkeeping would report an operation as
	// failed that in fact happened.
	c.chargeQuota(context.Background(), 1, 100)
	c.chargeQuota(context.Background(), 1, -100)

	// A refusal is not an error either; it undercounts the account, which
	// never blocks a later write on drift it did not cause.
	refusing := &countingSink{refuse: true}
	other, _ := newCore(t)
	if err := other.AttachQuotaSink(refusing); err != nil {
		t.Fatalf("attaching the refusing sink: %v", err)
	}
	other.chargeQuota(context.Background(), 1, 100)
}

func TestACoreWithNoSinkChargesNothing(t *testing.T) {
	c, _, hostDir, _ := writable(t)
	if c.quota != nil {
		t.Fatal("the fixture core already carries a sink")
	}
	// A write and a delete both run without a ledger, which is the
	// quota-less deployment rather than a degraded one.
	mustCreate(t, c, under(t, c, "Documents/free.txt", acl.Write), "bytes")
	if err := c.Delete(context.Background(), under(t, c, "Documents/free.txt", acl.Delete), true); err != nil {
		t.Fatalf("deleting with no sink: %v", err)
	}
	if got := trashNames(t, hostDir); len(got) != 0 {
		t.Fatalf("the delete left %v", got)
	}
}

func TestMagnitudeClampsTheMostNegativeValue(t *testing.T) {
	cases := []struct {
		in, want int64
	}{
		{-10, 10},
		{-1<<63 + 1, 1<<63 - 1},
		// Negating the most negative int64 wraps back to itself, which
		// would hand Release a negative it refuses.
		{-1 << 63, 1<<63 - 1},
	}
	for _, tc := range cases {
		if got := magnitude(tc.in); got != tc.want {
			t.Fatalf("magnitude(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
