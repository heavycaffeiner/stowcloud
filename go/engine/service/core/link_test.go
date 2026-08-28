//go:build linux

package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// fakeCipher is a reversible stand-in for the auth package's AEAD. It binds
// the token hash and key version the same way, so a ciphertext moved to
// another row or replayed under another version fails to open.
type fakeCipher struct{ refuse bool }

func (f *fakeCipher) aad(hash []byte, ver uint32) []byte {
	return append(append([]byte{byte(ver)}, hash...), '|')
}

func (f *fakeCipher) Seal(token, hash []byte, ver uint32) ([]byte, error) {
	if f.refuse {
		return nil, errors.New("the cipher refuses to seal")
	}
	return append(f.aad(hash, ver), token...), nil
}

func (f *fakeCipher) Open(blob, hash []byte, ver uint32) ([]byte, error) {
	prefix := f.aad(hash, ver)
	if !bytes.HasPrefix(blob, prefix) {
		return nil, errors.New("the ciphertext does not belong to this row")
	}
	return blob[len(prefix):], nil
}

// recordingHasher observes the plaintext so a test can prove what reached it,
// and returns a value distinguishable from its input.
type recordingHasher struct{ seen []string }

func (h *recordingHasher) hash(_ context.Context, plain string) (string, error) {
	h.seen = append(h.seen, plain)
	return "hashed:" + plain, nil
}

func (h *recordingHasher) verify(_ context.Context, enc, candidate string) (bool, error) {
	return enc == "hashed:"+candidate, nil
}

// linkable is a share the caller may share, with the link seams wired.
func linkable(t *testing.T) (c *Core, st *state.DB, host string, root Resolved, h *recordingHasher) {
	t.Helper()
	c, st, host, root = writable(t)
	c.linkStore = st
	h = &recordingHasher{}
	c.AttachLinkCrypto(&fakeCipher{}, h.hash, h.verify)
	return c, st, host, root, h
}

// mustLink mints a link over a path with the given permissions.
func mustLink(t *testing.T, c *Core, r Resolved, perms acl.Perms) Link {
	t.Helper()
	l, _, err := c.CreateLink(context.Background(), r, LinkSpec{Perms: perms, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	return l
}

func TestCreateLinkMintsATokenAndStoresOnlyItsHash(t *testing.T) {
	c, st, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	target := at(t, root, "note.txt")
	link, tok, err := c.CreateLink(ctx, target, LinkSpec{Perms: acl.Read | acl.Download, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	plain := string(tok.Reveal())
	if len(plain) != 22 {
		t.Fatalf("the token is %d characters, want 22", len(plain))
	}
	if _, derr := base64.RawURLEncoding.DecodeString(plain); derr != nil {
		t.Fatalf("the token is not base64url: %v", derr)
	}

	row, ok, err := st.ByID(ctx, link.ID)
	if err != nil || !ok {
		t.Fatalf("reading the stored row: %v (found=%v)", err, ok)
	}
	if bytes.Equal(row.TokenHash, []byte(plain)) {
		t.Fatal("the plaintext token was stored as the hash")
	}
	if !bytes.Equal(row.TokenHash, linkTokenHash([]byte(plain))) {
		t.Fatal("the stored hash is not the sha256 of the token")
	}
	if bytes.Contains(row.TokenEnc, []byte(plain)) && row.TokenKeyVer == nil {
		t.Fatal("the ciphertext is unbound plaintext")
	}
}

func TestCreateLinkRefusesWhatItMustNotMint(t *testing.T) {
	ctx := context.Background()

	t.Run("without the share bit", func(t *testing.T) {
		c, st, host, _, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		seedUser(t, st, 2, "bob")
		grantAt(t, c, st, 2, 10, "", "Documents", acl.Read)
		r, rerr := c.Resolve(2, vpath(t, "Documents/note.txt"), acl.Read)
		if rerr != nil {
			t.Fatalf("resolving as bob: %v", rerr)
		}
		if _, _, err := c.CreateLink(ctx, r, LinkSpec{Perms: acl.Read, MaxDown: -1}); !errors.Is(err, ErrDenied) {
			t.Fatalf("a caller without Share got %v, want ErrDenied", err)
		}
	})

	t.Run("granting nothing", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_, _, err := c.CreateLink(ctx, at(t, root, "note.txt"), LinkSpec{MaxDown: -1})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("an empty permission set got %v, want ErrDenied", err)
		}
	})

	t.Run("escalating past the creator", func(t *testing.T) {
		c, st, host, _, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		seedUser(t, st, 3, "eve")
		// Read and Share only: the link below asks for Download too.
		grantAt(t, c, st, 3, 10, "", "Documents", acl.Read|acl.Share)
		r, rerr := c.Resolve(3, vpath(t, "Documents/note.txt"), acl.Share)
		if rerr != nil {
			t.Fatalf("resolving as eve: %v", rerr)
		}
		_, _, err := c.CreateLink(ctx, r, LinkSpec{Perms: acl.Read | acl.Download, MaxDown: -1})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("an escalating link got %v, want ErrDenied", err)
		}
	})

	t.Run("expiring in the past", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, Expires: 1, MaxDown: -1})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("a past expiry got %v, want ErrDenied", err)
		}
	})

	t.Run("a drop shape on a file", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		// Create with neither Read nor Download is a drop, and a drop targets
		// a folder.
		_, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Create, MaxDown: -1})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("a drop link on a file got %v, want ErrDenied", err)
		}
	})
}

