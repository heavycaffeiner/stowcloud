// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
)

// Thumb answers GET /api/fs/thumb?path=...&size=small|medium|large.
//
// By path, not by fileid. The interface used to mint a signed link for each
// thumbnail, which needed `entry.id`, and that id is allocated lazily: a plain
// listing carries none, so the request could never be made for a file nobody
// had shared. Addressing the file the same way every other read does removes
// the dependency entirely.
//
// The bytes are served from this origin rather than the content origin. A
// thumbnail is a re-encode this server produced, not bytes a caller uploaded:
// the decoder ran in the jail, the output is a PNG this process wrote, and
// nothing of the original travels with it. There is nothing left for a browser
// to be tricked by.
func Thumb(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Preview == nil {
			// A build with no preview subsystem, which is a deployment choice
			// rather than an error. The listing says so too, so a client that
			// reads it never arrives here.
			return &apierr.RequestError{
				Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
				Message: "thumbnails are not available", Key: "fs.no_thumbnail",
			}
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		p, err := pathOf(r)
		if err != nil {
			return err
		}
		preset, perr := presetOf(r.URL.Query().Get("size"))
		if perr != nil {
			return perr
		}

		// The same permission the bytes need. A thumbnail is a derivative of
		// them, so seeing one is seeing the file.
		resolved, err := d.Core.Resolve(uid, p, acl.Read|acl.Download)
		if err != nil {
			return err
		}

		thumb, terr := d.Preview.Get(r.Context(), resolved, preset)
		if terr != nil {
			return thumbError(terr)
		}
		defer thumb.Close() //nolint:errcheck // the response is written either way.

		// Immutable and private. The key covers the identity, the mtime and
		// the size, so a changed file is a different URL rather than a stale
		// hit; private because the bytes are one account's to see.
		// PNG, which is what the encoder writes: lossless, keeps alpha, and
		// carries no metadata of its own, so the EXIF strip is a matter of
		// never copying anything across rather than of removing it.
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// A zero time leaves Last-Modified off and the range handling on,
		// which is what a body addressed by an immutable URL wants: the URL
		// changes when the file does, so there is nothing to revalidate.
		http.ServeContent(w, r, "", time.Time{}, thumb.File)
		return nil
	})
}

// presetOf maps the query value onto a preset.
func presetOf(s string) (preview.Preset, error) {
	switch s {
	case "", "small":
		return preview.PresetSmall, nil
	case "medium":
		return preview.PresetMedium, nil
	case "large":
		return preview.PresetLarge, nil
	}
	return 0, apierr.Unprocessable("fs.unknown_thumb_size", "size")
}

// thumbError turns a preview refusal into the answer a client reads.
//
// A file with no decoder is a 415 rather than a 500: it is a fact about the
// file, and the interface draws its type icon instead. Anything the account
// may not see is already a refusal from Resolve above.
func thumbError(err error) error {
	switch {
	case errors.Is(err, preview.ErrUnsupported):
		return &apierr.RequestError{
			Status: http.StatusUnsupportedMediaType, Code: apierr.CodeInvalidRequest,
			Message: "no thumbnail for this file", Key: "fs.no_thumbnail",
		}
	case errors.Is(err, core.ErrNotFound):
		return err
	}
	return err
}

// thumbnailable reports whether this name is worth asking for a thumbnail of.
//
// The extension, not the content: this runs once per listing row and opening
// every file in a directory to sniff its magic bytes would make a listing cost
// a read per entry. It is a hint, and the thumbnail route is the authority: a
// file named .jpg that is not one gets a 415 and the interface keeps its icon.
//
// The set is exactly what internal/preview can decode. A name outside it is
// never asked for, which is the point: it is what stops the client requesting
// a thumbnail of every text file in a folder.
func thumbnailable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp":
		return true
	}
	return false
}

// previewJSON is what a listing row says about its thumbnail.
type previewJSON struct {
	// Available is the server saying it can probably re-encode this file. The
	// client's own guard reads it, and it was never sent: every entry looked
	// like a file with no thumbnail, which is why the grid has only ever drawn
	// type icons.
	Available bool `json:"available"`
}
