//go:build linux

// The trash listing, as a client reads it.
package files

import (
	"strconv"
)

// TrashView is one deleted entry.
type TrashView struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// OrigPath is where it was deleted from, so a person can tell two files
	// with the same name apart. Absent for an entry recorded before origins
	// were kept, where the name is all there is.
	OrigPath string `json:"orig_path,omitempty"`

	IsDir bool `json:"is_dir"`

	// Size and DeletedAtNs are decimal strings. A file past 2^53 bytes and
	// every nanosecond timestamp lose exactness as JavaScript numbers, and a
	// size that comes back wrong is a restore a person declines because they
	// think it will not fit.
	Size        string `json:"size"`
	DeletedAtNs string `json:"deleted_at_ns"`
}

// TrashOf projects one entry.
func TrashOf(e TrashEntry) TrashView {
	return TrashView{
		ID:          e.ID,
		Name:        e.Name,
		OrigPath:    e.OrigPath,
		IsDir:       e.IsDir,
		Size:        strconv.FormatUint(e.Size, 10),
		DeletedAtNs: strconv.FormatInt(e.DeletedAtNs, 10),
	}
}
