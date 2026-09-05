//go:build linux

package lifecycle_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// aliceID is the fixture account's own id, looked up rather than assumed: a
// hardcoded 1 would set the quota on whatever row happened to be first and
// pass for the wrong reason.
func aliceID(t *testing.T, e *lifecycle.Engine) int64 {
	t.Helper()
	id, err := e.Auth.UserIDByName(context.Background(), "alice")
	if err != nil {
		t.Fatalf("looking up the fixture account: %v", err)
	}
	return id
}

// A cap an administrator sets is a cap the server keeps.
//
// The quota dialog says "Uploads, copies, and writes that would exceed this
// are rejected". Before this, nothing in a running deployment consumed the
// ledger: the byte accounting existed, the check existed, and no wiring
// attached one to the other, so a 1 MB account uploaded 3 MB twice over and
// still reported zero bytes used.
func TestAnUploadPastTheQuotaIsRefused(t *testing.T) {
	base, sess, share, _, e, _ := contentShareGrant(t, everyPerm(), []byte("unused"))
	ctx := context.Background()

	quota := int64(1 << 20)
	if err := e.Auth.SetQuota(ctx, aliceID(t, e), &quota); err != nil {
		t.Fatalf("setting the quota: %v", err)
	}

	// Declared up front, the way the protocol does it, so the refusal costs
	// the client nothing but the request that asked.
	dir, leaf := "/"+share+"/", "too-big.bin"
	status, _, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Length":   strconv.Itoa(3 << 20),
			"Upload-Metadata": metadataFor(map[string]string{"dest": dir, "filename": leaf}),
		}, nil)
	if status == http.StatusCreated {
		t.Fatalf("a 3 MB upload opened against a 1 MB quota: %s", body)
	}
	if status != http.StatusInsufficientStorage {
		t.Fatalf("the refusal answered %d, want 507: %s", status, body)
	}
}

// A write inside the cap is served, and the account is charged for it.
//
// The other half of the same wiring: a ledger that refused everything would
// pass the test above and make the product unusable.
func TestAnUploadInsideTheQuotaIsServedAndCharged(t *testing.T) {
	want := payload()
	base, sess, share, _, e, _ := contentShareGrant(t, everyPerm(), []byte("unused"))
	ctx := context.Background()

	quota := int64(8 << 20)
	if err := e.Auth.SetQuota(ctx, aliceID(t, e), &quota); err != nil {
		t.Fatalf("setting the quota: %v", err)
	}

	location := createUpload(t, base, sess, "/"+share+"/fits.bin", len(want))
	if status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable": "1.0.0",
			"Upload-Offset": "0",
			"Content-Type":  "application/offset+octet-stream",
		}, want); status != http.StatusNoContent {
		t.Fatalf("sending the body answered %d: %s", status, body)
	}

	acct, err := e.Auth.AccountInfo(ctx, aliceID(t, e))
	if err != nil {
		t.Fatalf("reading the account: %v", err)
	}
	if acct.UsageBytes == 0 {
		t.Error("the account was charged nothing for a file it uploaded")
	}
	if acct.UsageBytes < uint64(len(want)) {
		t.Errorf("the account was charged %d bytes for a %d byte file", acct.UsageBytes, len(want))
	}
}

// An unlimited account is not gated by the ledger.
//
// The default, and the shape most deployments run: a nil cap has to pass
// every size rather than fall into the refusal path the two tests above
// exercise.
func TestAnAccountWithNoQuotaUploadsFreely(t *testing.T) {
	base, sess, share, _, _, _ := contentShareGrant(t, acl.Read|acl.Write|acl.Create, []byte("unused"))

	if location := createUpload(t, base, sess, "/"+share+"/unbounded.bin", 3<<20); location == "" {
		t.Fatal("an account with no cap could not open an upload")
	}
}

// A direct write past the cap is refused too, not only a resumable upload.
//
// The dialog promises "uploads, copies, and writes", and this is the route a
// small file and the editor take. Enforcing only the resumable path would
// leave the cap trivially avoidable by sending the bytes the other way.
func TestADirectWritePastTheQuotaIsRefused(t *testing.T) {
	base, sess, share, _, e, _ := contentShareGrant(t, everyPerm(), []byte("unused"))
	ctx := context.Background()

	quota := int64(1 << 10)
	if err := e.Auth.SetQuota(ctx, aliceID(t, e), &quota); err != nil {
		t.Fatalf("setting the quota: %v", err)
	}

	big := make([]byte, 64<<10)
	status, body := upload(t, base, sess, "/"+share+"/over-cap.bin", big)
	if status == http.StatusOK {
		t.Fatalf("a 64 KiB write landed against a 1 KiB quota: %s", body)
	}
	if status != http.StatusInsufficientStorage {
		t.Fatalf("the refusal answered %d, want 507: %s", status, body)
	}
}
