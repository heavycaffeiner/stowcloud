// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// A ShareView carries no credential for any backend, by construction: the
// type has no field a secret could reach, and this asserts the encoded
// wire never carries the marker byte string a live credential would leave
// behind if one ever did.
func TestShareViewCarriesNoCredentialForAnyBackend(t *testing.T) {
	const marker = "do-not-leak-this-credential"

	cases := []struct {
		name  string
		share core.Share
	}{
		{"local", core.Share{ID: 1, Name: "docs", Host: "/srv/docs"}},
		{
			"s3",
			core.Share{
				ID: 2, Name: "bucket", Backend: core.BackendS3,
				Config: []byte(`{"bucket":"photos"}`),
				Secret: secret.New([]byte(marker)),
				Source: "s3://photos/team at https://minio:9000",
			},
		},
		{
			"veracrypt",
			core.Share{
				ID: 3, Name: "vault", Backend: core.BackendVeracrypt,
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
	view := ShareOf(core.Share{ID: 1, Name: "docs", Host: "/srv/docs"})
	if view.Backend != core.BackendLocal {
		t.Errorf("an empty backend rendered as %q, want %q", view.Backend, core.BackendLocal)
	}
}

// The source field carries the redacted location the opener produced,
// which for a local share is the host path, and for another backend is
// whatever Describe rendered rather than the host, which is empty for it.
func TestShareOfCarriesTheSourceField(t *testing.T) {
	view := ShareOf(core.Share{
		ID: 2, Name: "bucket", Backend: core.BackendS3,
		Source: "s3://photos/team at https://minio:9000",
	})
	if view.Source != "s3://photos/team at https://minio:9000" {
		t.Errorf("the source field is %q", view.Source)
	}
	if view.Host != "" {
		t.Errorf("a non-local share carries a host path: %q", view.Host)
	}
}
