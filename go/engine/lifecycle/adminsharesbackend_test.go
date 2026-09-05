//go:build linux

package lifecycle

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/objstore"
)

// An empty secret_access_key in a patch is refused rather than treated as
// clearing the stored credential: a share with no credential cannot serve.
func TestApplyS3PatchRefusesAnEmptyCredential(t *testing.T) {
	cfg := objstore.Config{Bucket: "photos", Region: "us-east-1"}

	if _, err := applyS3Patch(&cfg, &shareS3Request{
		SecretAccessKey: new(""),
	}); err == nil {
		t.Fatal("an empty secret_access_key was accepted as a patch")
	}

	// An absent one leaves the stored credential alone and reports none.
	plain, err := applyS3Patch(&cfg, &shareS3Request{Prefix: new("team")})
	if err != nil {
		t.Fatalf("a patch naming no credential was refused: %v", err)
	}
	if plain != "" {
		t.Errorf("a patch that did not mention the credential reported one: %q", plain)
	}
	if cfg.Prefix != "team" {
		t.Errorf("the field the patch did name was not applied: %+v", cfg)
	}
}

// A present, non-empty secret is reported so the caller can seal and store
// it as the share's new credential.
func TestApplyS3PatchReportsANewCredential(t *testing.T) {
	cfg := objstore.Config{Bucket: "photos", Region: "us-east-1"}
	plain, err := applyS3Patch(&cfg, &shareS3Request{SecretAccessKey: new("new-key")})
	if err != nil {
		t.Fatalf("applyS3Patch: %v", err)
	}
	if plain != "new-key" {
		t.Errorf("the reported credential is %q, want new-key", plain)
	}
}

// The same refusal for veracrypt's password.
func TestApplyVeracryptPatchRefusesAnEmptyCredential(t *testing.T) {
	cfg := vault.Config{Container: "/srv/vaults/v.hc"}
	if _, err := applyVeracryptPatch(&cfg, &shareVeracryptRequest{
		Password: new(""),
	}); err == nil {
		t.Fatal("an empty password was accepted as a patch")
	}
}

// Create and SizeMiB name a path that runs once, at creation. A patch
// naming either is refused rather than silently ignored or, worse, acted
// on again against a container already in use.
func TestApplyVeracryptPatchRefusesCreateFields(t *testing.T) {
	cfg := vault.Config{Container: "/srv/vaults/v.hc"}
	if _, err := applyVeracryptPatch(&cfg, &shareVeracryptRequest{
		Create: new(true), SizeMiB: new(uint64(256)),
	}); err == nil {
		t.Fatal("a patch naming create and size_mib was accepted")
	}
}

// shareSpecOf refuses an s3 object carried alongside backend local, rather
// than silently ignoring it and storing a local share with no s3 fields.
func TestShareSpecOfRefusesAnS3ObjectAgainstBackendLocal(t *testing.T) {
	_, err := shareSpecOf(createShareRequest{
		Name: "docs", Host: "/srv/docs", Backend: "local",
		S3: &shareS3Request{Bucket: new("photos")},
	})
	if err == nil {
		t.Fatal("an s3 object against backend local was accepted")
	}
}

// A veracrypt backend with no password is refused on creation: this server
// cannot open, let alone create, a container it holds no password for.
func TestShareSpecOfRefusesVeracryptWithNoPassword(t *testing.T) {
	_, err := shareSpecOf(createShareRequest{
		Name: "vault", Backend: "veracrypt",
		Veracrypt: &shareVeracryptRequest{
			Container: new("/srv/vaults/v.hc"),
			Create:    new(true), SizeMiB: new(uint64(256)),
		},
	})
	if err == nil {
		t.Fatal("a veracrypt backend with no password was accepted")
	}
}
