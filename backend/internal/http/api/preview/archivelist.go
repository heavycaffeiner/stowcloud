//go:build linux

package preview

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	featurepreview "github.com/heavycaffeiner/stowcloud/backend/internal/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// ArchiveListDeps supplies what reading an archive's directory needs.
type ArchiveListDeps struct {
	Core           *files.Core
	Resolve        func(files.UserID, string, acl.Perms) (files.Resolved, error)
	AcquireArchive func() (release func(), ok bool)
	Logger         *slog.Logger
}

// ArchiveListHandler reads an existing archive's central directory.
func ArchiveListHandler(d ArchiveListDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := files.Owner(c)
		if !ok {
			middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
			return
		}
		resolved, err := d.Resolve(owner, c.Query("path"), acl.Read|acl.Download)
		if err != nil {
			middleware.Fail(c, err)
			return
		}
		encrypted, err := d.Core.ShareEncrypted(c.Request.Context(), resolved.Share())
		if err != nil {
			middleware.Fail(c, err)
			return
		}
		if encrypted {
			middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		release, acquired := d.AcquireArchive()
		if !acquired {
			middleware.Refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
			return
		}
		defer release()
		entry, random, err := d.Core.OpenRandom(c.Request.Context(), resolved)
		if err != nil {
			middleware.Fail(c, err)
			return
		}
		defer func() {
			if closeErr := random.Close(); closeErr != nil && d.Logger != nil {
				d.Logger.Warn("closing an archive", "name", entry.Name, "error", closeErr)
			}
		}()
		listing, err := featurepreview.ListArchive(c.Request.Context(), random, random.Size)
		if err != nil {
			middleware.Fail(c, files.ErrNotFound)
			return
		}
		c.JSON(http.StatusOK, ArchiveListingOf(listing))
	}
}

// ArchiveListingView is what is inside a zip, read from its own directory.
type ArchiveListingView struct {
	Entries []ArchiveEntryView `json:"entries"`

	// Truncated marks a listing that stopped at the ceiling. Without it a
	// client shows a partial archive as though it were the whole one.
	Truncated bool `json:"truncated,omitempty"`

	// Skipped counts members whose names were refused. They exist in the
	// archive and are not shown, which is different from not being there.
	Skipped int `json:"skipped,omitempty"`

	// TotalUncompressed is what extraction would cost, as a decimal string.
	// A caller weighs it against the file's own size to spot a bomb before
	// extracting anything.
	TotalUncompressed string `json:"total_uncompressed"`
}

// ArchiveEntryView is one member.
type ArchiveEntryView struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`

	Size       string `json:"size"`
	Compressed string `json:"compressed"`

	ModTimeNs string `json:"mtime_ns,omitempty"`
}

// ArchiveListingOf projects a parsed directory.
//
// Entries are never nil: an empty archive encodes as an empty array, because
// a client iterating a null gets a runtime error rather than zero members.
func ArchiveListingOf(l featurepreview.ArchiveListing) ArchiveListingView {
	out := ArchiveListingView{
		Entries:           make([]ArchiveEntryView, 0, len(l.Entries)),
		Truncated:         l.Truncated,
		Skipped:           l.Skipped,
		TotalUncompressed: strconv.FormatUint(l.TotalUncompressed, 10),
	}
	for _, e := range l.Entries {
		v := ArchiveEntryView{
			Name:       e.Name,
			IsDir:      e.IsDir,
			Size:       strconv.FormatUint(e.Size, 10),
			Compressed: strconv.FormatUint(e.Compressed, 10),
		}
		if e.ModTimeNs != 0 {
			v.ModTimeNs = strconv.FormatInt(e.ModTimeNs, 10)
		}
		out.Entries = append(out.Entries, v)
	}
	return out
}