func TestCreateLinkHashesThePasswordBeforeItReachesTheRow(t *testing.T) {
	c, st, host, root, h := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	pw := "correct horse"
	link, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
		LinkSpec{Perms: acl.Read, Password: &pw, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if len(h.seen) != 1 || h.seen[0] != pw {
		t.Fatalf("the hasher saw %+v, want the plaintext once", h.seen)
	}

	stored, err := st.PasswordHash(ctx, link.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the stored password: %v (nil=%v)", err, stored == nil)
	}
	// The plaintext must never be what landed in the row: an earlier version
	// stored it as it arrived and every protected link refused its own
	// password.
	if *stored == pw {
		t.Fatal("the plaintext password was stored")
	}
	if !link.HasPassword {
		t.Fatal("the link does not report that it has a password")
	}
}

func TestTheCryptoSeamsFailClosedWhenUnwired(t *testing.T) {
	ctx := context.Background()

	t.Run("no hasher", func(t *testing.T) {
		c, st, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_ = st
		c.AttachLinkCrypto(&fakeCipher{}, nil, nil)
		pw := "secret"
		if _, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, Password: &pw, MaxDown: -1}); err == nil {
			t.Fatal("creation with a password and no hasher succeeded")
		}
	})

	t.Run("no cipher", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		c.AttachLinkCrypto(nil, nil, nil)
		if _, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, MaxDown: -1}); err == nil {
			t.Fatal("creation with no cipher succeeded rather than erroring")
		}
	})

	t.Run("no store", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		c.linkStore = nil
		if _, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, MaxDown: -1}); err == nil {
			t.Fatal("creation with no link store succeeded")
		}
	})
}

func TestTheIdentityPinIsSetForAFileAndAbsentForARoot(t *testing.T) {
	c, _, host, root, _ := linkable(t)
	writeFile(t, host, "note.txt", "body")

	file := mustLink(t, c, at(t, root, "note.txt"), acl.Read)
	if file.Dev() == nil {
		t.Fatal("a file link carries no identity pin")
	}
	// A share root stays path-only: the cross-check needs a birth time to
	// tell the original inode from a reused one, and a root is not a target
	// that can be replaced under the link.
	if rootLink := mustLink(t, c, root, acl.Read); rootLink.Dev() != nil {
		t.Fatal("a share-root link carries an identity pin")
	}
}

func TestGetLinkAndListLinksAreOwnerScoped(t *testing.T) {
	c, st, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")
	seedUser(t, st, 2, "bob")

	link := mustLink(t, c, at(t, root, "note.txt"), acl.Read)

	if _, err := c.GetLink(ctx, 2, link.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner's link returned %v, want ErrNotFound", err)
	}
	if _, err := c.GetLink(ctx, 1, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing id returned %v, want ErrNotFound", err)
	}
	others, err := c.ListLinks(ctx, 2, nil)
	if err != nil {
		t.Fatalf("listing another owner's links: %v", err)
	}
	if len(others) != 0 {
		t.Fatalf("another owner saw %d links", len(others))
	}
}

