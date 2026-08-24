// Linux only, because what it tests is.
//go:build linux

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// A password-protected link, from minting to opening.
//
// The whole round trip, because either half alone passed while the feature was
// broken: the password was written to the row exactly as it arrived, and the
// verifier read it as a hash. Every protected link refused its own password,
// and the plaintext sat in the database.
//
// A test that checked only the hashing, or only the verifying, would not have
// noticed. What was wrong was that they disagreed about what the column holds.

// fakeHasher is a stand-in for the auth service's password path, which cannot
// be built here. It is deliberately not the identity function: what is being
// checked is that whatever the hasher produces is what the verifier reads.
// fakeCipher stands in for the master-key cipher, which needs a key ring this
// test has no reason to build. It binds the token to its own hash and key
// version, so a blob opened under the wrong pair is refused, which is the
// property the real one is for.
type fakeCipher struct{}

func (fakeCipher) Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return fmtSeal(token, tokenHash, keyVer), nil
}

func (fakeCipher) Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error) {
	prefix := fmtBind(tokenHash, keyVer)
	rest, ok := bytesCutPrefix(blob, prefix)
	if !ok {
		return nil, errors.New("the blob was not sealed under this hash and key version")
	}
	return rest, nil
}

func fmtBind(tokenHash []byte, keyVer uint32) []byte {
	return fmt.Appendf(nil, "v%d:%x:", keyVer, tokenHash)
}

func fmtSeal(token, tokenHash []byte, keyVer uint32) []byte {
	return append(fmtBind(tokenHash, keyVer), token...)
}

func bytesCutPrefix(b, prefix []byte) ([]byte, bool) {
	if len(b) < len(prefix) || string(b[:len(prefix)]) != string(prefix) {
		return nil, false
	}
	return b[len(prefix):], true
}

// A real digest rather than a marker prefix. The real hasher is the auth
// service's Argon2 path, which this package cannot build, and what matters
// here is that the stored value does not contain the password: a stand-in that
// kept the plaintext inside it would make the check that nothing is stored
// pass for the wrong reason.
func attachFakeCrypto(c *Core) {
	digest := func(plain string) string {
		sum := sha256.Sum256([]byte("link-password:" + plain))
		return hex.EncodeToString(sum[:])
	}
	c.AttachLinkCrypto(fakeCipher{},
		func(_ context.Context, plain string) (string, error) {
			return digest(plain), nil
		},
		func(_ context.Context, stored, candidate string) (bool, error) {
			return stored == digest(candidate), nil
		})
}

func linkFixture(t *testing.T) (*Core, UserID) {
	t.Helper()
	c, _, _ := testCore(t)
	attachFakeCrypto(c)
	grantShare(t, c)
	return c, 42
}

// grantShare widens the fixture's grant, which is read-only, to include the
// permission that minting a link needs. A link is a delegation of the
// creator's own access, so without it there is nothing to delegate.
func grantShare(t *testing.T, c *Core) {
	t.Helper()
	c.acl.ReplaceGrants([]acl.Grant{{
		ID: 1, User: 42, Share: 1, Subpath: acl.NewPath(),
		Allow: acl.Read | acl.Download | acl.Share, Inherit: true, Label: "docs",
	}})
}

