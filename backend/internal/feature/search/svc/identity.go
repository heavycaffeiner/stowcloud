//go:build linux

package svc

import "github.com/stowcloud/namesearch/index"

// namespaceOf converts the Stowcloud share identity used by search.Source into
// the neutral namespace identity used by the index package. The conversion is
// explicit at this adapter boundary; the index's uint32 on-disk encoding is
// unchanged.
func namespaceOf(share uint32) index.Namespace { return index.Namespace(share) }
