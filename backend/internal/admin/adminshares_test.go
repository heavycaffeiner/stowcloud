// Linux only, matching the package under test.
//go:build linux

package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// A ShareView carries no credential for any backend, by construction: the
// type has no field a secret could reach, and this asserts the encoded
// wire never carries the marker byte string a live credential would leave
// behind if one ever did.
func TestShareViewCarriesNoCredentialForAnyBackend(t *testing.T) {
	const marker = "do-not-leak-this-credential"

	cases := []struct {
		name  string
		share files.Share
	}{
		{"local", files.Share{ID: 1, Name: "docs", Host: "/srv/docs"}},
		{
			"s3",
			files.Share{
				ID: 2, Name: "bucket", Backend: files.BackendS3,
				Config: []byte(`{"bucket":"photos"}`),
				Secret: secret.New([]byte(marker)),
				Source: "s3://photos/team at https://minio:9000",
			},
		},
		{
			"veracrypt",
			files.Share{
				ID: 3, Name: "vault", Backend: files.BackendVeracrypt,
				Config: []byte(`{"container":"/srv/vaults/v.hc"}`),
				Secret: secret.New([]byte(marker)),
				Source: "/srv/vaults/v.hc",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := ShareOf(tc.share)
			raw, err := json.Marshal(view)
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			if strings.Contains(string(raw), marker) {
				t.Fatalf("the response carries the credential: %s", raw)
			}
			if view.Backend == "" {
				t.Error("the backend field is empty")
			}
		})
	}
}

// An empty Backend reads as local, so a row from before backends existed
// and a client that never learned the field still get a sensible answer.
func TestShareOfDefaultsAnEmptyBackendToLocal(t *testing.T) {
	view := ShareOf(files.Share{ID: 1, Name: "docs", Host: "/srv/docs"})
	if view.Backend != files.BackendLocal {
		t.Errorf("an empty backend rendered as %q, want %q", view.Backend, files.BackendLocal)
	}
}
