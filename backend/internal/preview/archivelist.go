//go:build linux

package preview

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// ArchiveListDeps supplies what reading an archive's directory needs.
type ArchiveListDeps struct {
	Core           *files.Core
	Resolve        func(files.UserID, string, acl.Perms) (files.Resolved, error)
	AcquireArchive func() (release func(), ok bool)
	Logger         *slog.Logger
}

type archiveListInput struct {
	Path string `query:"path"`
}
type archiveListOutput struct{ Body ArchiveListingView }

// ArchiveList reads an existing archive's central directory.
func ArchiveList(d ArchiveListDeps) func(context.Context, *archiveListInput) (*archiveListOutput, error) {
	return func(ctx context.Context, in *archiveListInput) (*archiveListOutput, error) {
		owner, err := files.OwnerFrom(ctx)
		if err != nil {
			return nil, err
		}
		resolved, err := d.Resolve(owner, in.Path, acl.Read|acl.Download)
		if err != nil {
			return nil, err
		}
		encrypted, err := d.Core.ShareEncrypted(ctx, resolved.Share())
		if err != nil {
			return nil, err
		}
		if encrypted {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
		release, acquired := d.AcquireArchive()
		if !acquired {
			return nil, apierr.AsClassified(apierr.ResourceExhausted, "archive.busy")
		}
		defer release()
		entry, random, err := d.Core.OpenRandom(ctx, resolved)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := random.Close(); closeErr != nil && d.Logger != nil {
				d.Logger.Warn("closing an archive", "name", entry.Name, "error", closeErr)
			}
		}()
		listing, err := ListArchive(ctx, random, random.Size)
		if err != nil {
			return nil, files.ErrNotFound
		}
		return &archiveListOutput{Body: ArchiveListingOf(listing)}, nil
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
func ArchiveListingOf(l ArchiveListing) ArchiveListingView {
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