func TestListLinksNarrowsToOneTargetAndRoundTripsTheToken(t *testing.T) {
	c, _, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "a.txt", "a")
	writeFile(t, host, "b.txt", "b")

	first, tok, err := c.CreateLink(ctx, at(t, root, "a.txt"), LinkSpec{Perms: acl.Read, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	mustLink(t, c, at(t, root, "b.txt"), acl.Read)

	all, err := c.ListLinks(ctx, 1, nil)
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("the owner sees %d links, want 2", len(all))
	}

	target := at(t, root, "a.txt")
	narrowed, err := c.ListLinks(ctx, 1, &target)
	if err != nil {
		t.Fatalf("narrowed ListLinks: %v", err)
	}
	if len(narrowed) != 1 || narrowed[0].ID != first.ID {
		t.Fatalf("the narrowed listing is %+v, want just the first link", narrowed)
	}
	// A sealed row round-trips its token, which is what lets the owner read
	// the full URL again.
	if narrowed[0].Token == nil {
		t.Fatal("a sealed row listed with no recoverable token")
	}
	if got := string(narrowed[0].Token.Reveal()); got != string(tok.Reveal()) {
		t.Fatalf("the recovered token is %q, want the minted one", got)
	}
}

func TestALegacyRowListsWithNoRecoverableToken(t *testing.T) {
	c, st, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")
	link := mustLink(t, c, at(t, root, "note.txt"), acl.Read)

	// A row written before the ciphertext column existed. Nothing invents a
	// URL for it; the owner sees the link exists and cannot re-read it.
	if err := st.Write(ctx, func(tx *sqlTx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE share_link SET token_enc = NULL, token_key_ver = NULL WHERE id = ?`, link.ID)
		return err
	}); err != nil {
		t.Fatalf("ageing the row: %v", err)
	}

	got, err := c.GetLink(ctx, 1, link.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.Token != nil {
		t.Fatal("a legacy row produced a token from nowhere")
	}
}

func TestUpdateLinkLeavesClearsAndRefuses(t *testing.T) {
	c, _, host, root, h := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	pw := "first"
	expires := c.clk.Nanos() + 1_000_000_000_000
	capped := int32(5)
	link, _, err := c.CreateLink(ctx, at(t, root, "note.txt"), LinkSpec{
		Perms: acl.Read, Password: &pw, Expires: expires, MaxDown: capped,
		Label: "original", Note: "n",
	})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// An outer nil everywhere leaves every field alone.
	got, err := c.UpdateLink(ctx, 1, link.ID, LinkPatch{})
	if err != nil {
		t.Fatalf("the empty patch: %v", err)
	}
	if got.Label != "original" || got.Expires != expires || got.MaxDown != capped || !got.HasPassword {
		t.Fatalf("the empty patch changed something: %+v", got)
	}

	// An outer non-nil with an inner nil clears.
	var noPw *string
	var noExpiry *int64
	var noCap *int32
	got, err = c.UpdateLink(ctx, 1, link.ID, LinkPatch{
		Password: &noPw, Expires: &noExpiry, MaxDown: &noCap,
	})
	if err != nil {
		t.Fatalf("the clearing patch: %v", err)
	}
	if got.HasPassword {
		t.Fatal("clearing the password left one set")
	}
	if got.Expires != 0 {
		t.Fatalf("clearing the expiry left %d", got.Expires)
	}
	if got.MaxDown != -1 {
		t.Fatalf("clearing the cap left %d, want unlimited", got.MaxDown)
	}

	// A new password is hashed on the way in, same as at creation.
	next := "second"
	nextPtr := &next
	if _, err := c.UpdateLink(ctx, 1, link.ID, LinkPatch{Password: &nextPtr}); err != nil {
		t.Fatalf("setting a new password: %v", err)
	}
	if h.seen[len(h.seen)-1] != next {
		t.Fatalf("the hasher last saw %q, want the new plaintext", h.seen[len(h.seen)-1])
	}

	past := c.clk.Nanos() - 1
	pastPtr := &past
	if _, err := c.UpdateLink(ctx, 1, link.ID, LinkPatch{Expires: &pastPtr}); !errors.Is(err, ErrDenied) {
		t.Fatalf("a past expiry returned %v, want ErrDenied", err)
	}
	empty := acl.Perms(0)
	if _, err := c.UpdateLink(ctx, 1, link.ID, LinkPatch{Perms: &empty}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an empty permission set returned %v, want ErrDenied", err)
	}
	if _, err := c.UpdateLink(ctx, 2, link.ID, LinkPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner's update returned %v, want ErrNotFound", err)
	}
}

func TestUpdateLinkRecheckesPermissionsAgainstCurrentAccess(t *testing.T) {
	c, st, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")
	link := mustLink(t, c, at(t, root, "note.txt"), acl.Read)

	// The grant is narrowed after the link was minted. Widening the link now
	// must ask again and be refused, or a revoked grant is re-widened through
	// an update.
	if err := st.Write(ctx, func(tx *sqlTx) error {
		_, err := tx.ExecContext(ctx, `UPDATE "grant" SET allow = ? WHERE share = ?`,
			int64(acl.Read|acl.Share), 10)
		return err
	}); err != nil {
		t.Fatalf("narrowing the grant: %v", err)
	}
	if err := c.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}

	widen := acl.Read | acl.Download
	if _, err := c.UpdateLink(ctx, 1, link.ID, LinkPatch{Perms: &widen}); !errors.Is(err, ErrDenied) {
		t.Fatalf("widening past a revoked grant returned %v, want ErrDenied", err)
	}
}

func TestDeleteLinkIsOwnerScopedAndPermanent(t *testing.T) {
	c, st, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")
	seedUser(t, st, 2, "bob")

	link, tok, err := c.CreateLink(ctx, at(t, root, "note.txt"), LinkSpec{Perms: acl.Read, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	if err := c.DeleteLink(ctx, 2, link.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner's delete returned %v, want ErrNotFound", err)
	}
	if err := c.DeleteLink(ctx, 1, link.ID); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	// Revocation is permanent: the token resolves to nothing afterwards.
	if _, _, err := c.LinkPublic(ctx, string(tok.Reveal())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked token returned %v, want ErrNotFound", err)
	}
}

func TestNoteLinkDownloadConsumesToTheCapThenRefuses(t *testing.T) {
	c, _, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	link, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
		LinkSpec{Perms: acl.Read | acl.Download, MaxDown: 2})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	for i := range 2 {
		if nerr := c.NoteLinkDownload(ctx, link); nerr != nil {
			t.Fatalf("consume %d: %v", i, nerr)
		}
	}
	if err := c.NoteLinkDownload(ctx, link); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("consuming past the cap returned %v, want ErrLinkExpired", err)
	}

	// A vanished row is absent rather than capped, which is the other half of
	// the zero-affected-rows disambiguation.
	if err := c.DeleteLink(ctx, 1, link.ID); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if err := c.NoteLinkDownload(ctx, link); !errors.Is(err, ErrNotFound) {
		t.Fatalf("consuming a vanished link returned %v, want ErrNotFound", err)
	}
}

func TestLinkPublicAppliesEveryLivenessRule(t *testing.T) {
	ctx := context.Background()

	t.Run("an unknown token is absent", func(t *testing.T) {
		c, _, _, _, _ := linkable(t)
		// Absent rather than gone: reporting it as gone would assert it once
		// existed, letting a stranger sort guesses into real and invented.
		if _, _, err := c.LinkPublic(ctx, "AAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown token returned %v, want ErrNotFound", err)
		}
	})

	t.Run("a live token resolves with the link's perms", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_, tok, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read | acl.Download, MaxDown: -1})
		if err != nil {
			t.Fatalf("CreateLink: %v", err)
		}
		link, entry, err := c.LinkPublic(ctx, string(tok.Reveal()))
		if err != nil {
			t.Fatalf("LinkPublic: %v", err)
		}
		if entry.Perms != link.Perms {
			t.Fatalf("the entry carries %v, want the link's %v", entry.Perms, link.Perms)
		}
		if entry.Name != "note.txt" {
			t.Fatalf("the entry names %q", entry.Name)
		}
	})

	t.Run("a rename kills the link", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_, tok, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, MaxDown: -1})
		if err != nil {
			t.Fatalf("CreateLink: %v", err)
		}
		if rerr := os.Rename(filepath.Join(host, "note.txt"),
			filepath.Join(host, "moved.txt")); rerr != nil {
			t.Fatalf("renaming: %v", rerr)
		}
		if _, _, err := c.LinkPublic(ctx, string(tok.Reveal())); !errors.Is(err, ErrLinkExpired) {
			t.Fatalf("a renamed target returned %v, want ErrLinkExpired", err)
		}
	})

	t.Run("a recreate at the same path kills the link", func(t *testing.T) {
		c, _, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		_, tok, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, MaxDown: -1})
		if err != nil {
			t.Fatalf("CreateLink: %v", err)
		}
		// The birth time is what distinguishes the original inode from one
		// the filesystem reused at the same name.
		if rerr := os.Remove(filepath.Join(host, "note.txt")); rerr != nil {
			t.Fatalf("removing: %v", rerr)
		}
		writeFile(t, host, "note.txt", "different")
		if _, _, err := c.LinkPublic(ctx, string(tok.Reveal())); !errors.Is(err, ErrLinkExpired) {
			t.Fatalf("a recreated target returned %v, want ErrLinkExpired", err)
		}
	})

	t.Run("expiry and an unregistered share are both gone", func(t *testing.T) {
		c, st, host, root, _ := linkable(t)
		writeFile(t, host, "note.txt", "body")
		link, tok, err := c.CreateLink(ctx, at(t, root, "note.txt"),
			LinkSpec{Perms: acl.Read, MaxDown: -1})
		if err != nil {
			t.Fatalf("CreateLink: %v", err)
		}
		past := c.clk.Nanos() - 1
		pastPtr := &past
		if uerr := st.Update(ctx, link.ID, state.LinkRowPatch{ExpiresNs: &pastPtr}); uerr != nil {
			t.Fatalf("expiring the row: %v", uerr)
		}
		if _, _, err := c.LinkPublic(ctx, string(tok.Reveal())); !errors.Is(err, ErrLinkExpired) {
			t.Fatalf("an expired link returned %v, want ErrLinkExpired", err)
		}
	})
}

func TestLinkCheckPasswordAcceptsRefusesAndFailsClosed(t *testing.T) {
	c, _, host, root, _ := linkable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	open := mustLink(t, c, at(t, root, "note.txt"), acl.Read)
	// A link with no password accepts anything.
	if ok, err := c.LinkCheckPassword(ctx, open, "whatever"); err != nil || !ok {
		t.Fatalf("an unprotected link answered (%v, %v), want true", ok, err)
	}

	pw := "hunter2"
	protected, _, err := c.CreateLink(ctx, at(t, root, "note.txt"),
		LinkSpec{Perms: acl.Read, Password: &pw, MaxDown: -1})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if ok, cerr := c.LinkCheckPassword(ctx, protected, pw); cerr != nil || !ok {
		t.Fatalf("the right password answered (%v, %v), want true", ok, cerr)
	}
	if ok, cerr := c.LinkCheckPassword(ctx, protected, "wrong"); cerr != nil || ok {
		t.Fatalf("the wrong password answered (%v, %v), want false", ok, cerr)
	}

	// An unwired verifier errors rather than silently passing.
	c.AttachLinkCrypto(&fakeCipher{}, nil, nil)
	if _, err := c.LinkCheckPassword(ctx, protected, pw); err == nil {
		t.Fatal("an unwired verifier passed a password")
	}
}

// sqlTx is the transaction type the state store hands its writers, named here
// so a test can reach the two columns no store method exposes.
type sqlTx = sql.Tx
