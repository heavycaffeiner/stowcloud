package auth_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// ringFile renders a key ring file holding the given versions, so a test can
// put the two halves of an interrupted rotation on disk without waiting for
// one to be interrupted.
func ringFile(t *testing.T, keys map[uint32][32]byte, order []uint32) []byte {
	t.Helper()
	count, err := num.Narrow[uint16](len(order))
	if err != nil {
		t.Fatalf("a fixture ring of %d keys: %v", len(order), err)
	}
	out := append([]byte(nil), "SCMKEYRNG1\n"...)
	out = binary.BigEndian.AppendUint16(out, count)
	for _, ver := range order {
		out = binary.BigEndian.AppendUint32(out, ver)
		k := keys[ver]
		out = append(out, k[:]...)
	}
	return out
}

// An interrupted rotation leaves the ring and the database disagreeing in one
// of exactly two ways, and startup recovers both: the database naming the
// older version means the re-seal never committed, and naming the newer one
// means the compaction never ran.
func TestStartupAlignsARingLeftBehindByAnInterruptedRotation(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		dbVer    uint32
		wantOnly uint32
	}{
		{"the re-seal never committed", 1, 1},
		{"the compaction never ran", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			path := filepath.Join(f.dir, "master.key")

			// The ring the middle of a rotation holds: both keys at once.
			ring, _, err := auth.LoadKeyRing(path)
			if err != nil {
				t.Fatalf("LoadKeyRing: %v", err)
			}
			first, _ := ring.Active()
			var second [32]byte
			copy(second[:], bytes.Repeat([]byte{0x5a}, 32))
			if werr := os.WriteFile(path, ringFile(t,
				map[uint32][32]byte{1: first, 2: second}, []uint32{1, 2}), 0o600); werr != nil {
				t.Fatalf("writing the interrupted ring: %v", werr)
			}
			if serr := f.store.SetKeyVersion(ctx, tc.dbVer); serr != nil {
				t.Fatalf("SetKeyVersion: %v", serr)
			}

			if _, err = f.svc.OpenMasterKey(ctx); err != nil {
				t.Fatalf("OpenMasterKey: %v", err)
			}
			aligned, _, err := auth.LoadKeyRing(path)
			if err != nil {
				t.Fatalf("LoadKeyRing: %v", err)
			}
			vs := aligned.Versions()
			if len(vs) != 1 || vs[0] != tc.wantOnly {
				t.Fatalf("the aligned ring holds %v, want only %d", vs, tc.wantOnly)
			}
		})
	}
}

// A wrong key file is found at startup rather than one failing sign-in at a
// time, and a second factor is the kind that refuses: its secret is the only
// copy of that factor.
func TestStartupRefusesAKeyThatCannotOpenAnEnrolledSecondFactor(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err := f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}

	// A different key at the same version is what restoring the wrong file
	// from a backup looks like.
	var wrong [32]byte
	copy(wrong[:], bytes.Repeat([]byte{0x11}, 32))
	path := filepath.Join(f.dir, "master.key")
	if err := os.WriteFile(path, ringFile(t,
		map[uint32][32]byte{1: wrong}, []uint32{1}), 0o600); err != nil {
		t.Fatalf("writing the wrong key: %v", err)
	}

	if _, err := f.svc.OpenMasterKey(ctx); err == nil {
		t.Fatal("a key that cannot open an enrolled second factor started")
	}
}

// A stored file-sharing credential is derived material one password change
// regenerates, so it warns instead of refusing: the deployment still serves.
func TestStartupSurvivesAKeyThatCannotOpenAnSMBCredential(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	var wrong [32]byte
	copy(wrong[:], bytes.Repeat([]byte{0x22}, 32))
	path := filepath.Join(f.dir, "master.key")
	if err := os.WriteFile(path, ringFile(t,
		map[uint32][32]byte{1: wrong}, []uint32{1}), 0o600); err != nil {
		t.Fatalf("writing the wrong key: %v", err)
	}

	if _, err := f.svc.OpenMasterKey(ctx); err != nil {
		t.Fatalf("a key that cannot open a derived credential refused startup: %v", err)
	}
}
