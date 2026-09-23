//go:build linux

// Share encryption: the HTTP surface over the files feature's zero-knowledge,
// per-share encryption contract, in the rclone-crypt format a real
// `rclone mount` can read.
//
// This server never sees a passphrase and performs no encryption. Every
// check here is boundary validation of what a client is about to hand over,
// not a decision about whether the value is any good as key material: this
// server holds nothing that could decrypt a rclone-crypt file, so it cannot
// tell a well-formed verifier from a maliciously constructed one, only a
// well-shaped one from a malformed one.
package app

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// saltLen is rclone's own password2 shape: base64url of 128 bits, with no
// padding, which is exactly 22 characters. Anything else does not carry the
// entropy the design assumes. It mirrors core's own unexported saltChars
// (core/encryption.go), which enforces the same bound again once this
// handler's checks have already passed; duplicated here, rather than
// imported, because core keeps its own bound unexported on purpose.
const saltLen = 22

// verifierMagic and verifierLen describe the rclone-crypt encryption of the
// contract's fixed 19-byte plaintext: rclone's 32-byte file header, one
// 16-byte Poly1305 tag, and the 19 bytes, for 67 total, starting with the
// format's own magic. Both mirror core's own unexported rcloneFileMagic and
// verifierBytes for the same reason saltLen mirrors saltChars above.
const verifierMagic = "RCLONE\x00\x00"

const verifierLen = 67

// shareEncryptionListView is the read response: every encrypted share the
// caller can see.
type shareEncryptionListView struct {
	Shares []handler.ShareEncryptionView `json:"shares"`
}

// shareEncryptionList answers every share with encryption turned on that the
// caller holds a grant on.
//
// Filtered through the caller's own projected roots rather than
// core.EncryptedShares, which lists every encrypted share on the whole
// deployment: reporting that list to an ordinary account would disclose the
// existence of a share it holds no grant on. Roots is the same mechanism the
// file listing and every DAV mount already use to answer "what can this
// caller see", so a share invisible there stays invisible here, and one
// projected under two labels (two grants on the same share) reports both.
func (e *Engine) shareEncryptionList(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	roots := e.Core.Roots(owner)
	order := make([]int64, 0, len(roots))
	labels := make(map[int64][]string, len(roots))
	for _, root := range roots {
		if _, seen := labels[root.Share]; !seen {
			order = append(order, root.Share)
		}
		labels[root.Share] = append(labels[root.Share], root.Label)
	}

	out := make([]handler.ShareEncryptionView, 0, len(order))
	for _, share := range order {
		// A root naming a share id too wide for a ShareID is a corrupt
		// grant, and skipping it keeps one from emptying the whole answer.
		id, nerr := num.Narrow[uint32](share)
		if nerr != nil {
			continue
		}
		enc, found, err := e.Core.EncryptionOf(c.Request.Context(), core.ShareID(id))
		if err != nil {
			fail(c, err)
			return
		}
		if !found {
			continue
		}
		out = append(out, handler.ShareEncryptionOf(core.ShareID(id), labels[share], enc))
	}
	writeJSON(c, http.StatusOK, shareEncryptionListView{Shares: out})
}

// enableShareEncryptionRequest is the body of turning encryption on for one
// share: the rclone-crypt parameters a client already derived and verified
// against itself before this request was ever sent.
type enableShareEncryptionRequest struct {
	Scheme   string `json:"scheme"`
	Salt     string `json:"salt"`
	Verifier string `json:"verifier"`
}

// shareEncryptionEnable turns end-to-end encryption on for one share.
//
// Administrator only: this is a decision about the deployment's own share
// registry, not something a grant to read or write inside a share also
// carries.
//
// The three checks below are the only validation there is, and the contract
// says so explicitly: nothing about a passphrase reaches this server, so
// scheme, salt and verifier can only be checked for shape, never for whether
// they were derived from a real passphrase. Every one of the three failures
// is 422; only the fourth, a non-empty share, comes back from core rather
// than being checked here, because whether a share is empty is exactly the
// fact core already has to look up to decide.
func (e *Engine) shareEncryptionEnable(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}

	var req enableShareEncryptionRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	if req.Scheme != core.SchemeRcloneCrypt {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_scheme"})
		return
	}
	if !validSalt(req.Salt) {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_salt"})
		return
	}
	verifier, verr := base64.StdEncoding.DecodeString(req.Verifier)
	if verr != nil || !validVerifierShape(verifier) {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_verifier"})
		return
	}

	err := e.Core.EnableEncryption(c.Request.Context(), id, core.Encryption{
		Scheme:   req.Scheme,
		Salt:     req.Salt,
		Verifier: verifier,
	})
	if err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			// Scheme, salt and verifier were already checked above, so the
			// only constraint core can still be refusing is the one it
			// enforces itself: the share is not empty.
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
			return
		}
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// validSalt reports whether s is rclone's own password2 shape: exactly 22
// characters of base64url. That length is what guarantees the salt carries
// the 128 bits of entropy the design assumes; nothing shorter or longer can.
func validSalt(s string) bool {
	if len(s) != saltLen {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			continue
		default:
			return false
		}
	}
	return true
}

// validVerifierShape reports whether decoded is the right length and starts
// with the format's own header. It is boundary validation, not decryption:
// this server holds no key to decrypt the verifier with, so the only thing
// worth asserting is that the client sent something of the shape it
// promised.
func validVerifierShape(decoded []byte) bool {
	return len(decoded) == verifierLen && strings.HasPrefix(string(decoded), verifierMagic)
}

// shareEncryptionDisable turns encryption off for one share.
//
// Idempotent: a share with no key material already answers success rather
// than not-found, the same way turning off a setting that is already off is
// not an error anywhere else on this surface. core.DisableEncryption already
// implements exactly this idempotence; nothing here has to duplicate it.
func (e *Engine) shareEncryptionDisable(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}

	if err := e.Core.DisableEncryption(c.Request.Context(), id); err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
			return
		}
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
