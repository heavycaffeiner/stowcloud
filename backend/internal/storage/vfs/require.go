//go:build linux

package vfs

import local "github.com/stowcloud/storage/local"

type ResolverError = local.ResolverError

var ErrResolverUnavailable = local.ErrResolverUnavailable