// resolveTestFile resolves the fixture's own file the way a request does, so
// the link is minted against a real permission decision.
func resolveTestFile(t *testing.T, c *Core, owner UserID) Resolved {
	t.Helper()
	vp, err := vfs.ParseVpath("docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, rerr := c.Resolve(owner, vp, acl.Read)
	if rerr != nil {
		t.Fatalf("resolving the fixture's file: %v", rerr)
	}
	return r
}

func mintProtected(t *testing.T, c *Core, owner UserID, password string) Link {
	t.Helper()
	link, _, cerr := c.CreateLink(context.Background(), resolveTestFile(t, c, owner),
		LinkSpec{Perms: acl.Read, Password: &password})
	if cerr != nil {
		t.Fatalf("minting a link: %v", cerr)
	}
	return link
}

// The defect, in one test: a link has to accept the password it was created
// with.
func TestAProtectedLinkAcceptsItsOwnPassword(t *testing.T) {
	c, owner := linkFixture(t)
	link := mintProtected(t, c, owner, "correct horse battery staple")

	ok, err := c.LinkCheckPassword(context.Background(), link, "correct horse battery staple")
	if err != nil {
		t.Fatalf("checking the password: %v", err)
	}
	if !ok {
		t.Fatal("a link refused the password it was created with, so nobody can open it")
	}
}

func TestAProtectedLinkRefusesTheWrongPassword(t *testing.T) {
	c, owner := linkFixture(t)
	link := mintProtected(t, c, owner, "correct horse battery staple")

	for _, wrong := range []string{"", "wrong", "correct horse battery stapl"} {
		ok, err := c.LinkCheckPassword(context.Background(), link, wrong)
		if err != nil {
			t.Fatalf("checking %q: %v", wrong, err)
		}
		if ok {
			t.Errorf("%q opened a link it is not the password for", wrong)
		}
	}
}

// The plaintext must not be in the row. That is the other half of the defect:
// even once opening worked, a stored password is a stored password.
func TestThePlaintextIsNotStored(t *testing.T) {
	c, owner := linkFixture(t)
	const password = "a-very-distinctive-passphrase"
	link := mintProtected(t, c, owner, password)

	var stored string
	if err := c.state.SQL().QueryRowContext(ctx(), sqlLinkPassword, link.ID).Scan(&stored); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if strings.Contains(stored, password) {
		t.Fatalf("the row holds the password itself: %q", stored)
	}
	if stored == "" {
		t.Fatal("the row holds nothing, so the link is not protected at all")
	}
}

// A link with no password accepts anything, which is what an unprotected link
// is.
func TestAnUnprotectedLinkAcceptsAnything(t *testing.T) {
	c, owner := linkFixture(t)
	link, _, cerr := c.CreateLink(context.Background(), resolveTestFile(t, c, owner),
		LinkSpec{Perms: acl.Read})
	if cerr != nil {
		t.Fatal(cerr)
	}

	ok, verr := c.LinkCheckPassword(context.Background(), link, "")
	if verr != nil || !ok {
		t.Fatalf("an unprotected link refused an empty password: %v %v", ok, verr)
	}
}

// Changing the password through an update goes through the same hashing. A
// second path that wrote the plaintext would be the same defect again.
func TestAnUpdatedPasswordIsHashedToo(t *testing.T) {
	c, owner := linkFixture(t)
	link := mintProtected(t, c, owner, "the-first-password")

	next := "the-second-password"
	pw := &next
	if _, err := c.UpdateLink(context.Background(), owner, link.ID, LinkPatch{Password: &pw}); err != nil {
		t.Fatalf("updating: %v", err)
	}

	var stored string
	if err := c.state.SQL().QueryRowContext(ctx(), sqlLinkPassword, link.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, next) {
		t.Fatalf("an updated password was stored as itself: %q", stored)
	}

	ok, verr := c.LinkCheckPassword(context.Background(), link, next)
	if verr != nil {
		t.Fatal(verr)
	}
	if !ok {
		t.Error("the link refuses the password it was just given")
	}

	// And the old one stops working, or the change was not a change.
	stale, serr := c.LinkCheckPassword(context.Background(), link, "the-first-password")
	if serr != nil {
		t.Fatal(serr)
	}
	if stale {
		t.Error("the previous password still opens the link")
	}
}

// Clearing the password removes the protection rather than setting it to an
// empty one, which would be a link only an empty string opens.
func TestClearingThePasswordRemovesTheProtection(t *testing.T) {
	c, owner := linkFixture(t)
	link := mintProtected(t, c, owner, "a-password")

	var cleared *string
	if _, err := c.UpdateLink(context.Background(), owner, link.ID, LinkPatch{Password: &cleared}); err != nil {
		t.Fatalf("clearing: %v", err)
	}

	ok, verr := c.LinkCheckPassword(context.Background(), link, "anything at all")
	if verr != nil {
		t.Fatal(verr)
	}
	if !ok {
		t.Fatal("a link with its password cleared still refuses one")
	}
}

// With no hasher attached, minting a protected link fails rather than storing
// what it was handed. The fallback nobody notices is writing the password down.
func TestWithNoHasherAProtectedLinkIsRefused(t *testing.T) {
	c, _, _ := testCore(t)
	grantShare(t, c)
	// The cipher is attached and the hasher deliberately is not, so what this
	// checks is the password path rather than the token path.
	c.AttachLinkCrypto(fakeCipher{}, nil, nil)

	password := "a-password"
	if _, _, cerr := c.CreateLink(context.Background(), resolveTestFile(t, c, 42),
		LinkSpec{Perms: acl.Read, Password: &password}); cerr == nil {
		t.Fatal("a protected link was minted with nothing to hash the password, so the plaintext was stored")
	}
}
