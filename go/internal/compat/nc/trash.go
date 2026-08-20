//go:build compat_nc

package nc

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The trash collection.
//
// The protocol has one flat trash per account, over however many per-share
// trashes the caller can reach. That flattening is this layer's job: the core
// keeps a trash per share because that is where a deleted file physically is,
// and the client has no concept of a share at all.

// TrashEntry is one deleted item, as this surface renders it.
type TrashEntry struct {
	// Name is what the entry is called in the flat collection. It has to be
	// unique across every share the caller can see, which the source
	// guarantees rather than this reconstructing.
	Name string
	// OriginalPath is where it was, which is what a client shows and what a
	// restore puts it back to.
	OriginalPath string
	DeletedAtS   int64
	Size         uint64
	IsDir        bool
	FileID       FileID
}

// TrashPort is what the layer needs to answer the collection.
type TrashPort interface {
	// List returns everything the caller can restore, across every share.
	List(ctx context.Context, user ncport.UserID) ([]TrashEntry, error)
	// Restore puts one entry back at the path recorded in it, which is what
	// the user means: the request's destination leaf is ignored, because a
	// client sends the name it wants and the entry knows where it came from.
	Restore(ctx context.Context, user ncport.UserID, name string) error
	// Delete removes one entry permanently.
	Delete(ctx context.Context, user ncport.UserID, name string) error
	// Empty removes everything, which is the collection-level delete.
	Empty(ctx context.Context, user ncport.UserID) error
}

// The trash properties this surface reports, beyond the ones every entry has.
const (
	trashFilenameProp = "trashbin-filename"
	trashOriginalProp = "trashbin-original-location"
	trashDeletedProp  = "trashbin-deletion-time"
	trashTitleProp    = "trashbin-title"
)

// TrashProps renders the vendor properties for one deleted entry.
//
// The deletion time is a plain seconds count rather than a formatted date: the
// client sorts on it, and a formatted value would sort lexically.
func TrashProps(e TrashEntry) []EmittedProp {
	return []EmittedProp{
		{Space: NSNextcloudX, Local: trashFilenameProp, Value: e.Name},
		{Space: NSNextcloudX, Local: trashOriginalProp, Value: e.OriginalPath},
		{Space: NSNextcloudX, Local: trashDeletedProp,
			Value: strconv.FormatInt(e.DeletedAtS, 10)},
		// The title is the original leaf, which is what a client shows in the
		// list. Deriving it from the flat name would show the disambiguating
		// suffix the collection needs and the user never chose.
		{Space: NSNextcloudX, Local: trashTitleProp, Value: leafOf(e.OriginalPath)},
	}
}

// trashList answers the collection.
func (l *Layer) trashList(ctx context.Context, user ncport.UserID) ([]TrashEntry, *OCSError) {
	if l.deps.Trash == nil {
		// No trash wired up is an empty collection rather than a refusal: the
		// client renders an empty screen, which is what a deployment with
		// nothing deleted looks like anyway.
		return nil, nil
	}
	entries, err := l.deps.Trash.List(ctx, user)
	if err != nil {
		return nil, ServerError("could not read the trash")
	}
	return entries, nil
}

// trashRestore puts one entry back.
//
// The destination the client sent is deliberately not read. The entry records
// where it came from, and that is where the user means it to go; honouring a
// client-chosen destination would let a restore write anywhere the caller can
// reach, which is a move dressed as an undo.
func (l *Layer) trashRestore(ctx context.Context, user ncport.UserID, name string) *OCSError {
	if l.deps.Trash == nil {
		return NotFound("no such entry")
	}
	if err := l.deps.Trash.Restore(ctx, user, name); err != nil {
		return NotFound("no such entry")
	}
	return nil
}

// trashDelete removes one entry permanently.
func (l *Layer) trashDelete(ctx context.Context, user ncport.UserID, name string) *OCSError {
	if l.deps.Trash == nil {
		return NotFound("no such entry")
	}
	if err := l.deps.Trash.Delete(ctx, user, name); err != nil {
		return NotFound("no such entry")
	}
	return nil
}

// trashEmpty removes everything.
func (l *Layer) trashEmpty(ctx context.Context, user ncport.UserID) *OCSError {
	if l.deps.Trash == nil {
		return nil
	}
	if err := l.deps.Trash.Empty(ctx, user); err != nil {
		return ServerError("could not empty the trash")
	}
	return nil
}

func leafOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ServeTrash dispatches a request against the trash collection.
//
// It is exported and takes a parsed target, because the compat WebDAV mount is
// routed by the server: this package describes what a path means and what to
// do with it, and the assembly decides which requests reach here.
func (l *Layer) ServeTrash(w http.ResponseWriter, r *http.Request, target DavTarget) {
	who, ok := l.authenticate(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	user := ncport.UserID(who.User)

	switch {
	case target.Kind == TargetTrashRoot && r.Method == "PROPFIND":
		entries, oerr := l.trashList(r.Context(), user)
		if oerr != nil {
			http.Error(w, oerr.Message, oerr.Code)
			return
		}
		l.writeTrashListing(w, r, entries)

	case target.Kind == TargetTrashRoot && r.Method == "DELETE":
		if oerr := l.trashEmpty(r.Context(), user); oerr != nil {
			http.Error(w, oerr.Message, oerr.Code)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case target.Kind == TargetTrashEntry && r.Method == "DELETE":
		if oerr := l.trashDelete(r.Context(), user, target.Entry); oerr != nil {
			http.Error(w, oerr.Message, oerr.Code)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case target.Kind == TargetTrashEntry && r.Method == "MOVE":
		// A move out of the trash is a restore. The destination header names
		// the restore collection and its leaf is ignored: the entry knows
		// where it came from, and that is where the user means it to go.
		if oerr := l.trashRestore(r.Context(), user, target.Entry); oerr != nil {
			http.Error(w, oerr.Message, oerr.Code)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		w.Header().Set("Allow", "PROPFIND, DELETE, MOVE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// writeTrashListing renders the collection.
//
// The document is a multistatus, which the WebDAV package owns, so this hands
// back the entries and their properties rather than writing the XML: a second
// writer here would be a second place the escaping has to be right.
func (l *Layer) writeTrashListing(w http.ResponseWriter, r *http.Request, entries []TrashEntry) {
	if l.deps.WriteTrash == nil {
		// Nothing to render with. An empty collection is the honest answer:
		// the client shows an empty screen rather than an error.
		w.WriteHeader(http.StatusMultiStatus)
		return
	}
	l.deps.WriteTrash(w, r, entries)
}
