//go:build linux && compat_nc

package lifecycle_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An account looking itself up by name learns its own quota.
//
// The app refreshes its stored profile from this route on every server-status
// check and writes whatever it parsed over the record it holds. An answer
// without the block leaves it holding zeros, which reads as an account with no
// space left, so uploads stop being offered while the disk is in fact empty.
func TestLookingUpYourOwnLoginReportsQuota(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	req := newReq(t, http.MethodGet, base+"/ocs/v2.php/cloud/users/alice?format=json", http.NoBody)
	req.Header.Set("Authorization", credential)
	req.Header.Set("OCS-APIRequest", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("looking up the account: %v", err)
	}
	defer closeRespBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the lookup answered %d", resp.StatusCode)
	}

	var body struct {
		OCS struct {
			Data struct {
				ID    string         `json:"id"`
				Quota map[string]any `json:"quota"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if derr := json.Unmarshal(readAllBody(t, resp.Body), &body); derr != nil {
		t.Fatalf("decoding the answer: %v", derr)
	}
	if body.OCS.Data.ID != "alice" {
		t.Fatalf("the lookup named %q", body.OCS.Data.ID)
	}
	if body.OCS.Data.Quota == nil {
		t.Fatal("the account's own lookup carried no quota, so a client stores zero and reads the disk as full")
	}
	// The client divides by total and compares free against the file it is
	// about to send, so both have to be there and neither may be zero.
	for _, field := range []string{"free", "total", "used"} {
		if _, ok := body.OCS.Data.Quota[field]; !ok {
			t.Errorf("the quota block has no %q", field)
		}
	}
}

// Both account routes report the storage's real free space.
//
// A client compares the file it is about to send against this number and
// holds the upload while the number is smaller, so a zero here is every
// upload refused on a disk with room to spare. The two routes have to agree:
// one client reads its quota from each.
func TestBothAccountRoutesReportRealFreeSpace(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	for _, route := range []string{"/ocs/v2.php/cloud/user", "/ocs/v2.php/cloud/users/alice"} {
		req := newReq(t, http.MethodGet, base+route+"?format=json", http.NoBody)
		req.Header.Set("Authorization", credential)
		req.Header.Set("OCS-APIRequest", "true")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		var body struct {
			OCS struct {
				Data struct {
					Quota struct {
						Free  int64 `json:"free"`
						Total int64 `json:"total"`
					} `json:"quota"`
				} `json:"data"`
			} `json:"ocs"`
		}
		derr := json.Unmarshal(readAllBody(t, resp.Body), &body)
		closeRespBody(t, resp)
		if derr != nil {
			t.Fatalf("%s: decoding: %v", route, derr)
		}
		if body.OCS.Data.Quota.Free <= 0 {
			t.Errorf("%s reported %d bytes free, so a client refuses every upload",
				route, body.OCS.Data.Quota.Free)
		}
		if body.OCS.Data.Quota.Total <= 0 {
			t.Errorf("%s reported a total of %d, which a client divides by",
				route, body.OCS.Data.Quota.Total)
		}
	}
}
