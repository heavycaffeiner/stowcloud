// Linux only, for the same reason as the rest of this package.
//go:build linux

// Content references, so no client composes a URL out of a path.
//
// A listing already knows every entry's own virtual path. Sealing that path
// into the row costs one encryption per entry and no extra request, which is
// what makes it the right shape for a grid: a two-step mint per tile would
// turn one paint into as many round trips as there are visible files.
//
// These claims are not credentials. The route that opens one refuses it
// unless the account it names is the account already signed in, so a claim in
// a browser history is worth nothing without that session. That is what buys
// them a lifetime long enough to outlive a scroll and a video seek, and it is
// the difference from the compatibility layer's direct URL, which is opened
// with no session at all and lives for minutes.
package lifecycle

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// sealClaim mints one claim naming an account and a virtual path.
func (e *Engine) sealClaim(
	purpose handler.ClaimPurpose, user core.UserID, vpath string, width, height int,
) (string, error) {
	return handler.SealClaim(e.claimKey, handler.Claim{
		Purpose: purpose,
		UserID:  int64(user),
		Path:    vpath,
		Width:   width,
		Height:  height,
	}, e.clk().Nanos())
}

// entryRefsFor seals the references one listing row carries.
//
// A directory gets none: there are no bytes to fetch and no preview to make.
// A file whose name the decoder does not recognise gets no thumbnail claim,
// which is what keeps a grid from asking for a preview of every text file.
func (e *Engine) entryRefsFor(user core.UserID, entry core.Entry, vpath string) handler.EntryRefs {
	if entry.IsDir || vpath == "" {
		return handler.EntryRefs{}
	}

	var refs handler.EntryRefs
	content, err := e.sealClaim(handler.PurposeContent, user, vpath, 0, 0)
	if err != nil {
		// A row without its reference still lists, and the screen falls back
		// to its type icon. Failing the listing over a preview would be the
		// worse trade.
		e.log().Warn("an entry's content reference could not be sealed",
			"subsystem", "files", "error", err)
		return handler.EntryRefs{}
	}
	refs.Content = content

	if handler.Previewable(entry) {
		thumb, terr := e.sealClaim(handler.PurposeThumb, user, vpath, 0, 0)
		if terr != nil {
			e.log().Warn("an entry's thumbnail reference could not be sealed",
				"subsystem", "files", "error", terr)
			return refs
		}
		refs.Thumb = thumb
	}
	return refs
}

// openBoundClaim resolves a claim the caller presented, for one purpose.
//
// The account the claim names has to be the account making the request. The
// claim narrows what a session may fetch; it never says who the session is.
// Without this comparison any signed-in account could replay a claim it found
// in a history or a screenshot and read another account's file, which is
// exactly what the compatibility layer's unauthenticated direct URL does on
// purpose and what this must not.
//
// Every refusal answers as a missing file: a wrong purpose, a bad seal, an
// expired deadline and somebody else's claim are one answer, because telling
// them apart tells a caller which part of a forged claim to fix.
func (e *Engine) openBoundClaim(
	c *fiber.Ctx, purpose handler.ClaimPurpose, owner core.UserID,
) (handler.Claim, bool) {
	value := c.Query("claim")
	if value == "" {
		return handler.Claim{}, false
	}
	keys := map[uint32][]byte{e.claimKey.Version: e.claimKey.Key}
	cl, err := handler.OpenClaim(keys, purpose, value, e.clk().Nanos())
	if err != nil {
		return handler.Claim{}, false
	}
	if cl.UserID != int64(owner) {
		return handler.Claim{}, false
	}
	return cl, true
}

// ErrNoClaim reports a content request that carried no usable claim. It maps
// to the same answer a missing file gets.
var ErrNoClaim = errors.New("no usable content claim")

// entryView projects one entry with its own references.
func (e *Engine) entryView(owner core.UserID, r core.Resolved, entry core.Entry) handler.EntryView {
	vpath := e.vpath(owner, r, entry)
	return handler.EntryOf(entry, vpath, e.entryRefsFor(owner, entry, vpath))
}

// refsOf is the per-row sealer a listing hands to the projection.
func (e *Engine) refsOf(owner core.UserID) func(core.Entry, string) handler.EntryRefs {
	return func(entry core.Entry, vpath string) handler.EntryRefs {
		return e.entryRefsFor(owner, entry, vpath)
	}
}
