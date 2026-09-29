package core

import (
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
)

// UserID is an account, as the auth layer addresses one. Opaque to the core:
// a grant names a user id, never a username.
type UserID int64

// ShareID is the VFS share id, the only id scheme the core recognises a
// share by.
//
// An alias rather than a distinct type: the VFS mints and consumes these
// ids, so a distinct type would force a conversion at every crossing while
// proving nothing, since both sides already share one id space.
type ShareID = vfs.ShareID

// Token is a caller-supplied validator: the ETag the client last saw, sent
// to prove nothing changed in between.
type Token string
