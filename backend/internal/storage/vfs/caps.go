//go:build linux

package vfs

import local "github.com/stowcloud/storage/local"

type Support = local.Support
type Caps = local.Caps

const (
	SupportUnknown = local.SupportUnknown
	SupportPresent = local.SupportPresent
	SupportMissing = local.SupportMissing
	SupportBlocked = local.SupportBlocked
	SupportDenied  = local.SupportDenied
)

func Probe() Caps                  { return local.Probe() }
func RequireResolver(c Caps) error { return local.RequireResolver(c) }
